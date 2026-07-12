package daemon_test

// N3 acceptance: a durable subscription surfaces a semantically matching new
// send (NO shared keywords) in the next digest, marked; the cap is enforced;
// disable stops delivery; the events replay cleanly through reindex; updates
// are base-revision optimistic (stale rejects pre-ack).

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/projection"
)

// semanticStub is a deterministic embedder with a synonym table: it maps
// council/planning language and shire/development-application language to
// the same region of the vector space WITHOUT any shared tokens, which is
// exactly what the acceptance criterion demands of the pipeline. Semantic
// quality in production comes from the real model; this proves the plumbing.
type semanticStub struct{}

func (semanticStub) ModelID() string { return "test-semantic-v1" }
func (semanticStub) Dim() int        { return 3 }
func (semanticStub) Close() error    { return nil }

func (semanticStub) Embed(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		lower := strings.ToLower(t)
		switch {
		case strings.Contains(lower, "council") || strings.Contains(lower, "planning") ||
			strings.Contains(lower, "shire") || strings.Contains(lower, "development application"):
			out[i] = []float32{1, 0.1, 0} // the "planning approval" concept
		case strings.Contains(lower, "grinder") || strings.Contains(lower, "burr"):
			out[i] = []float32{0, 1, 0} // equipment concept
		default:
			out[i] = []float32{0, 0, 1} // everything else
		}
	}
	return out, nil
}

func startSubDaemon(t *testing.T, dir string) *daemon.Daemon {
	t.Helper()
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: semanticStub{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func enrich(t *testing.T, d *daemon.Daemon) {
	t.Helper()
	for {
		n, err := d.EnrichOnce(64)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

func digestFor(t *testing.T, d *daemon.Daemon, view string) *daemon.DigestOutput {
	t.Helper()
	out, err := d.Digest(daemon.DigestOptions{AgentView: view, BudgetChars: 8000})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestN3SemanticMatchSurfacesMarked(t *testing.T) {
	dir := initCairn(t)
	d := startSubDaemon(t, dir)

	// noise first, then the durable subscription
	for _, body := range []string{
		"the new burr grinder arrived and the hopper is loose",
		"quarterly bean order placed with the importer",
	} {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := d.SubscribeDurable(daemon.SubscribeRequest{
		OwnerView: "roastery", InterestQuery: "council planning approval",
	})
	if err != nil {
		t.Fatal(err)
	}

	// the semantically matching send shares NO keywords with the query
	match, err := d.Publish(daemon.PublishRequest{Actor: "operator",
		Body: "the shire signed off on the development application for the espresso bar"})
	if err != nil {
		t.Fatal(err)
	}
	enrich(t, d)

	out := digestFor(t, d, "roastery")
	if !strings.Contains(out.Payload, match.MessageID+" [subscription]") {
		t.Fatalf("matching send not surfaced/marked in digest:\n%s", out.Payload)
	}
	if strings.Count(out.Payload, "[subscription]") != 1 {
		t.Fatalf("distractors were marked too:\n%s", out.Payload)
	}

	// same message never re-delivered as a subscription match
	out2 := digestFor(t, d, "roastery")
	if strings.Contains(out2.Payload, "[subscription]") {
		t.Fatalf("second digest re-delivered the match:\n%s", out2.Payload)
	}

	_ = sub
}

func TestN3CapEnforcedAndDisableStopsDelivery(t *testing.T) {
	dir := initCairn(t)
	d := startSubDaemon(t, dir)

	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "burr grinder maintenance log"}); err != nil {
		t.Fatal(err)
	}
	sub, err := d.SubscribeDurable(daemon.SubscribeRequest{
		OwnerView: "ops", InterestQuery: "council planning approval",
		TopN: 1, PushCapPerDay: 1, // window and day cap both = 1
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{
		"shire approval letter for the extension arrived",
		"development application fees paid at the council office",
	} {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	enrich(t, d)

	// cap 1: exactly one of the two matches may surface
	out := digestFor(t, d, "ops")
	if n := strings.Count(out.Payload, "[subscription]"); n != 1 {
		t.Fatalf("cap 1 delivered %d matches:\n%s", n, out.Payload)
	}
	// allowance exhausted: the second match must NOT surface next digest
	out = digestFor(t, d, "ops")
	if strings.Contains(out.Payload, "[subscription]") {
		t.Fatalf("cap not enforced across digests:\n%s", out.Payload)
	}

	// disable stops delivery even after the window would reopen
	if _, err := d.SubscriptionDisable(sub.SubscriptionID, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator",
		Body: "another shire planning notice"}); err != nil {
		t.Fatal(err)
	}
	enrich(t, d)
	out = digestFor(t, d, "ops")
	if strings.Contains(out.Payload, "[subscription]") {
		t.Fatalf("disabled subscription still delivering:\n%s", out.Payload)
	}
}

func TestN3OptimisticUpdateAndValidation(t *testing.T) {
	dir := initCairn(t)
	d := startSubDaemon(t, dir)

	sub, err := d.SubscribeDurable(daemon.SubscribeRequest{OwnerView: "x", InterestQuery: "anything"})
	if err != nil {
		t.Fatal(err)
	}

	// stale base rejected pre-ack
	q := "new query"
	_, err = d.SubscriptionUpdate(daemon.SubUpdateRequest{
		SubscriptionID: sub.SubscriptionID, BaseRevision: 99, InterestQuery: &q,
	})
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale base accepted: %v", err)
	}

	// correct base applies and bumps the revision
	if _, err := d.SubscriptionUpdate(daemon.SubUpdateRequest{
		SubscriptionID: sub.SubscriptionID, BaseRevision: 1, InterestQuery: &q,
	}); err != nil {
		t.Fatal(err)
	}
	row, err := d.Projection().SubscriptionByID(sub.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Revision != 2 || row.InterestQuery != "new query" {
		t.Fatalf("update not applied: %+v", row)
	}
	// the PREVIOUS base is now stale
	if _, err := d.SubscriptionUpdate(daemon.SubUpdateRequest{
		SubscriptionID: sub.SubscriptionID, BaseRevision: 1, InterestQuery: &q,
	}); err == nil {
		t.Fatal("re-used base revision accepted")
	}

	// unknown topics reject pre-ack; unknown subscription rejects pre-ack
	if _, err := d.SubscribeDurable(daemon.SubscribeRequest{
		OwnerView: "x", InterestQuery: "q", Topics: []string{"no-such-topic"},
	}); err == nil || !strings.Contains(err.Error(), "before ack") {
		t.Fatalf("unknown topic accepted: %v", err)
	}
	if _, err := d.SubscriptionDisable("0190a1b2-c3d4-7e5f-8901-00000000dead", "operator"); err == nil {
		t.Fatal("unknown subscription disabled")
	}
	if _, err := d.SubscribeDurable(daemon.SubscribeRequest{OwnerView: "x", InterestQuery: ""}); err == nil {
		t.Fatal("empty interest query accepted")
	}
}

// Acceptance: subscription events replay cleanly through reindex — delete
// the derived projection, restart (recover replays the log), and the
// subscription table and digest behavior reproduce.
func TestN3ReplayThroughReindex(t *testing.T) {
	dir := initCairn(t)
	d := startSubDaemon(t, dir)

	sub, err := d.SubscribeDurable(daemon.SubscribeRequest{
		OwnerView: "replay", InterestQuery: "council planning approval", TopN: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	q := "council planning approval decisions"
	if _, err := d.SubscriptionUpdate(daemon.SubUpdateRequest{
		SubscriptionID: sub.SubscriptionID, BaseRevision: 1, InterestQuery: &q,
	}); err != nil {
		t.Fatal(err)
	}
	victim, err := d.SubscribeDurable(daemon.SubscribeRequest{OwnerView: "replay", InterestQuery: "doomed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SubscriptionDelete(victim.SubscriptionID, "operator"); err != nil {
		t.Fatal(err)
	}
	before, err := d.Projection().Subscriptions("", false)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	// destroy the DERIVED projection; recovery must rebuild it from the log
	dbPath := projection.DBPath(dir)
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	d2 := startSubDaemon(t, dir)
	after, err := d2.Projection().Subscriptions("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || len(after) != 1 {
		t.Fatalf("replayed subscriptions %d != original %d", len(after), len(before))
	}
	if fmt.Sprintf("%+v", after[0]) != fmt.Sprintf("%+v", before[0]) {
		t.Fatalf("replayed subscription differs:\n before %+v\n after  %+v", before[0], after[0])
	}
	if after[0].Revision != 2 || after[0].InterestQuery != q {
		t.Fatalf("optimistic update lost in replay: %+v", after[0])
	}

	// the rebuilt mesh still matches and marks
	if _, err := d2.Publish(daemon.PublishRequest{Actor: "operator",
		Body: "shire development application approved"}); err != nil {
		t.Fatal(err)
	}
	enrich(t, d2)
	out := digestFor(t, d2, "replay")
	if !strings.Contains(out.Payload, "[subscription]") {
		t.Fatalf("post-replay digest has no subscription match:\n%s", out.Payload)
	}
	var parked []projection.ParkedEvent
	if parked, err = d2.Projection().ParkedEvents(); err != nil {
		t.Fatal(err)
	}
	if len(parked) != 0 {
		t.Fatalf("replay parked %d subscription events: %+v", len(parked), parked)
	}
}
