package daemon_test

// FIX-J1 (RULINGS.md R49) — CROSS-NODE / CROSS-ORIGIN referential ordering.
//
// F1 made `send --topic <new>` emit topic.create + topic.link.add atomically ON
// THE ORIGIN. That atomicity is LOCAL. The live two-node run (Codex Brief A run
// 2) reproduced a topic.link.add originating on NODE-B replicating to NODE-A and
// PARKING on A's reindex with "FOREIGN KEY constraint failed" — the referenced
// topic did not yet exist in A's projection. atomic-on-origin is NOT
// atomic-across-the-mesh.
//
// The projection's Apply enforces per-origin sequence contiguity, so a
// SAME-origin link can never precede its create (R49.4 holds trivially there).
// The genuine failure is CROSS-ORIGIN: a topic.link.add on B's origin references
// a topic.create on A's origin; if B's origin is applied before A's during
// reconcile/reindex, the link's FK fails. R49 makes that park RETRYABLE and
// self-heal once A's create is applied.
//
// These tests are genuinely CROSS-NODE (two mesh nodes, real reconcile). A
// single-node stand-in does NOT satisfy J1.

import (
	"os"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
)

// reindexNode rebuilds a node's projection from the log alone and returns the
// reindex report (which counts parked events). RetryParked runs inside Replay,
// so a cross-origin ordering park self-heals during the rebuild (R49.2).
func reindexNode(t *testing.T, dir string) *projection.ReindexReport {
	t.Helper()
	dbPath := projection.DBPath(dir)
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	bodyFetch := projection.StoreBodyFetch(object.NewStore(fsx.OS{}, dir))
	rep, err := projection.ReindexLexical(fsx.OS{}, dir, dbPath, nil, bodyFetch)
	if err != nil {
		t.Fatalf("reindex %s: %v", dir, err)
	}
	return rep
}

// walkOrigin reads an origin's records + envelopes in sequence order from a dir
// that carries full mesh trust.
func walkOrigin(t *testing.T, dir string, o cairnlog.Origin) (recs [][]byte, envs []*event.Envelope) {
	t.Helper()
	trust, err := identity.MeshTrust(fsx.OS{}, dir)
	if err != nil {
		t.Fatalf("mesh trust: %v", err)
	}
	if _, err := cairnlog.Walk(fsx.OS{}, dir, o, trust.Verifier(), func(env *event.Envelope, rec []byte) error {
		recs = append(recs, append([]byte(nil), rec...))
		e := *env
		envs = append(envs, &e)
		return nil
	}); err != nil {
		t.Fatalf("walk origin %s/%d: %v", o.DeviceID, o.Generation, err)
	}
	return recs, envs
}

// TestJ1CrossNodeTopicLinkReplicatesCleanly is the primary J1 regression. A
// creates a topic; B links a message to it (a CROSS-ORIGIN link — the link is on
// B's origin, the topic.create on A's origin); the two nodes fully converge;
// BOTH reindex cleanly (zero parked), doctor exit 0, gates zero-loss PASS.
func TestJ1CrossNodeTopicLinkReplicatesCleanly(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	dB, cancelB, _ := startSyncNode(t, p.baseB, p.dirB, warnB)

	// A creates the topic (topic.create on A's origin) via an auto-created send.
	if _, err := dA.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "A seeds the shared topic",
		Topics: []string{"shared"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatalf("A send --topic shared: %v", err)
	}
	// B learns the topic.
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("B<-A reconcile: %v", err)
	}

	// B links a NEW message to the EXISTING topic — resolveTopic finds A's
	// topic_id, so B emits publish + topic.link.add (no create) on B's origin.
	// The link references a topic whose create lives on A's origin (cross-origin).
	if _, err := dB.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "B contributes to the shared topic",
		Topics: []string{"shared"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatalf("B send --topic shared: %v", err)
	}
	// Full bidirectional convergence.
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("B->A reconcile: %v", err)
	}
	if _, err := dA.SyncWith(dB.SyncAddr()); err != nil {
		t.Fatalf("A->B reconcile: %v", err)
	}

	cancelA()
	cancelB()
	dA.Close()
	dB.Close()

	// Both nodes reindex from the log alone; the cross-origin link must project
	// (self-healing if it parked mid-walk). Zero parked on both.
	for _, dir := range []string{p.dirA, p.dirB} {
		rep := reindexNode(t, dir)
		if rep.Parked != 0 {
			pj, _ := projection.Open(projection.DBPath(dir), nil)
			var parked []projection.ParkedEvent
			if pj != nil {
				parked, _ = pj.ParkedEvents()
				pj.Close()
			}
			t.Fatalf("J1 REGRESSION: reindex %s parked %d event(s): %+v", dir, rep.Parked, parked)
		}
		problems, _, err := daemon.DeepDoctor(fsx.OS{}, dir, projection.DBPath(dir), time.Now())
		if err != nil {
			t.Fatalf("deep doctor %s: %v", dir, err)
		}
		if len(problems) != 0 {
			t.Fatalf("J1: deep doctor %s problems: %v", dir, problems)
		}
	}

	// The link is actually live on A: the shared topic carries BOTH messages.
	pA, err := projection.Open(projection.DBPath(p.dirA), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pA.Close()
	topicID, err := pA.TopicIDByName("shared")
	if err != nil || topicID == "" {
		t.Fatalf("J1: topic 'shared' not projected on A (id=%q err=%v)", topicID, err)
	}
	msgs, err := pA.MessagesInTopicIDs([]string{topicID})
	if err != nil {
		t.Fatalf("J1: messages in topic: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("J1: expected 2 messages under 'shared' on A, got %d", len(msgs))
	}
}

// TestJ1AdversarialLinkBeforeCreate is the adversarial case R49 mandates,
// deterministically: B's origin (carrying a cross-origin topic.link.add) is
// applied to a fresh projection BEFORE A's origin (carrying the topic.create).
// The link must park RETRYABLE (never a terminal failure), then SELF-HEAL when
// A's create is applied and the retry sweep runs.
func TestJ1AdversarialLinkBeforeCreate(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	dB, cancelB, _ := startSyncNode(t, p.baseB, p.dirB, warnB)

	if _, err := dA.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "A seeds the shared topic",
		Topics: []string{"shared"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatalf("A send --topic shared: %v", err)
	}
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("B<-A reconcile: %v", err)
	}
	if _, err := dB.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "B contributes a cross-origin link",
		Topics: []string{"shared"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatalf("B send --topic shared: %v", err)
	}
	// Push B's origin to A so A holds BOTH origins (and full trust to verify).
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("B->A reconcile: %v", err)
	}
	cancelA()
	cancelB()
	dA.Close()
	dB.Close()

	oA := cairnlog.Origin{DeviceID: p.deviceA, Generation: config.FirstGeneration}
	oB := cairnlog.Origin{DeviceID: p.deviceB, Generation: config.FirstGeneration}
	recsA, envsA := walkOrigin(t, p.dirA, oA)
	recsB, envsB := walkOrigin(t, p.dirA, oB)

	// Sanity: B's origin carries a topic.link.add, A's a topic.create.
	if !containsType(envsB, "topic.link.add") {
		t.Fatalf("J1 adversarial: B origin has no topic.link.add")
	}
	if !containsType(envsA, "topic.create") {
		t.Fatalf("J1 adversarial: A origin has no topic.create")
	}

	dbPath := projection.DBPath(p.dirA) + ".adversarial"
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	proj, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proj.Close()

	// Adversarial: apply B's WHOLE origin FIRST — its cross-origin link cannot
	// resolve A's topic yet.
	for i, env := range envsB {
		if err := proj.Apply(env, recsB[i]); err != nil {
			t.Fatalf("adversarial apply B seq %d (%s): %v", env.OriginSequence, env.EventType, err)
		}
	}
	parked, err := proj.ParkedEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(parked) == 0 {
		t.Fatalf("J1 adversarial: B's cross-origin link did NOT park (expected retryable park)")
	}
	var linkParked *projection.ParkedEvent
	for i := range parked {
		if parked[i].EventType == "topic.link.add" {
			linkParked = &parked[i]
		}
	}
	if linkParked == nil {
		t.Fatalf("J1 adversarial: expected a parked topic.link.add, got %+v", parked)
	}
	if !linkParked.Retryable {
		t.Fatalf("J1 adversarial: parked link is not RETRYABLE: %+v", *linkParked)
	}

	// Now apply A's origin (the topic.create). R49.2: the retry sweep inside
	// Apply heals the parked link the moment its dependency lands.
	for i, env := range envsA {
		if err := proj.Apply(env, recsA[i]); err != nil {
			t.Fatalf("adversarial apply A seq %d (%s): %v", env.OriginSequence, env.EventType, err)
		}
	}

	// A final explicit sweep is idempotent and guarantees convergence.
	if _, err := proj.RetryParked(); err != nil {
		t.Fatalf("retry parked sweep: %v", err)
	}
	parked, err = proj.ParkedEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(parked) != 0 {
		t.Fatalf("J1 adversarial: parked events did not self-heal: %+v", parked)
	}

	// The healed link is live.
	topicID, err := proj.TopicIDByName("shared")
	if err != nil || topicID == "" {
		t.Fatalf("J1 adversarial: topic 'shared' missing after heal (id=%q err=%v)", topicID, err)
	}
	msgs, err := proj.MessagesInTopicIDs([]string{topicID})
	if err != nil {
		t.Fatalf("J1 adversarial: messages in topic: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("J1 adversarial: expected 2 messages under 'shared', got %d", len(msgs))
	}
}

func containsType(envs []*event.Envelope, typ string) bool {
	for _, e := range envs {
		if e.EventType == typ {
			return true
		}
	}
	return false
}
