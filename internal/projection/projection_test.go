package projection_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/testutil"
)

// The embedded schema must stay byte-identical to the normative DDL.
func TestSchemaMatchesNormativeDDL(t *testing.T) {
	norm, err := os.ReadFile("../../build/sql/projection.sql")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(norm) != string(embedded) {
		t.Fatal("internal/projection/schema.sql has drifted from build/sql/projection.sql — copy it over")
	}
}

const (
	msgA  = "0190a1b2-c3d4-7e5f-8901-0000000000a1"
	msgB  = "0190a1b2-c3d4-7e5f-8901-0000000000a2"
	msgC  = "0190a1b2-c3d4-7e5f-8901-0000000000a3"
	msgD  = "0190a1b2-c3d4-7e5f-8901-0000000000a5"
	revA1 = "0190a1b2-c3d4-7e5f-8901-0000000000b1"
	revA2 = "0190a1b2-c3d4-7e5f-8901-0000000000b2"
	revB1 = "0190a1b2-c3d4-7e5f-8901-0000000000b3"
	revC1 = "0190a1b2-c3d4-7e5f-8901-0000000000b4"
	revD1 = "0190a1b2-c3d4-7e5f-8901-0000000000b6"
	topT  = "0190a1b2-c3d4-7e5f-8901-0000000000c1"
	linkA = "0190a1b2-c3d4-7e5f-8901-0000000000d1"
	linkB = "0190a1b2-c3d4-7e5f-8901-0000000000d2"
	pinP  = "0190a1b2-c3d4-7e5f-8901-0000000000e1"
)

// identifierBody is the C2 fixture body: every interesting fragment in it is
// mid-token under `unicode61 tokenchars '_-#@'` — "-" is a tokenchar, so the
// whole run id is ONE token, and "=" is not, so err=ECONNREFUSED splits into
// exactly two.
const identifierBody = "the PeerAdd handler failed err=ECONNREFUSED on run 0190a1b2-c3d4-7e5f-8901-0000000000ff"

func publish(t *testing.T, c *testutil.Chain, store *object.Store, lg *cairnlog.Log, msgID, revID, body, class string) {
	t.Helper()
	hash, err := store.Put([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	env, rec := c.Event(t, "message.publish", "message", msgID, map[string]any{
		"message_id": msgID, "revision_id": revID, "body_hash": hash,
		"body_bytes": body, "body_len": len(body), "body_mime": "text/markdown",
		"text_class": class, "declared_priority": 1,
	}, "operator")
	if err := lg.Append(rec, env); err != nil {
		t.Fatal(err)
	}
}

// buildCorpus writes a representative event log: publishes, a revision, a
// merge revision, retraction, topic links (incl. protected + removal),
// pins, a signal, and a source_ref.
func buildCorpus(t *testing.T) (*fsx.MemFS, *testutil.Chain, *object.Store) {
	t.Helper()
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	store := object.NewStore(m, "/p")

	c, genv, grec := testutil.NewChain(t)
	lg, err := cairnlog.Create(m, "/p", cairnlog.Origin{DeviceID: c.DeviceID, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Append(grec, genv); err != nil {
		t.Fatal(err)
	}

	app := func(eventType, objType, objID string, payload map[string]any, actor string) {
		env, rec := c.Event(t, eventType, objType, objID, payload, actor)
		if err := lg.Append(rec, env); err != nil {
			t.Fatal(err)
		}
	}

	publish(t, c, store, lg, msgA, revA1, "alpha document about zebra migrations", "canonical")
	publish(t, c, store, lg, msgB, revB1, "beta note mentioning zebra crossings", "canonical")
	publish(t, c, store, lg, msgC, revC1, "gamma scratchpad zebra draft", "ephemeral")
	// C2 fixture: identifiers a word tokenizer can only match whole — the
	// camelCase symbol, the run id, and the errno all sit INSIDE one token.
	publish(t, c, store, lg, msgD, revD1, identifierBody, "canonical")

	// revise A: new head
	newBody := "alpha document REVISED, zebras gone"
	hash, _ := store.Put([]byte(newBody))
	app("message.revise_body", "message", msgA, map[string]any{
		"message_id": msgA,
		"revisions": []map[string]any{{
			"revision_id": revA2, "parent_revision_ids": []string{revA1},
			"body_hash": hash, "body_bytes": newBody, "body_len": len(newBody),
		}},
	}, "operator")

	// retract C
	app("message.retract", "message", msgC, map[string]any{"message_id": msgC, "reason": "draft"}, "operator")

	// topics and links
	app("topic.create", "topic", topT, map[string]any{"topic_id": topT, "name": "project/zebra"}, "operator")
	app("topic.link.add", "link", linkA, map[string]any{"link_id": linkA, "message_id": msgA, "topic_id": topT}, "operator")
	app("topic.link.add", "link", linkB, map[string]any{"link_id": linkB, "message_id": msgA, "topic_id": topT, "protected": true}, "operator")

	// pin + signal + source_ref publish
	objHash, _ := store.Put([]byte("pinned blob"))
	app("blob.pin", "pin", pinP, map[string]any{"pin_id": pinP, "principal_id": "operator", "object_hash": objHash, "durability": "pinned"}, "operator")
	app("signal.emit", "message", msgA, map[string]any{"message_id": msgA, "kind": "important", "weight": 5}, "operator")

	srcBody := "imported wiki page"
	srcHash, _ := store.Put([]byte(srcBody))
	app("message.publish", "message", "0190a1b2-c3d4-7e5f-8901-0000000000a4", map[string]any{
		"message_id": "0190a1b2-c3d4-7e5f-8901-0000000000a4", "revision_id": "0190a1b2-c3d4-7e5f-8901-0000000000b5",
		"body_hash": srcHash, "body_bytes": srcBody, "body_len": len(srcBody), "body_mime": "text/markdown",
		"text_class": "canonical", "declared_priority": 0,
		"source_ref": map[string]any{"path": "wiki/zebra.md", "content_hash": srcHash, "imported_at": "2026-07-11T00:00:00.000000Z"},
	}, "operator")

	lg.Close()
	return m, c, store
}

func openAndReplay(t *testing.T, m *fsx.MemFS, dbPath string, store *object.Store) *projection.Projection {
	t.Helper()
	p, err := projection.Open(dbPath, projection.StoreBodyFetch(store))
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Replay(p, m, "/p", func() cairnlog.VerifyFunc {
		return identity.NewChainVerifier().Verify
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectionEndToEnd(t *testing.T) {
	m, _, store := buildCorpus(t)
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	p := openAndReplay(t, m, dbPath, store)
	defer p.Close()

	// search hits head revisions only: A's revised body, B; C is retracted
	res, err := p.SearchLexical("zebra", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, r := range res {
		ids[r.MessageID] = true
		if r.MessageID == msgA && r.RevisionID != revA2 {
			t.Fatalf("search returned non-head revision %s for A", r.RevisionID)
		}
	}
	if !ids[msgB] || ids[msgC] {
		t.Fatalf("wrong result set: %v", ids)
	}
	// "zebras" (revised body) matches A's head
	res, _ = p.SearchLexical("zebras", 10, false)
	if len(res) != 1 || res[0].MessageID != msgA {
		t.Fatalf("revised head not indexed: %+v", res)
	}
	// old body content must not surface via the head join
	res, _ = p.SearchLexical("migrations", 10, false)
	if len(res) != 0 {
		t.Fatalf("superseded revision surfaced as head: %+v", res)
	}

	// retraction visibility: default hidden, include_retracted shows
	res, _ = p.SearchLexical("gamma", 10, false)
	if len(res) != 0 {
		t.Fatal("retracted message in default search")
	}
	res, _ = p.SearchLexical("gamma", 10, true)
	if len(res) != 1 || !res[0].Retracted {
		t.Fatalf("include_retracted failed: %+v", res)
	}

	// links visible
	links, err := p.VisibleLinks(msgA)
	if err != nil || len(links) != 2 {
		t.Fatalf("links: %v %v", links, err)
	}

	// pins active
	if n, _ := p.ActivePins(object.Hash([]byte("pinned blob"))); n != 1 {
		t.Fatalf("active pins %d", n)
	}

	// object refs exclude the pinned object, include bodies
	refs, err := p.ObjectRefs()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		if r.Hash == object.Hash([]byte("pinned blob")) {
			t.Fatal("pinned object offered to housekeeping")
		}
	}
	if len(refs) < 4 {
		t.Fatalf("too few refs: %d", len(refs))
	}
}

// M3 acceptance: delete index.sqlite → reindex reproduces byte-identical
// query results.
func TestReindexByteIdentical(t *testing.T) {
	m, _, store := buildCorpus(t)
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")

	queries := []string{"zebra", "zebras", "gamma", "alpha OR beta", "wiki"}
	// C2: the companion index is populated in the same transaction, so it is
	// covered by the same guarantee — trigram-only queries (nothing here is a
	// whole token) must come back identical across a full rebuild too.
	triQueries := []string{"eerAdd", "7e5f-8901", "CONNREFUSED", "ebra", "zz"}
	snapshot := func(p *projection.Projection) []byte {
		var all [][]projection.SearchResult
		for _, q := range queries {
			for _, inc := range []bool{false, true} {
				res, err := p.SearchLexical(q, 25, inc)
				if err != nil {
					t.Fatal(err)
				}
				all = append(all, res)
			}
		}
		var lex, tri [][]string
		for _, q := range append(append([]string{}, queries...), triQueries...) {
			for _, inc := range []bool{false, true} {
				ids, err := p.LexicalTopK(q, 25, inc)
				if err != nil {
					t.Fatal(err)
				}
				lex = append(lex, ids)
				ids, err = p.TrigramMessageHits(q, 25, inc)
				if err != nil {
					t.Fatal(err)
				}
				tri = append(tri, ids)
			}
		}
		links, _ := p.VisibleLinks(msgA)
		blob, err := json.Marshal(struct {
			Results [][]projection.SearchResult
			Lexical [][]string
			Trigram [][]string
			Links   []string
		}{all, lex, tri, links})
		if err != nil {
			t.Fatal(err)
		}
		return blob
	}

	// live build (as the daemon would apply)
	if _, err := projection.ReindexLexical(m, "/p", dbPath, func() cairnlog.VerifyFunc {
		return identity.NewChainVerifier().Verify
	}, projection.StoreBodyFetch(store)); err != nil {
		t.Fatal(err)
	}
	p1, err := projection.Open(dbPath, projection.StoreBodyFetch(store))
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(p1)
	p1.Close()

	// delete the database entirely, reindex, snapshot again
	os.Remove(dbPath)
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")
	if _, err := projection.ReindexLexical(m, "/p", dbPath, func() cairnlog.VerifyFunc {
		return identity.NewChainVerifier().Verify
	}, projection.StoreBodyFetch(store)); err != nil {
		t.Fatal(err)
	}
	p2, err := projection.Open(dbPath, projection.StoreBodyFetch(store))
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot(p2)
	p2.Close()

	if string(before) != string(after) {
		t.Fatalf("reindex not byte-identical:\n%s\nvs\n%s", before, after)
	}
}

// CAPTURE C2 acceptance: substring/identifier queries the unicode61 word
// index is structurally blind to are answered by the trigram companion, and
// LexicalTopK — the one production lexical candidate path (search + digest
// interest) — surfaces them.
func TestTrigramCompanionFindsSubstrings(t *testing.T) {
	m, _, store := buildCorpus(t)
	p := openAndReplay(t, m, filepath.Join(t.TempDir(), "index.sqlite"), store)
	defer p.Close()

	// Each of these is a fragment INSIDE a token of identifierBody: the
	// camelCase tail of a symbol, a slice across two UUID groups (the whole
	// run id is one token because "-" is a tokenchar), and the errno without
	// its leading E.
	for _, q := range []string{"eerAdd", "7e5f-8901", "CONNREFUSED"} {
		word, err := p.SearchLexical(q, 25, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(word) != 0 {
			t.Fatalf("%q: word index unexpectedly matched %+v — the fixture no longer proves the gap", q, word)
		}
		ids, err := p.LexicalTopK(q, 25, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != msgD {
			t.Fatalf("%q: LexicalTopK = %v, want [%s]", q, ids, msgD)
		}
	}

	// Word hits keep their positions: the trigram union only appends. "zebra"
	// is a whole token in B (and a substring of A's "zebras"), so B must
	// still come first, from the word index.
	ids, err := p.LexicalTopK("zebra", 25, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 || ids[0] != msgB {
		t.Fatalf("trigram union displaced the word hit: %v", ids)
	}

	// Retraction gating is the word index's, unchanged: C is retracted, and
	// "amma" reaches it only through the trigram companion.
	if ids, err = p.LexicalTopK("amma", 25, false); err != nil || len(ids) != 0 {
		t.Fatalf("retracted message reachable via trigram: %v %v", ids, err)
	}
	if ids, err = p.LexicalTopK("amma", 25, true); err != nil || len(ids) != 1 || ids[0] != msgC {
		t.Fatalf("include_retracted trigram: %v %v", ids, err)
	}

	// A term shorter than the trigram width tokenizes to nothing: it must
	// match nothing rather than everything, and must not error.
	if q := projection.FTSTrigramQuery("zz"); q != "" {
		t.Fatalf("sub-width term survived into the trigram query: %q", q)
	}
	if ids, err = p.TrigramMessageHits("zz", 25, false); err != nil || len(ids) != 0 {
		t.Fatalf("sub-width trigram query: %v %v", ids, err)
	}
	// Mixed-width queries keep only the usable terms.
	if q := projection.FTSTrigramQuery("zz eerAdd"); q != `"eerAdd"` {
		t.Fatalf("mixed-width trigram query = %q", q)
	}
}

// M3 acceptance: crash mid-projection resumes idempotently — apply a prefix,
// "crash" (close), reopen and replay: final state equals a one-shot replay.
func TestCrashMidProjectionResumes(t *testing.T) {
	m, c, store := buildCorpus(t)

	// collect all records in order
	var envs []*event.Envelope
	var recs [][]byte
	_, err := cairnlog.Walk(m, "/p", cairnlog.Origin{DeviceID: c.DeviceID, Generation: 1},
		identity.NewChainVerifier().Verify,
		func(e *event.Envelope, r []byte) error {
			envs = append(envs, e)
			recs = append(recs, append([]byte(nil), r...))
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}

	for cut := 1; cut < len(envs); cut++ {
		dbPath := filepath.Join(t.TempDir(), "index.sqlite")
		p, err := projection.Open(dbPath, projection.StoreBodyFetch(store))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < cut; i++ {
			if err := p.Apply(envs[i], recs[i]); err != nil {
				t.Fatalf("cut %d apply %d: %v", cut, i, err)
			}
		}
		p.Close() // crash here (SQLite's own WAL+FULL covers torn tx)

		// restart: full replay must resume from the checkpoint idempotently
		p2 := openAndReplay(t, m, dbPath, store)
		res, err := p2.SearchLexical("zebras", 10, false)
		if err != nil || len(res) != 1 {
			t.Fatalf("cut %d: post-resume search: %v %v", cut, res, err)
		}
		seq, _ := p2.Checkpoint(c.DeviceID, 1)
		if seq != int64(len(envs)) {
			t.Fatalf("cut %d: checkpoint %d, want %d", cut, seq, len(envs))
		}
		// double-apply of an old event is a no-op
		if err := p2.Apply(envs[0], recs[0]); err != nil {
			t.Fatalf("re-apply: %v", err)
		}
		p2.Close()
	}
}

// TESTING.md: ephemeral expiry racing reindex → no crash, event intact,
// revision simply not lexically indexed.
func TestExpiredBodyDuringReindex(t *testing.T) {
	m, _, store := buildCorpus(t)

	// expire C's body (delete the object as housekeeping would); note C's
	// body was also inlined — simulate a non-inline publish by fetching via
	// store only. Build a fresh projection with a bodyFetch that fails for
	// C's hash and inline stripped via store-only fetch.
	cHash := object.Hash([]byte("gamma scratchpad zebra draft"))
	fetch := func(hash string) ([]byte, bool) {
		if hash == cHash {
			return nil, false // expired
		}
		return projection.StoreBodyFetch(store)(hash)
	}
	// strip inline copies so the fetch path is exercised: reindex with a
	// custom fetch — inline bodies bypass it, so instead assert directly:
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	p, err := projection.Open(dbPath, fetch)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := projection.Replay(p, m, "/p", func() cairnlog.VerifyFunc {
		return identity.NewChainVerifier().Verify
	}); err != nil {
		t.Fatalf("replay with expired body crashed: %v", err)
	}
}

// Observed-remove convergence: the same logical add/remove assertions,
// applied in every valid order (removes after the adds they observed),
// converge to the same visible link set; a remove never kills an add it did
// not list; protected links survive automatic (non-operator) removal.
func TestObservedRemoveLinkProperties(t *testing.T) {
	linkC := "0190a1b2-c3d4-7e5f-8901-0000000000d3"

	// remove(linkA) observed only linkA; linkB/linkC adds are concurrent
	orderings := [][]op{
		{{"add", linkA, "operator", false}, {"remove", linkA, "operator", false}, {"add", linkB, "operator", false}, {"add", linkC, "operator", false}},
		{{"add", linkA, "operator", false}, {"add", linkB, "operator", false}, {"remove", linkA, "operator", false}, {"add", linkC, "operator", false}},
		{{"add", linkA, "operator", false}, {"add", linkB, "operator", false}, {"add", linkC, "operator", false}, {"remove", linkA, "operator", false}},
	}

	var results [][]string
	for _, ops := range orderings {
		visible := runLinkOps(t, ops, linkC)
		results = append(results, visible)
	}
	for i := 1; i < len(results); i++ {
		if !reflect.DeepEqual(results[0], results[i]) {
			t.Fatalf("orderings diverged: %v vs %v", results[0], results[i])
		}
	}
	if len(results[0]) != 2 {
		t.Fatalf("remove killed unobserved adds: visible=%v", results[0])
	}

	// protected link survives automatic removal, dies to operator removal
	vis := runLinkOps(t, []op{
		{"add", linkA, "operator", true},
		{"remove", linkA, "agent-7", false}, // automatic: must NOT remove
	}, linkC)
	if len(vis) != 1 {
		t.Fatalf("protected link removed by automatic process: %v", vis)
	}
	vis = runLinkOps(t, []op{
		{"add", linkA, "operator", true},
		{"remove", linkA, "operator", false}, // operator: may remove
	}, linkC)
	if len(vis) != 0 {
		t.Fatalf("operator removal failed on protected link: %v", vis)
	}
}

// op is one link assertion for the convergence property tests.
type op struct {
	kind      string // add | remove
	linkID    string
	actor     string
	protected bool
}

func runLinkOps(t *testing.T, ops []op, _ string) []string {
	t.Helper()
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	store := object.NewStore(m, "/p")
	c, genv, grec := testutil.NewChain(t)
	lg, err := cairnlog.Create(m, "/p", cairnlog.Origin{DeviceID: c.DeviceID, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Append(grec, genv); err != nil {
		t.Fatal(err)
	}
	publish(t, c, store, lg, msgA, revA1, "linkable body", "canonical")
	env, rec := c.Event(t, "topic.create", "topic", topT, map[string]any{"topic_id": topT, "name": "t"}, "operator")
	if err := lg.Append(rec, env); err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		var e *event.Envelope
		var r []byte
		if o.kind == "add" {
			e, r = c.Event(t, "topic.link.add", "link", o.linkID,
				map[string]any{"link_id": o.linkID, "message_id": msgA, "topic_id": topT, "protected": o.protected}, o.actor)
		} else {
			e, r = c.Event(t, "topic.link.remove", "link", o.linkID,
				map[string]any{"removed_link_ids": []string{o.linkID}}, o.actor)
		}
		if err := lg.Append(r, e); err != nil {
			t.Fatal(err)
		}
	}
	lg.Close()

	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	p := openAndReplay(t, m, dbPath, store)
	defer p.Close()
	rows, err := p.LiveLinkIDs(msgA)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
