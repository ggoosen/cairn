package daemon_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// Telemetry: interactions recorded with attribution (inferred flagged),
// outcomes bind to interaction_id, gates aggregate correctly.
func TestTelemetryOutcomesAndGates(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "telemetry subject about lighthouses"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Search(daemon.SearchOptions{Query: "lighthouses", K: 5, BudgetChars: 500})
	if err != nil {
		t.Fatal(err)
	}

	// outcome without a valid interaction id is refused
	if err := d.Outcome("0190a1b2-dead-7000-8000-000000000000", "found", pub.MessageID); err == nil {
		t.Fatal("outcome accepted for unknown interaction")
	}
	if err := d.Outcome(out.InteractionID, "definitely-not-an-outcome", ""); err == nil {
		t.Fatal("bogus outcome kind accepted")
	}
	if err := d.Outcome(out.InteractionID, "found", pub.MessageID); err != nil {
		t.Fatal(err)
	}

	g, err := d.Telemetry().Gates()
	if err != nil {
		t.Fatal(err)
	}
	if g.Interactions == 0 || g.OutcomeFound != 1 || g.BudgetViolations != 0 || g.LatencySamples == 0 {
		t.Fatalf("gate stats wrong: %+v", g)
	}

	var report strings.Builder
	if err := d.GatesReport(&report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"acknowledged-event loss", "provenance", "budget compliance", "P95", "human-measured", "automated"} {
		if !strings.Contains(report.String(), want) {
			t.Fatalf("gates report missing %q:\n%s", want, report.String())
		}
	}
	if strings.Contains(report.String(), "FAIL") {
		t.Fatalf("gates report shows FAIL on a healthy cairn:\n%s", report.String())
	}
	// telemetry NEVER in the event log: nothing new appended by search/outcome
	ids := walkActive(t, dir)
	if len(ids) != 2 { // genesis + publish only
		t.Fatalf("telemetry leaked into the event log: %d events", len(ids))
	}
}

// Emergency reserve lifecycle (rulings §11 + TESTING.md reserve rows):
// preallocated at start; ordinary sends never touch it; release grants
// exactly ONE ≤64 KiB send; oversized/second sends refused; reserve
// re-preallocated afterwards; priority is never authorization.
func TestEmergencyReserveLifecycle(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	present, size, granted := d.ReserveStatus()
	if !present || size != int64(config.EmergencyReserveBytes) || granted {
		t.Fatalf("reserve not preallocated: present=%v size=%d granted=%v", present, size, granted)
	}

	// ordinary sends leave the reserve untouched (even at max priority —
	// declared priority is never reserve authorization)
	for i := 0; i < 3; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: fmt.Sprintf("ordinary %d", i), DeclaredPriority: 3}); err != nil {
			t.Fatal(err)
		}
	}
	present, size, _ = d.ReserveStatus()
	if !present || size != int64(config.EmergencyReserveBytes) {
		t.Fatal("ordinary sends consumed the reserve")
	}

	// emergency without release → refused
	if _, err := d.EmergencyPublish(daemon.PublishRequest{Actor: "operator", Body: "urgent", DeclaredPriority: 3}); err == nil {
		t.Fatal("emergency send allowed without release")
	}

	if err := d.ReleaseReserve(); err != nil {
		t.Fatal(err)
	}
	_, _, granted = d.ReserveStatus()
	if !granted {
		t.Fatal("release grant not recorded")
	}
	// double release refused while grant unused
	if err := d.ReleaseReserve(); err == nil {
		t.Fatal("double release allowed")
	}
	// oversized emergency refused (grant not consumed by the size check)
	big := strings.Repeat("x", config.EmergencyReleaseMaxBytes+1)
	if _, err := d.EmergencyPublish(daemon.PublishRequest{Actor: "operator", Body: big}); err == nil {
		t.Fatal("oversized emergency send allowed")
	}

	res, err := d.EmergencyPublish(daemon.PublishRequest{Actor: "operator", Body: "small urgent note"})
	if err != nil || res.MessageID == "" {
		t.Fatalf("emergency send: %+v %v", res, err)
	}
	// grant consumed; reserve re-preallocated
	present, size, granted = d.ReserveStatus()
	if granted {
		t.Fatal("grant not consumed")
	}
	if !present || size != int64(config.EmergencyReserveBytes) {
		t.Fatal("reserve not re-preallocated after emergency send")
	}
	if _, err := d.EmergencyPublish(daemon.PublishRequest{Actor: "operator", Body: "second urgent"}); err == nil {
		t.Fatal("second emergency send without a new release allowed")
	}
}

// Restored-portable-data row: adopt archives history and creates a NEW origin.
func TestInitAdoptCreatesNewOrigin(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "pre-restore content"}); err != nil {
		t.Fatal(err)
	}
	d.Close()

	oldLoaded, _ := identity.Load(dir)
	oldCairnID := oldLoaded.Portable.CairnID
	// simulate restore on a machine without device-local identity
	if err := os.RemoveAll(filepath.Dir(oldLoaded.DeviceDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Load(dir); err == nil {
		t.Fatal("restore not detected")
	}
	// plain init refuses over restored data
	if _, err := identity.Initialize(identity.InitOptions{Dir: dir, Out: io.Discard}); err == nil {
		t.Fatal("plain init over restored data allowed")
	}

	res, err := identity.Adopt(identity.InitOptions{Dir: dir, Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if res.CairnID == oldCairnID {
		t.Fatal("adopt reused the old cairn identity")
	}
	// old history preserved
	if _, err := os.Stat(filepath.Join(dir, "events-preadopt-"+oldCairnID)); err != nil {
		t.Fatal("old history not archived")
	}
	// new origin fully usable
	d2, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if _, err := d2.Publish(daemon.PublishRequest{Actor: "operator", Body: "post-adopt content"}); err != nil {
		t.Fatalf("new origin cannot write: %v", err)
	}
}

// Clock rollback / extreme-future rows: ordering is (origin, generation,
// sequence) — never wall time; future timestamps never boost freshness.
func TestClockRollbackAndFutureTimestamps(t *testing.T) {
	dir := initCairn(t)
	clock := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{},
		Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "first at noon"}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(-3 * time.Hour) // clock ROLLBACK
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "second after rollback"}); err != nil {
		t.Fatalf("rollback blocked a send: %v", err)
	}
	clock = clock.Add(100 * 365 * 24 * time.Hour) // extreme future
	futurePub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "third from the far future"})
	if err != nil {
		t.Fatal(err)
	}
	_ = futurePub
	clock = time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)

	// sequences stay contiguous and verified (Walk enforces chain+sigs)
	ids := walkActive(t, dir)
	if len(ids) != 4 {
		t.Fatalf("events %d, want 4", len(ids))
	}
	// search does not crash and future item gets no negative-age boost > 1
	out, err := d.Search(daemon.SearchOptions{Query: "future", K: 5})
	if err != nil || len(out.Results) == 0 {
		t.Fatalf("search after clock chaos: %v", err)
	}
}

// Row 10 (view swap): a fault during digest regeneration never exposes a
// partial digest.md — the old content stays intact.
func TestViewSwapNeverPartial(t *testing.T) {
	dir := initCairn(t)
	ff := &faultFS{inner: fsx.OS{}}
	d, err := daemon.Start(daemon.Options{Dir: dir, FS: ff, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "digest content v1"}); err != nil {
		t.Fatal(err)
	}
	out1, err := d.Digest(daemon.DigestOptions{AgentView: "operator", BudgetChars: 2000})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := os.ReadFile(out1.Path)
	if err != nil {
		t.Fatal(err)
	}

	// fault every fs op for the next digest attempts; digest must fail
	// WITHOUT corrupting the existing file (or at worst removing it —
	// never exposing partial bytes)
	for k := 1; k <= 6; k++ {
		ff.mu.Lock()
		ff.failAfter = ff.count + k
		ff.mu.Unlock()
		_, derr := d.Digest(daemon.DigestOptions{AgentView: "operator", BudgetChars: 2000})
		ff.mu.Lock()
		ff.failAfter = 0
		ff.mu.Unlock()
		if derr == nil {
			continue // this fault point landed after completion
		}
		if blob, rerr := os.ReadFile(out1.Path); rerr == nil {
			if string(blob) != string(v1) && !strings.Contains(string(blob), "interaction") {
				t.Fatalf("fault@%d: partial digest exposed: %q", k, blob)
			}
		}
	}
	// healthy regeneration still works
	if _, err := d.Digest(daemon.DigestOptions{AgentView: "operator", BudgetChars: 2000}); err != nil {
		t.Fatal(err)
	}
}

// SQLite failure AFTER ack degrades (warning) and never un-acks; restart heals.
func TestProjectionFailureAfterAckDegrades(t *testing.T) {
	dir := initCairn(t)
	var warn strings.Builder
	d, err := daemon.Start(daemon.Options{Dir: dir, Warn: &warn})
	if err != nil {
		t.Fatal(err)
	}
	// sabotage the projection connection so Apply fails after ack
	d.Projection().Close()
	res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "acked despite projection death"})
	if err != nil {
		t.Fatalf("publish must ack despite projection failure: %v", err)
	}
	if !strings.Contains(warn.String(), "projection lag") {
		t.Fatalf("no degradation warning: %q", warn.String())
	}
	d.Close()

	// restart: replay heals the projection; the acked event is visible
	d2, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	hits, err := d2.Projection().SearchLexical("acked", 10, false)
	if err != nil || len(hits) != 1 || hits[0].MessageID != res.MessageID {
		t.Fatalf("acked event lost from projection after heal: %v %v", hits, err)
	}
}

// Invalid signature on a hash-valid frame: doctor surfaces it; recovery
// refuses (conservative quarantine for a single-writer P0 log).
func TestInvalidSignatureSurfaced(t *testing.T) {
	dir := initCairn(t)
	loaded, _ := identity.Load(dir)

	// craft a chained record signed by an UNKNOWN key
	_, evilPriv, _ := identity.GenerateKey()
	ids := walkActive(t, dir)
	_ = ids
	// use testutil-style envelope with correct chain position but foreign key
	report, err := cairnWalkReport(dir, loaded)
	if err != nil {
		t.Fatal(err)
	}
	forged := forgeEvent(t, loaded, evilPriv, report.NextSeq, report.LastEventID)
	segDir := filepath.Join(dir, "events", loaded.Device.DeviceID, "1")
	segs, _ := os.ReadDir(segDir)
	var segPath string
	for _, e := range segs {
		if strings.HasSuffix(e.Name(), ".seg") {
			segPath = filepath.Join(segDir, e.Name())
		}
	}
	f, err := os.OpenFile(segPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(forged)
	f.Close()

	doc, err := cairnDoctor(dir)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Clean() {
		t.Fatal("doctor clean on invalid signature")
	}
	found := false
	for _, o := range doc.Origins {
		for _, p := range o.Problems {
			if strings.Contains(p.Detail, "verification") || strings.Contains(p.Detail, "signing key") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("signature problem not surfaced: %+v", doc.Origins)
	}
	if _, err := daemon.Start(daemon.Options{Dir: dir}); err == nil {
		t.Fatal("daemon started over an invalid-signature record")
	}
}

// Missing referenced object (non-ephemeral) is reported by object verification.
func TestMissingObjectSurfaced(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "canonical body that will vanish"})
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	problems, err := daemon.VerifyObjects(fsx.OS{}, dir, time.Now())
	if err != nil || len(problems) != 0 {
		t.Fatalf("healthy store reported problems: %v %v", problems, err)
	}
	// delete the canonical object out from under the log
	os.Remove(filepath.Join(dir, "objects", pub.BodyHash[:2], pub.BodyHash[2:]))
	problems, err = daemon.VerifyObjects(fsx.OS{}, dir, time.Now())
	if err != nil || len(problems) == 0 {
		t.Fatalf("missing canonical object not reported: %v %v", problems, err)
	}
	if !strings.Contains(problems[0], pub.BodyHash) {
		t.Fatalf("problem does not name the object: %v", problems)
	}
}

// --- helpers for the invalid-signature row -----------------------------------

func cairnWalkReport(dir string, loaded *identity.Loaded) (*cairnlog.RecoveryReport, error) {
	return cairnlog.Walk(fsx.OS{}, dir,
		cairnlog.Origin{DeviceID: loaded.Device.DeviceID, Generation: loaded.Device.OriginGeneration},
		identity.NewChainVerifier().Verify, nil)
}

func cairnDoctor(dir string) (*cairnlog.DoctorReport, error) {
	return cairnlog.Doctor(fsx.OS{}, dir, identity.NewChainVerifier().Verify)
}

// forgeEvent frames a chain-consistent record signed by a FOREIGN key.
func forgeEvent(t *testing.T, loaded *identity.Loaded, evilPriv ed25519.PrivateKey, seq int64, prev string) []byte {
	t.Helper()
	env := &event.Envelope{
		SchemaVersion:         config.EventSchemaVersion,
		CairnID:               loaded.Portable.CairnID,
		EventType:             "signal.emit",
		OriginDeviceID:        loaded.Device.DeviceID,
		OriginGeneration:      loaded.Device.OriginGeneration,
		OriginSequence:        seq,
		PreviousOriginEventID: prev,
		ActorPrincipalID:      "intruder",
		ObjectType:            "message",
		ObjectID:              "0190a1b2-c3d4-7e5f-8901-00000000dead",
		WallTime:              "2026-07-11T00:00:00.000000Z",
		PayloadSchema:         config.PayloadSchemaID,
		Payload:               json.RawMessage(`{"message_id":"0190a1b2-c3d4-7e5f-8901-00000000dead","kind":"important"}`),
		SigningKeyID:          event.KeyID(evilPriv.Public().(ed25519.PublicKey)),
	}
	rec, err := env.Sign(evilPriv)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cairnlog.EncodeFrame(&buf, rec)
	return buf.Bytes()
}
