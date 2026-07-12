package projection_test

// F7 priority: DIRECT tests on projection replay — the seam that carried
// both audit blockers (per-origin verification and projector stalls).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/testutil"
)

// buildTwoOriginLog fabricates the migrate SHAPE directly at the log layer:
// origin A holds genesis + a publish + device.add(B) + revoke(A); origin B
// holds publishes signed by B's key — admitted only via A's log.
func buildTwoOriginLog(t *testing.T) (*fsx.MemFS, string) {
	t.Helper()
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	store := object.NewStore(m, "/p")

	a, genv, grec := testutil.NewChain(t)
	lgA, err := cairnlog.Create(m, "/p", cairnlog.Origin{DeviceID: a.DeviceID, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := lgA.Append(grec, genv); err != nil {
		t.Fatal(err)
	}
	// publish on A
	body := "origin A message"
	hash, _ := store.Put([]byte(body))
	env, rec := a.Event(t, "message.publish", "message", "0190a1b2-c3d4-7e5f-8901-00000000aaa1",
		map[string]any{"message_id": "0190a1b2-c3d4-7e5f-8901-00000000aaa1",
			"revision_id": "0190a1b2-c3d4-7e5f-8901-00000000aaa2", "body_hash": hash,
			"body_bytes": body, "body_len": len(body), "body_mime": "text/markdown",
			"text_class": "canonical", "declared_priority": 0}, "operator")
	if err := lgA.Append(rec, env); err != nil {
		t.Fatal(err)
	}
	// admit device B in A's log, then revoke A (the migrate shape)
	b := a.AdmitNewDevice(t, lgA)
	env, rec = a.Event(t, "device.revoke", "device", a.DeviceID,
		map[string]any{"device_id": a.DeviceID, "reason": "migrated"}, "operator")
	if err := lgA.Append(rec, env); err != nil {
		t.Fatal(err)
	}
	lgA.Close()

	// B's own origin: publishes verified only via A-admitted key
	lgB, err := cairnlog.Create(m, "/p", cairnlog.Origin{DeviceID: b.DeviceID, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	body2 := "origin B message"
	hash2, _ := store.Put([]byte(body2))
	env, rec = b.Event(t, "message.publish", "message", "0190a1b2-c3d4-7e5f-8901-00000000bbb1",
		map[string]any{"message_id": "0190a1b2-c3d4-7e5f-8901-00000000bbb1",
			"revision_id": "0190a1b2-c3d4-7e5f-8901-00000000bbb2", "body_hash": hash2,
			"body_bytes": body2, "body_len": len(body2), "body_mime": "text/markdown",
			"text_class": "canonical", "declared_priority": 0}, "operator")
	if err := lgB.Append(rec, env); err != nil {
		t.Fatal(err)
	}
	lgB.Close()
	return m, "/p"
}

// Two-pass replay projects cross-origin-admitted streams (nil verifier).
func TestReplayTwoPassCrossOrigin(t *testing.T) {
	m, dir := buildTwoOriginLog(t)
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := projection.Replay(p, m, dir, nil); err != nil {
		t.Fatalf("two-pass replay: %v", err)
	}
	hits, err := p.SearchLexical("origin", 10, false)
	if err != nil || len(hits) != 2 {
		t.Fatalf("both origins projected: %d hits, %v", len(hits), err)
	}
	parked, _ := p.ParkedEvents()
	if len(parked) != 0 {
		t.Fatalf("parked on healthy log: %v", parked)
	}
}

// Per-origin fresh verifiers (the OLD behavior) must still FAIL on this
// shape — pinning the regression the two-pass fix addresses.
func TestReplayFreshPerOriginVerifierStillFails(t *testing.T) {
	m, dir := buildTwoOriginLog(t)
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	err = projection.Replay(p, m, dir, func() cairnlog.VerifyFunc {
		return identity.NewChainVerifier().Verify
	})
	if err == nil {
		t.Fatal("fresh per-origin verification unexpectedly verified a cross-origin admission — the pinned regression shape is wrong")
	}
}

// Replay is idempotent and resumes across partial application.
func TestReplayIdempotentAndResumable(t *testing.T) {
	m, dir := buildTwoOriginLog(t)
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Replay(p, m, dir, nil); err != nil {
		t.Fatal(err)
	}
	// run it twice more: no duplicates, no errors
	for i := 0; i < 2; i++ {
		if err := projection.Replay(p, m, dir, nil); err != nil {
			t.Fatalf("re-replay %d: %v", i, err)
		}
	}
	hits, _ := p.SearchLexical("origin", 10, false)
	if len(hits) != 2 {
		t.Fatalf("duplicated or lost on re-replay: %d", len(hits))
	}
	p.Close()

	// resume after reopen
	p2, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	if err := projection.Replay(p2, m, dir, nil); err != nil {
		t.Fatal(err)
	}
}

// A schema-version bump forces ErrSchemaVersion (typed) on old databases.
func TestOpenSchemaVersionTyped(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	// forge an old version marker
	if _, err := p.DBForTest().Exec(`UPDATE meta SET value='1' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	p.Close()
	_, err = projection.Open(dbPath, nil)
	if err == nil {
		t.Fatal("old schema accepted")
	}
	if !projection.IsSchemaVersionErr(err) {
		t.Fatalf("not typed: %v", err)
	}
	os.Remove(dbPath)
}
