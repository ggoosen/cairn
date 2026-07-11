package ingest_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/ingest"
)

// liveCairn boots an initialized cairn with a served daemon and returns an
// IPC caller (everything goes through the daemon, as in production).
func liveCairn(t *testing.T) (ingest.Caller, *daemon.Daemon) {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	dir := filepath.Join(t.TempDir(), "cairn")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	t.Cleanup(func() { cancel(); d.Close() })

	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(loaded.DeviceDir, "daemon.sock.path")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func(req daemon.Request) (*daemon.Response, error) {
		return daemon.Call(loaded.DeviceDir, req)
	}, d
}

// writeWiki creates a small llm-wiki style tree.
func writeWiki(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "team-wiki")
	files := map[string]string{
		"README.md":             "# Team Wiki\nStart at [[Deploy Runbook]] and [[Auth Notes]].\n",
		"ops/deploy-runbook.md": "# Deploy Runbook\nCanary first. See also [[Auth Notes]] and [[Missing Page]].\n",
		"eng/auth-notes.md":     "# Auth Notes\nToken rotation details live here.\n",
		"eng/deep/scaling.md":   "# Scaling\nHorizontal only. Related: [[Deploy Runbook]].\n",
		"assets/ignore.txt":     "not markdown\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func newID() string {
	u, _ := uuid.NewV7()
	return u.String()
}

func TestIngestScanApplyEndToEnd(t *testing.T) {
	call, d := liveCairn(t)
	root := writeWiki(t)
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)

	// --- scan: deterministic manifest ---------------------------------------
	m, err := ingest.Scan(call, root, "team-wiki", "", newID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 4 { // .txt excluded
		t.Fatalf("entries: %d", len(m.Entries))
	}
	byPath := map[string]ingest.Entry{}
	for _, e := range m.Entries {
		if e.Action != ingest.ActionPublish || e.MessageID == "" {
			t.Fatalf("fresh scan should publish everything: %+v", e)
		}
		byPath[e.Path] = e
	}
	// wiki links resolved to pre-assigned message ids
	rb := byPath["README.md"]
	if len(rb.RelatesTo) != 2 {
		t.Fatalf("README relates_to: %+v", rb)
	}
	dr := byPath["ops/deploy-runbook.md"]
	if len(dr.RelatesTo) != 1 || dr.RelatesTo[0] != byPath["eng/auth-notes.md"].MessageID {
		t.Fatalf("runbook relates_to: %+v", dr)
	}
	if len(dr.Unresolved) != 1 || dr.Unresolved[0] != "Missing Page" {
		t.Fatalf("unresolved: %+v", dr.Unresolved)
	}
	// topics from directory structure
	if want := []string{"team-wiki", "team-wiki/eng", "team-wiki/eng/deep"}; strings.Join(byPath["eng/deep/scaling.md"].Topics, ",") != strings.Join(want, ",") {
		t.Fatalf("topics: %v", byPath["eng/deep/scaling.md"].Topics)
	}

	// --- apply: provenance + topics + search ---------------------------------
	rep, err := ingest.Apply(call, m, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Published != 4 || rep.Revised != 0 || rep.Skipped != 0 {
		t.Fatalf("report: %+v", rep)
	}
	// source_refs projected
	msgID, err := d.Projection().SourceRefMessage("team-wiki/ops/deploy-runbook.md")
	if err != nil || msgID != dr.MessageID {
		t.Fatalf("source_ref projection: %q %v", msgID, err)
	}
	// body searchable
	hits, _ := d.Projection().SearchLexical("canary", 10, false)
	if len(hits) != 1 || hits[0].MessageID != dr.MessageID {
		t.Fatalf("imported body not searchable: %v", hits)
	}
	// topic links live
	links, _ := d.Projection().VisibleLinks(dr.MessageID)
	if len(links) != 2 { // team-wiki + team-wiki/ops
		t.Fatalf("topic links: %v", links)
	}
	// --- idempotency: rescan + apply with no changes → all skips, no events --
	m2, err := ingest.Scan(call, root, "team-wiki", "", newID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m2.Entries {
		if e.Action != ingest.ActionSkip {
			t.Fatalf("unchanged file not skipped: %+v", e)
		}
		if e.MessageID != byPath[e.Path].MessageID {
			t.Fatalf("message id not stable across rescans: %+v", e)
		}
	}
	rep2, err := ingest.Apply(call, m2, now.Add(time.Hour))
	if err != nil || rep2.Published != 0 || rep2.Revised != 0 || rep2.Skipped != 4 {
		t.Fatalf("idempotent apply: %+v %v", rep2, err)
	}

	// --- update: edit ONE file → exactly one revise --------------------------
	if err := os.WriteFile(filepath.Join(root, "eng/auth-notes.md"),
		[]byte("# Auth Notes\nToken rotation details live here.\nNEW: refresh nonces doubled.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m3, err := ingest.Scan(call, root, "team-wiki", "", newID, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	actions := map[ingest.Action]int{}
	for _, e := range m3.Entries {
		actions[e.Action]++
	}
	if actions[ingest.ActionRevise] != 1 || actions[ingest.ActionSkip] != 3 {
		t.Fatalf("update classification: %v", actions)
	}
	rep3, err := ingest.Apply(call, m3, now.Add(2*time.Hour))
	if err != nil || rep3.Revised != 1 || rep3.Published != 0 {
		t.Fatalf("update apply: %+v %v", rep3, err)
	}
	hits, _ = d.Projection().SearchLexical("nonces doubled", 10, false)
	authID, _ := d.Projection().SourceRefMessage("team-wiki/eng/auth-notes.md")
	if len(hits) != 1 || hits[0].MessageID != authID {
		t.Fatalf("revised body not the head: %v", hits)
	}

	// --- drift guard: source changes between scan and apply → refused --------
	m4, _ := ingest.Scan(call, root, "team-wiki", "", newID, now.Add(3*time.Hour))
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("tampered after scan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Apply(call, m4, now.Add(3*time.Hour)); err == nil || !strings.Contains(err.Error(), "changed since the scan") {
		t.Fatalf("drift not refused: %v", err)
	}
}

// Ingest respects text-class policy — no operator override from ingest.
func TestIngestCannotForceCanonical(t *testing.T) {
	call, _ := liveCairn(t)
	root := filepath.Join(t.TempDir(), "big-wiki")
	os.MkdirAll(root, 0o755)
	big := "# Big\n" + strings.Repeat("filler line for oversized canonical body\n", 30000) // > 1 MiB
	if err := os.WriteFile(filepath.Join(root, "big.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m, err := ingest.Scan(call, root, "big-wiki", "", newID, now)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ingest.Apply(call, m, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Downgraded != 1 {
		t.Fatalf("oversized canonical import not downgraded: %+v", rep)
	}
}

// Manifest round-trips through disk (the reviewable artifact).
func TestManifestRoundTrip(t *testing.T) {
	call, _ := liveCairn(t)
	root := writeWiki(t)
	m, err := ingest.Scan(call, root, "team-wiki", "", newID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := ingest.WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}
	m2, err := ingest.ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Entries) != len(m.Entries) || m2.Repo != m.Repo || m2.Root != m.Root {
		t.Fatalf("round trip mismatch")
	}
}
