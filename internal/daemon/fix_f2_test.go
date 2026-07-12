package daemon_test

// F2 regression tests (Claude CAIRN-07; root cause of Codex AUDIT-005).
// Written RED against d0aca05: the new origin's device.add cert lives in
// the OLD origin's log; per-origin replay verification therefore fails
// ("record before genesis") and reindex exits 1. identity show failed
// post-migrate for the same root cause (per-origin genesis lookup).

import (
	"encoding/json"
	"io"
	"os"
	"sort"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/projection"
)

// F2(b) lives in fix_f2b_test.go (added with the MeshTrust API in the fix).

func publishOne(t *testing.T, dir, body string) string {
	t.Helper()
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return res.MessageID
}

func reindexTo(t *testing.T, dir, dbPath string) *projection.ReindexReport {
	t.Helper()
	report, err := projection.ReindexLexical(fsx.OS{}, dir, dbPath, nil, nil)
	if err != nil {
		t.Fatalf("RED (auditor scenario): reindex on migrated mesh failed: %v", err)
	}
	return report
}

func searchSnapshot(t *testing.T, dbPath, query string) string {
	t.Helper()
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	hits, err := p.SearchLexical(query, 25, false)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(hits)
	return string(blob)
}

// F2(a): init → send → migrate → send → delete index.sqlite → reindex →
// ALL messages retrievable, byte-identical results across reindexes.
func TestF2MigratedMeshReindexes(t *testing.T) {
	dir := initCairn(t)
	publishOne(t, dir, "pre-migration message one about anchors")
	publishOne(t, dir, "pre-migration message two about anchors")

	if _, err := identity.Migrate(identity.MigrateOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	publishOne(t, dir, "post-migration message three about anchors")
	publishOne(t, dir, "post-migration message four about anchors")

	// destroy the projection and rebuild purely from the log
	dbPath := projection.DBPath(dir)
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	report := reindexTo(t, dir, dbPath)
	if report.Parked != 0 {
		t.Fatalf("reindex parked %d events on a healthy migrated mesh", report.Parked)
	}
	first := searchSnapshot(t, dbPath, "anchors")

	var hits []projection.SearchResult
	json.Unmarshal([]byte(first), &hits)
	if len(hits) != 4 {
		t.Fatalf("migrated mesh lost messages on reindex: %d/4 retrievable", len(hits))
	}

	// byte-identical across a second full rebuild
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	reindexTo(t, dir, dbPath)
	second := searchSnapshot(t, dbPath, "anchors")
	if first != second {
		t.Fatalf("reindex not byte-identical on migrated mesh:\n%s\nvs\n%s", first, second)
	}
}

// F2(c): migrate twice (A→B→C) and reindex.
func TestF2DoubleMigrateReindexes(t *testing.T) {
	dir := initCairn(t)
	publishOne(t, dir, "epoch alpha message")
	if _, err := identity.Migrate(identity.MigrateOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	publishOne(t, dir, "epoch beta message")
	if _, err := identity.Migrate(identity.MigrateOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	publishOne(t, dir, "epoch gamma message")

	dbPath := projection.DBPath(dir)
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	report := reindexTo(t, dir, dbPath)
	if report.Parked != 0 {
		t.Fatalf("parked %d", report.Parked)
	}
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	hits, err := p.SearchLexical("epoch", 10, false)
	if err != nil || len(hits) != 3 {
		t.Fatalf("A→B→C reindex lost messages: %d/3, %v", len(hits), err)
	}
}

// F2(d): combined F1+F2 — a migrated mesh WITH CLI-created topics
// reindexes clean, and topic links survive the rebuild.
func TestF2MigratedMeshWithTopicsReindexes(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "topical message before migrate",
		Topics: []string{"combined/topic"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatal(err)
	}
	d.Close()

	if _, err := identity.Migrate(identity.MigrateOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}

	d2, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d2.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "topical message after migrate",
		Topics: []string{"combined/topic", "combined/fresh"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatal(err)
	}
	d2.Close()

	dbPath := projection.DBPath(dir)
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	report := reindexTo(t, dir, dbPath)
	if report.Parked != 0 {
		t.Fatalf("parked %d", report.Parked)
	}
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ids, err := p.DigestCandidates([]string{"combined/topic"})
	if err != nil || len(ids) != 2 {
		t.Fatalf("topic filter after migrate+reindex: %v %v", ids, err)
	}
	sort.Strings(ids)
	hits, _ := p.SearchLexical("topical message", 10, false)
	if len(hits) != 2 {
		t.Fatalf("messages lost: %d/2", len(hits))
	}
}
