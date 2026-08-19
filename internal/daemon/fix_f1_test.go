package daemon_test

// F1 regression tests (work order: Codex AUDIT-003 ≡ Claude CAIRN-01).
// Written RED against commit d0aca05: `send --topic <name>` emitted
// topic.link.add with the raw string as topic_id and no topic.create; the
// acked event failed FK projection, poisoning restart and reindex.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/projection"
)

// containsParked reports whether any doctor line mentions a parked event.
func containsParked(lines []string) bool {
	for _, l := range lines {
		if strings.Contains(l, "parked") {
			return true
		}
	}
	return false
}

// F1(a): fresh mesh, send --topic <new name> × 3 distinct topics → all
// visible in search AND digest, restart clean, reindex clean.
func TestF1SendWithNewTopicNames(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	topics := []string{"auditor/alpha", "auditor/beta", "auditor/gamma"}
	var msgIDs []string
	for i, topic := range topics {
		res, err := d.Publish(daemon.PublishRequest{
			Actor:            "operator",
			Body:             "poison probe message " + topic,
			Topics:           []string{topic}, // by NAME, as the CLI --topic flag sends
			AutoCreateTopics: true,            // the operator CLI path
		})
		if err != nil {
			t.Fatalf("send %d --topic %s: %v", i, topic, err)
		}
		// ack covers the WHOLE request: publish + topic.create + link
		if len(res.EventIDs) < 3 {
			t.Fatalf("expected publish+create+link events before ack, got %d: %v", len(res.EventIDs), res.EventIDs)
		}
		msgIDs = append(msgIDs, res.MessageID)
	}

	// visible in search
	hits, err := d.Projection().SearchLexical("poison probe", 10, false)
	if err != nil || len(hits) != 3 {
		t.Fatalf("search: %d hits, %v", len(hits), err)
	}
	// topic links live (per-name digest hard filter finds exactly one)
	for i, topic := range topics {
		ids, err := d.Projection().DigestCandidates([]string{topic})
		if err != nil || len(ids) != 1 || ids[0] != msgIDs[i] {
			t.Fatalf("topic %s candidates: %v %v", topic, ids, err)
		}
	}
	d.Close()

	// restart clean (the auditor's poison point)
	d2, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatalf("restart after --topic sends: %v", err)
	}
	hits, err = d2.Projection().SearchLexical("poison probe", 10, false)
	if err != nil || len(hits) != 3 {
		t.Fatalf("post-restart search: %d hits, %v", len(hits), err)
	}
	d2.Close()

	// reindex clean, zero parked
	dbPath := projection.DBPath(dir)
	os.Remove(dbPath)
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")
	report, err := projection.ReindexLexical(fsx.OS{}, dir, dbPath, nil, nil)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if report.Parked != 0 {
		t.Fatalf("reindex parked %d events on a healthy log", report.Parked)
	}
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	hits, err = p.SearchLexical("poison probe", 10, false)
	if err != nil || len(hits) != 3 {
		t.Fatalf("post-reindex search: %d hits, %v", len(hits), err)
	}
}

// F1(b): link to a bogus topic → pre-ack rejection, no event appended.
func TestF1LinkUnknownTopicRejectedPreAck(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "linkable body"})
	if err != nil {
		t.Fatal(err)
	}
	before := len(walkActive(t, dir))
	_ = before

	_, err = d.Link(pub.MessageID, "no-such-topic", false, "operator")
	if err == nil || !strings.Contains(err.Error(), "topic") {
		t.Fatalf("bogus link not rejected pre-ack: %v", err)
	}
	d.Close()
	after := walkActive(t, dir) // Walk verifies the whole chain
	for _, id := range after {
		_ = id
	}
	if len(after) != 2 { // genesis + publish only — the rejected link never landed
		t.Fatalf("rejected link appended an event: %d events", len(after))
	}
	// and the mesh restarts clean
	d2, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	d2.Close()
}

// F1(c): a synthetic unprojectable event already IN the log is PARKED —
// subsequent events still project, reindex exits clean with a report,
// doctor flags the parked event.
func TestF1UnprojectableEventIsParkedNotFatal(t *testing.T) {
	dir := initCairn(t)
	var warn strings.Builder
	d, err := daemon.Start(daemon.Options{Dir: dir, Warn: &warn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "before the poison"}); err != nil {
		t.Fatal(err)
	}
	// inject the historical poison the way the OLD daemon wrote it: a
	// topic.link.add whose topic_id is a raw string that resolves to nothing
	poisonID, err := d.SimpleEventUnvalidatedForTest("topic.link.add", "link",
		"0190a1b2-c3d4-7e5f-8901-00000000cafe",
		map[string]any{
			"link_id":    "0190a1b2-c3d4-7e5f-8901-00000000cafe",
			"message_id": "0190a1b2-c3d4-7e5f-8901-00000000beef", // nonexistent
			"topic_id":   "not-even-a-uuid",
		})
	if err != nil {
		t.Fatal(err)
	}
	// the event AFTER the poison must still project (stream never stalls)
	after, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "after the poison"})
	if err != nil {
		t.Fatal(err)
	}
	// D11: matching is disjunctive, so "before the poison" is a legitimate
	// (worse) hit for this query too — the assertion is that the event after the
	// poison PROJECTED and is the best match, never that nothing else matched.
	hits, err := d.Projection().SearchLexical("after the poison", 10, false)
	if err != nil || len(hits) == 0 || hits[0].MessageID != after.MessageID {
		t.Fatalf("event after poison not projected: %v %v", hits, err)
	}
	parked, err := d.Projection().ParkedEvents()
	if err != nil || len(parked) != 1 || parked[0].EventID != poisonID {
		t.Fatalf("poison not parked: %v %v", parked, err)
	}
	// FIX-F8.3: the daemon logged LOUDLY at park time — event id, type,
	// origin/seq, error, and the doctor pointer
	for _, want := range []string{"PARKED EVENT", poisonID, "topic.link.add", "cairn doctor"} {
		if !strings.Contains(warn.String(), want) {
			t.Fatalf("park-time log missing %q:\n%s", want, warn.String())
		}
	}
	d.Close()

	// restart: parked again, stream complete
	d2, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatalf("restart over parked event: %v", err)
	}
	hits, _ = d2.Projection().SearchLexical("after the poison", 10, false)
	if len(hits) == 0 || hits[0].MessageID != after.MessageID {
		t.Fatal("post-restart projection incomplete")
	}
	d2.Close()

	// reindex: exit clean (nil error) WITH a parked report
	dbPath := projection.DBPath(dir)
	os.Remove(dbPath)
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")
	report, err := projection.ReindexLexical(fsx.OS{}, dir, dbPath, nil, nil)
	if err != nil {
		t.Fatalf("reindex aborted on parked event: %v", err)
	}
	if report.Parked != 1 {
		t.Fatalf("reindex parked report: %d", report.Parked)
	}

	// R49 amends F1: a FK-failing topic.link.add is now a RETRYABLE park (a
	// missing intra-mesh reference the projector cannot distinguish from a
	// not-yet-arrived dependency). Within the grace window it is INFORMATIONAL
	// (the dependency may still arrive); once the window lapses it is a FAILURE
	// (the dependency never arrived). This poison's referents never exist, so it
	// stays parked across the grace boundary.
	within, infos, err := daemon.DoctorProjection(dir, dbPath, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range within {
		if strings.Contains(p, "parked") {
			t.Fatalf("within grace, retryable park should be informational, not a problem: %v", within)
		}
	}
	if !containsParked(infos) {
		t.Fatalf("doctor did not surface the retryable parked event as info: %v", infos)
	}
	overdue, _, err := daemon.DoctorProjection(dir, dbPath, time.Now().Add(config.ParkedRetryableGrace+time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !containsParked(overdue) {
		t.Fatalf("doctor did not flag the parked event past the grace window: %v", overdue)
	}

	// the log itself is untouched and fully valid (immutability)
	loaded, _ := identity.Load(dir)
	if _, err := cairnlog.Walk(fsx.OS{}, dir,
		cairnlog.Origin{DeviceID: loaded.Device.DeviceID, Generation: loaded.Device.OriginGeneration},
		identity.NewChainVerifier().Verify, nil); err != nil {
		t.Fatalf("log damaged: %v", err)
	}
}
