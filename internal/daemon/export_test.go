package daemon_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// editExportBody rewrites an export file's body, leaving the front-matter
// block byte-identical (the legal operator edit).
func editExportBody(t *testing.T, path, newBody string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	idx := strings.Index(s[4:], "\n---\n")
	if !strings.HasPrefix(s, "---\n") || idx < 0 {
		t.Fatalf("export malformed: %s", s)
	}
	head := s[:4+idx+5]
	if err := os.WriteFile(path, []byte(head+newBody), 0o644); err != nil {
		t.Fatal(err)
	}
}

func publishBody(t *testing.T, d *daemon.Daemon, body string) *daemon.PublishResult {
	t.Helper()
	res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Base == head: an edited export becomes ONE normal revision.
func TestExportIngestNormalRevision(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	pub := publishBody(t, d, "first line\nsecond line\n")

	path, err := d.Export(pub.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	editExportBody(t, path, "first line\nsecond line edited\n")

	res, err := d.IngestExport(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "revised" || len(res.NewRevisionIDs) != 1 {
		t.Fatalf("outcome: %+v", res)
	}
	info, _ := d.Projection().MessageInfo(pub.MessageID)
	if info.HeadRevisionID != res.HeadRevisionID {
		t.Fatal("head not moved")
	}
	rev, err := d.Projection().RevisionInfo(res.HeadRevisionID)
	if err != nil || rev.MessageID != pub.MessageID {
		t.Fatalf("revision row: %+v %v", rev, err)
	}
	hits, _ := d.Projection().SearchLexical("edited", 10, false)
	if len(hits) != 1 {
		t.Fatal("revised body not indexed")
	}
	// CRLF normalization on ingest
	editExportBodyCRLF := "crlf line\r\nanother\r\n"
	path2, _ := d.Export(pub.MessageID)
	editExportBody(t, path2, editExportBodyCRLF)
	res2, err := d.IngestExport(path2)
	if err != nil {
		t.Fatal(err)
	}
	rev2, err := d.Projection().RevisionInfo(res2.HeadRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := d.Store().Get(rev2.BodyHash)
	if strings.Contains(string(body), "\r") {
		t.Fatal("CRLF not normalized")
	}
}

// Stale edit vs 1 and vs N intervening revisions, non-overlapping → ONE
// event with TWO revisions (operator branch + machine-merged head).
func TestExportIngestCleanMergeRaces(t *testing.T) {
	for _, intervening := range []int{1, 3} {
		dir := initCairn(t)
		d := startDaemon(t, dir)
		pub := publishBody(t, d, "alpha\nbeta\ngamma\n")

		path, err := d.Export(pub.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		baseHead := pub.RevisionID

		// intervening revisions move the head (edit line 1 each time)
		head := baseHead
		for i := 0; i < intervening; i++ {
			line1 := "alpha" + strings.Repeat("!", i+1)
			ires, err := d.IngestExport(path) // no-op guard: file unchanged → unchanged
			_ = ires
			_ = err
			// move head via a direct export+edit cycle on the CURRENT head
			p2, err := d.Export(pub.MessageID)
			if err != nil {
				t.Fatal(err)
			}
			editExportBody(t, p2, line1+"\nbeta\ngamma\n")
			r, err := d.IngestExport(p2)
			if err != nil {
				t.Fatal(err)
			}
			head = r.HeadRevisionID
		}

		// now apply the STALE edit (base = original revision, line 3 edited)
		stale, err := renderStaleExport(t, dir, d, pub.MessageID, baseHead)
		if err != nil {
			t.Fatal(err)
		}
		editExportBody(t, stale, "alpha\nbeta\ngamma EDITED\n")
		res, err := d.IngestExport(stale)
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != "merged" || len(res.NewRevisionIDs) != 2 || res.EventID == "" {
			t.Fatalf("intervening=%d: %+v", intervening, res)
		}

		// merged head body contains BOTH changes
		info, _ := d.Projection().MessageInfo(pub.MessageID)
		body, err := d.Store().Get(info.BodyHash)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "gamma EDITED") || !strings.Contains(string(body), "alpha"+strings.Repeat("!", intervening)) {
			t.Fatalf("merge lost a side: %q", body)
		}
		_ = head
		d.Close()
	}
}

// renderStaleExport re-renders an export pinned to an OLD base revision
// (simulating an export taken before intervening edits).
func renderStaleExport(t *testing.T, dir string, d *daemon.Daemon, messageID, baseRevision string) (string, error) {
	t.Helper()
	rev, err := d.Projection().RevisionInfo(baseRevision)
	if err != nil {
		return "", err
	}
	body, err := d.Store().Get(rev.BodyHash)
	if err != nil {
		return "", err
	}
	content := "---\nmessage_id: " + messageID + "\nrevision_id: " + baseRevision +
		"\nbase_revision_id: " + baseRevision + "\nbody_hash: " + rev.BodyHash +
		"\nexported_at: 2026-07-11T00:00:00.000000Z\n---\n" + string(body)
	path := filepath.Join(dir, "exports", messageID+".stale.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Overlapping stale edit → conflict materialization, then resolve.
func TestConflictAndResolve(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	pub := publishBody(t, d, "shared line\n")

	// head moves (same line edited)
	p1, _ := d.Export(pub.MessageID)
	editExportBody(t, p1, "shared line from head\n")
	if _, err := d.IngestExport(p1); err != nil {
		t.Fatal(err)
	}

	// stale edit touches the same line → conflict
	stale, err := renderStaleExport(t, dir, d, pub.MessageID, pub.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	editExportBody(t, stale, "shared line from operator\n")
	res, err := d.IngestExport(stale)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "conflict" || res.ConflictDir == "" {
		t.Fatalf("expected conflict: %+v", res)
	}
	for _, f := range []string{"BASE.md", "CURRENT.md", "OPERATOR_EDIT.md", "RESOLVE.md", "conflict.json"} {
		if _, err := os.Stat(filepath.Join(res.ConflictDir, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	seed, _ := os.ReadFile(filepath.Join(res.ConflictDir, "RESOLVE.md"))
	if !strings.Contains(string(seed), "<<<<<<<") {
		t.Fatalf("RESOLVE.md not seeded with markers: %s", seed)
	}

	// doctor conflicts sees the debt
	dirs, err := daemon.UnresolvedConflicts(fsx.OS{}, dir)
	if err != nil || len(dirs) != 1 {
		t.Fatalf("conflict debt: %v %v", dirs, err)
	}

	// resolve with markers still present → refused
	if _, err := d.Resolve(pub.MessageID); err == nil || !strings.Contains(err.Error(), "markers") {
		t.Fatalf("marker guard: %v", err)
	}

	// operator resolves
	if err := os.WriteFile(filepath.Join(res.ConflictDir, "RESOLVE.md"),
		[]byte("shared line resolved by operator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rres, err := d.Resolve(pub.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if rres.Outcome != "merged" && rres.Outcome != "revised" {
		t.Fatalf("resolve outcome: %+v", rres)
	}
	if _, err := os.Stat(res.ConflictDir); err == nil {
		t.Fatal("conflict dir not cleaned up")
	}
	info, _ := d.Projection().MessageInfo(pub.MessageID)
	body, _ := d.Store().Get(info.BodyHash)
	if !strings.Contains(string(body), "resolved by operator") {
		t.Fatalf("resolution not applied: %q", body)
	}
	dirs, _ = daemon.UnresolvedConflicts(fsx.OS{}, dir)
	if len(dirs) != 0 {
		t.Fatal("conflict debt not cleared")
	}
}

// Retraction racing export ingest → rejected with a structured error receipt.
func TestRetractionRacesIngest(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	pub := publishBody(t, d, "soon retracted\n")

	path, err := d.Export(pub.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	editExportBody(t, path, "edit racing retraction\n")

	if _, err := d.SimpleEvent("message.retract", "message", pub.MessageID,
		map[string]any{"message_id": pub.MessageID}, daemon.PublishRequest{Actor: "operator"}); err != nil {
		t.Fatal(err)
	}

	_, err = d.IngestExport(path)
	if err == nil || !strings.Contains(err.Error(), "retracted") {
		t.Fatalf("retraction race: %v", err)
	}
	blob, rerr := os.ReadFile(path + ".reject.json")
	if rerr != nil || !strings.Contains(string(blob), "retracted") {
		t.Fatalf("error receipt: %s %v", blob, rerr)
	}
	// no revision was created
	info, _ := d.Projection().MessageInfo(pub.MessageID)
	if info.HeadRevisionID != pub.RevisionID {
		t.Fatal("retracted message gained a revision")
	}
}

// Front-matter is read-only: any mutation is rejected with an error receipt.
func TestFrontMatterMutationRejected(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	pub := publishBody(t, d, "protected front matter\n")
	other := publishBody(t, d, "another message\n")

	mutations := []struct {
		name string
		edit func(content string) string
	}{
		{"body_hash", func(c string) string {
			return strings.Replace(c, "body_hash: ", "body_hash: 00", 1)
		}},
		{"message_id swap", func(c string) string {
			return strings.ReplaceAll(c, pub.MessageID, other.MessageID)
		}},
		{"unknown field", func(c string) string {
			return strings.Replace(c, "exported_at:", "declared_priority: 3\nexported_at:", 1)
		}},
		{"front-matter deleted", func(c string) string {
			i := strings.Index(c[4:], "\n---\n")
			return c[4+i+5:]
		}},
	}
	for _, m := range mutations {
		path, err := d.Export(pub.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		content, _ := os.ReadFile(path)
		if err := os.WriteFile(path, []byte(m.edit(string(content))), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := d.IngestExport(path); err == nil {
			t.Fatalf("%s: mutation accepted", m.name)
		}
		if _, err := os.Stat(path + ".reject.json"); err != nil {
			t.Fatalf("%s: no error receipt", m.name)
		}
		os.Remove(path + ".reject.json")
	}
}

// Unchanged body → no event.
func TestUnchangedExportIsNoOp(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	pub := publishBody(t, d, "steady state\n")
	path, _ := d.Export(pub.MessageID)
	res, err := d.IngestExport(path)
	if err != nil || res.Outcome != "unchanged" || res.EventID != "" {
		t.Fatalf("%+v %v", res, err)
	}
}

// M5 acceptance: crash during merge leaves NO half-graph — after a fault at
// every mutating fs op of a merge ingest, the message has either its
// pre-ingest revisions or the complete 2-revision merge; doctor stays clean.
func TestMergeCrashLeavesNoHalfGraph(t *testing.T) {
	// count ops of a clean merge ingest
	setup := func(t *testing.T, fsys fsx.FS) (string, *daemon.Daemon, string, string) {
		dir := initCairn(t)
		d, err := daemon.Start(daemon.Options{Dir: dir, FS: fsys})
		if err != nil {
			t.Fatal(err)
		}
		pub := publishBody(t, d, "one\ntwo\nthree\n")
		// move head
		p, _ := d.Export(pub.MessageID)
		editExportBody(t, p, "one HEAD\ntwo\nthree\n")
		if _, err := d.IngestExport(p); err != nil {
			t.Fatal(err)
		}
		stale, err := renderStaleExport(t, dir, d, pub.MessageID, pub.RevisionID)
		if err != nil {
			t.Fatal(err)
		}
		editExportBody(t, stale, "one\ntwo\nthree STALE-EDIT\n")
		return dir, d, pub.MessageID, stale
	}

	probeFS := &faultFS{inner: fsx.OS{}}
	_, dProbe, _, staleProbe := setup(t, probeFS)
	before := probeFS.count
	if res, err := dProbe.IngestExport(staleProbe); err != nil || res.Outcome != "merged" {
		t.Fatalf("probe merge: %+v %v", res, err)
	}
	totalOps := probeFS.count - before
	dProbe.Close()
	if totalOps < 4 {
		t.Fatalf("suspiciously few merge ops: %d", totalOps)
	}

	for k := 1; k <= totalOps; k++ {
		ff := &faultFS{inner: fsx.OS{}}
		dir, d, msgID, stale := setup(t, ff)
		ff.mu.Lock()
		ff.failAfter = ff.count + k
		ff.mu.Unlock()

		res, err := d.IngestExport(stale)
		d.Close()

		// restart cleanly and inspect the revision graph
		d2, err2 := daemon.Start(daemon.Options{Dir: dir})
		if err2 != nil {
			t.Fatalf("fail@%d: restart: %v", k, err2)
		}
		revCount := revisionCount(t, dir)
		if err == nil && res != nil && res.Outcome == "merged" {
			if revCount != 4 { // publish + head-move + 2 merge revisions
				t.Fatalf("fail@%d: acked merge but %d revisions", k, revCount)
			}
		} else if revCount != 2 && revCount != 4 {
			t.Fatalf("fail@%d: HALF GRAPH: %d revisions", k, revCount)
		}
		doc, derr := cairnlog.Doctor(fsx.OS{}, dir, identity.NewChainVerifier().Verify)
		if derr != nil || !doc.Clean() {
			t.Fatalf("fail@%d: doctor: %v", k, derr)
		}
		_ = msgID
		d2.Close()
	}
}

// revisionCount counts revision objects across publish/revise events in the
// log (independent of the projection).
func revisionCount(t *testing.T, dir string) int {
	t.Helper()
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	_, err = cairnlog.Walk(fsx.OS{}, dir,
		cairnlog.Origin{DeviceID: loaded.Device.DeviceID, Generation: loaded.Device.OriginGeneration},
		identity.NewChainVerifier().Verify,
		func(env *event.Envelope, _ []byte) error {
			switch env.EventType {
			case "message.publish", "message.reply":
				n++
			case "message.revise_body":
				var pl struct {
					Revisions []json.RawMessage `json:"revisions"`
				}
				if json.Unmarshal(env.Payload, &pl) == nil {
					n += len(pl.Revisions)
				}
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
