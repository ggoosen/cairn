package daemon_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/bench"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/rank"
)

// --- golden corpus (BUILD-PLAN M6 / TESTING.md §4) ---------------------------
// ~200 messages across 4 synthetic projects; ~30 queries with known-relevant
// sets. Deterministic (generated in code). CI numbers use the deterministic
// dev embedder; real-model numbers are recorded during M8 dogfood.

// goldenCorpus loads the SHARED fixtures (internal/bench, drift-pinned to
// testdata/corpus/) so the test and `cairn bench golden` cannot diverge.
func goldenCorpus() ([]bench.Message, []bench.Query) {
	msgs, queries, err := bench.Corpus()
	if err != nil {
		panic(err)
	}
	return msgs, queries
}

// buildCorpusDaemon publishes the corpus and fully enriches it.
func buildCorpusDaemon(t *testing.T) (*daemon.Daemon, map[string]string) {
	t.Helper()
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	msgs, _ := goldenCorpus()
	keyToID := map[string]string{}
	for _, m := range msgs {
		res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: m.Body})
		if err != nil {
			t.Fatal(err)
		}
		keyToID[m.Key] = res.MessageID
	}
	for {
		n, err := d.EnrichOnce(64)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	return d, keyToID
}

// M6 acceptance: Success@5 ≥ 70% on the golden corpus with default
// constants (validates configuration, not the thesis — rulings §10);
// lexical_only top-10 ≥ 60% (TESTING.md §4).
func TestGoldenCorpusSuccessAt5(t *testing.T) {
	d, keyToID := buildCorpusDaemon(t)
	_, queries := goldenCorpus()

	hit := 0
	for _, q := range queries {
		out, err := d.Search(daemon.SearchOptions{Query: q.Query, K: 5})
		if err != nil {
			t.Fatal(err)
		}
		if out.RetrievalMode != "full" {
			t.Fatalf("corpus search not in full mode: %s", out.RetrievalMode)
		}
		if inTop(out.Results, keyToID, q.Relevant) {
			hit++
		}
	}
	success := float64(hit) / float64(len(queries))
	t.Logf("Success@5 = %.2f (%d/%d)", success, hit, len(queries))
	if success < 0.70 {
		t.Fatalf("Success@5 %.2f below the 0.70 gate", success)
	}

	// lexical_only: same queries, embeddings disabled, top-10, ≥ 60%
	d.SetEmbedderForTest(nil)
	hit = 0
	for _, q := range queries {
		out, err := d.Search(daemon.SearchOptions{Query: q.Query, K: 10})
		if err != nil {
			t.Fatal(err)
		}
		if out.RetrievalMode != "lexical_only" {
			t.Fatalf("expected lexical_only, got %s", out.RetrievalMode)
		}
		if inTop(out.Results, keyToID, q.Relevant) {
			hit++
		}
	}
	lex := float64(hit) / float64(len(queries))
	t.Logf("lexical_only top-10 = %.2f (%d/%d)", lex, hit, len(queries))
	if lex < 0.60 {
		t.Fatalf("lexical_only top-10 %.2f below the 0.60 gate", lex)
	}
}

func inTop(results []daemon.RankedResult, keyToID map[string]string, relevant []string) bool {
	want := map[string]bool{}
	for _, k := range relevant {
		want[keyToID[k]] = true
	}
	for _, r := range results {
		if want[r.MessageID] {
			return true
		}
	}
	return false
}

// M6 acceptance: budget compliance — the returned payload NEVER exceeds
// budget_chars (metadata, prefixes, truncation markers included), for search
// and digest, across a sweep of budgets; mandatory overflow is reported.
func TestBudgetComplianceProperty(t *testing.T) {
	d, keyToID := buildCorpusDaemon(t)

	budgets := []int{1, 10, 40, 97, 250, 800, 2500, 10000, 1 << 20}
	for _, b := range budgets {
		out, err := d.Search(daemon.SearchOptions{Query: "authentication token", K: 25, BudgetChars: b})
		if err != nil {
			t.Fatal(err)
		}
		if got := rank.BudgetChars(out.Payload); got > b {
			t.Fatalf("search payload %d chars exceeds budget %d", got, b)
		}
		dout, err := d.Digest(daemon.DigestOptions{AgentView: "operator", BudgetChars: b})
		if err != nil {
			t.Fatal(err)
		}
		if got := rank.BudgetChars(dout.Payload); got > b {
			t.Fatalf("digest payload %d chars exceeds budget %d", got, b)
		}
	}

	// RETR-D4: thread payloads obey the same hard budget
	root, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "budget thread root"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := d.Publish(daemon.PublishRequest{
			Actor: "operator", Body: fmt.Sprintf("budget thread reply %d with some padding text", i),
			ReplyToMessageID: root.MessageID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range budgets {
		// the thread id IS the root message's id
		tout, terr := d.Thread(root.MessageID, b, "")
		if terr != nil {
			t.Fatal(terr)
		}
		if got := rank.BudgetChars(tout.Payload); got > b {
			t.Fatalf("thread payload %d chars exceeds budget %d", got, b)
		}
	}

	// mandatory overflow: many explicit recipients, tiny budget
	for i := 0; i < 6; i++ {
		if _, err := d.Publish(daemon.PublishRequest{
			Actor: "operator", Body: fmt.Sprintf("mandatory delivery number %d for agent-x", i),
			Recipients: []string{"agent-x"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	dout, err := d.Digest(daemon.DigestOptions{AgentView: "agent-x", BudgetChars: 400})
	if err != nil {
		t.Fatal(err)
	}
	if rank.BudgetChars(dout.Payload) > 400 {
		t.Fatalf("digest exceeded budget")
	}
	if dout.OmittedMandatory == 0 {
		t.Fatal("mandatory overflow not reported (omitted_mandatory_count)")
	}
	_ = keyToID
}

// M6 acceptance: why-ranked output matches recomputed scores EXACTLY.
func TestWhyRankedExactArithmetic(t *testing.T) {
	d, _ := buildCorpusDaemon(t)
	out, err := d.Search(daemon.SearchOptions{Query: "canary rollout error budget", K: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) == 0 {
		t.Fatal("no results")
	}
	for _, r := range out.Results {
		text, err := d.WhyRanked(out.InteractionID, r.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		// recompute from the STORED components (via the projection) and
		// compare exactly (Dec is shortest-round-trip)
		_, compJSON, finalRank, err := d.Projection().Explanation(out.InteractionID, r.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			R, F, Score string
			Peff        string `json:"P_eff"`
			Weights     struct{ R, F, P string }
		}
		if err := json.Unmarshal([]byte(compJSON), &rec); err != nil {
			t.Fatal(err)
		}
		// plain IEEE-754 recompute — each product explicitly rounded (no FMA),
		// summed in term order: the R51 external-verifier semantics.
		recomputed := float64(rank.ParseDec(rec.R)*rank.ParseDec(rec.Weights.R)) +
			float64(rank.ParseDec(rec.F)*rank.ParseDec(rec.Weights.F)) +
			float64(rank.ParseDec(rec.Peff)*rank.ParseDec(rec.Weights.P))
		if rank.Dec(recomputed) != rec.Score {
			t.Fatalf("stored score %s != recomputed %s", rec.Score, rank.Dec(recomputed))
		}
		if recomputed != r.Score {
			t.Fatalf("returned score %v != recomputed %v", r.Score, recomputed)
		}
		if finalRank != r.Rank {
			t.Fatalf("rank mismatch: %d vs %d", finalRank, r.Rank)
		}
		if !strings.Contains(text, rec.Score) || !strings.Contains(text, "total") {
			t.Fatalf("why-ranked text missing stored arithmetic:\n%s", text)
		}
	}
}

// M6 acceptance: kill enricher mid-batch → lexical_only served; reindex
// heals to full mode.
func TestEnricherDeathAndReindexHeals(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	for i := 0; i < 6; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator",
			Body: fmt.Sprintf("resilience corpus message %d about failover", i)}); err != nil {
			t.Fatal(err)
		}
	}
	// enricher processes a partial batch, then "dies mid-batch"
	if _, err := d.EnrichOnce(2); err != nil {
		t.Fatal(err)
	}
	d.SetEmbedderForTest(nil)

	out, err := d.Search(daemon.SearchOptions{Query: "failover resilience", K: 5})
	if err != nil {
		t.Fatal(err)
	}
	if out.RetrievalMode != "lexical_only" {
		t.Fatalf("dead enricher must serve lexical_only, got %s", out.RetrievalMode)
	}
	if len(out.Results) == 0 {
		t.Fatal("lexical_only returned nothing")
	}

	// heal: embedder restored, reindex --semantic backfills everything
	d.SetEmbedderForTest(embed.BagOfWords{})
	if _, err := d.ReindexSemantic(); err != nil {
		t.Fatal(err)
	}
	pending, err := d.Projection().PendingEmbeddings(100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("reindex left %d pending: %v", len(pending), err)
	}
	out, err = d.Search(daemon.SearchOptions{Query: "failover resilience", K: 5})
	if err != nil || out.RetrievalMode != "full" {
		t.Fatalf("post-reindex mode %s (%v)", out.RetrievalMode, err)
	}
}

// Model migration: stored vectors from another model force invalidation.
type renamedEmbedder struct{ embed.BagOfWords }

func (renamedEmbedder) ModelID() string { return "dev-bagofwords-v2-MIGRATED" }

func TestModelMigrationInvalidates(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "migration subject text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.EnrichOnce(10); err != nil {
		t.Fatal(err)
	}

	d.SetEmbedderForTest(renamedEmbedder{})
	// incremental enrichment refuses to mix models
	if _, err := d.EnrichOnce(10); err == nil || !strings.Contains(err.Error(), "reindex") {
		t.Fatalf("model mix not refused: %v", err)
	}
	// reindex --semantic migrates
	n, err := d.ReindexSemantic()
	if err != nil || n == 0 {
		t.Fatalf("migration reindex: %d %v", n, err)
	}
	model, _ := d.Projection().EmbeddingModelID()
	if model != "dev-bagofwords-v2-MIGRATED" {
		t.Fatalf("meta model %q", model)
	}
}

// Digest semantics: mandatory ordering, uniform R without a query, interest
// query steering, and the per-line quote prefix.
func TestDigestSemantics(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "plain background note about gardening"}); err != nil {
		t.Fatal(err)
	}
	pinTarget, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "pinned reference document about irrigation"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SimpleEvent("blob.pin", "pin", "0190a1b2-c3d4-7e5f-8901-00000000f001",
		map[string]any{"pin_id": "0190a1b2-c3d4-7e5f-8901-00000000f001", "principal_id": "operator",
			"object_hash": pinTarget.BodyHash, "durability": "pinned"},
		daemon.PublishRequest{Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	recip, err := d.Publish(daemon.PublishRequest{Actor: "operator",
		Body: "directed status update about composting", Recipients: []string{"gardener"}})
	if err != nil {
		t.Fatal(err)
	}

	out, err := d.Digest(daemon.DigestOptions{AgentView: "gardener", BudgetChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	// order: recipient item first, then pinned, then scored
	iRecip := strings.Index(out.Payload, recip.MessageID)
	iPin := strings.Index(out.Payload, pinTarget.MessageID)
	if iRecip < 0 || iPin < 0 || iRecip > iPin {
		t.Fatalf("mandatory ordering wrong (recip@%d pin@%d):\n%s", iRecip, iPin, out.Payload)
	}
	// every quoted body line carries the prefix
	for _, line := range strings.Split(out.Payload, "\n") {
		if strings.Contains(line, "gardening") || strings.Contains(line, "irrigation") || strings.Contains(line, "composting") {
			if !strings.HasPrefix(line, "> [CAIRN] ") {
				t.Fatalf("unprefixed quoted line: %q", line)
			}
		}
	}

	// interest query steers ranking of non-mandatory items
	viewDir := filepath.Join(dir, "views", "gardener")
	os.MkdirAll(viewDir, 0o700)
	os.WriteFile(filepath.Join(viewDir, "view.json"),
		[]byte(`{"interest_query":"gardening background note"}`), 0o644)
	out2, err := d.Digest(daemon.DigestOptions{AgentView: "gardener", BudgetChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2.Payload, "gardening") {
		t.Fatalf("interest-query digest lost the relevant item:\n%s", out2.Payload)
	}
}

// CI-B4: retrieval reads are documented lock-free while the enricher
// goroutine and test hooks mutate embedder state — meaningful only under
// `go test -race`, where any unsynchronized access fails the run.
func TestConcurrentSearchEnrichRace(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for i := 0; i < 5; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: fmt.Sprintf("race corpus item %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	loop := func(f func()) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				f()
			}
		}
	}
	wg.Add(4)
	go loop(func() { d.Search(daemon.SearchOptions{Query: "race corpus", K: 3}) })
	go loop(func() { d.Digest(daemon.DigestOptions{AgentView: "racer", BudgetChars: 800}) })
	go loop(func() { d.EnrichOnce(4) })
	go loop(func() { d.SetEmbedderForTest(embed.BagOfWords{}) })
	time.Sleep(500 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// CAPTURE C2 end-to-end: a mid-token identifier fragment reaches the agent
// through the real send → search path. The projection unit test proves the
// index; this proves the wiring, because LexicalTopK is the ONLY lexical
// candidate source search and digest-interest have.
func TestSearchFindsIdentifierSubstring(t *testing.T) {
	d := startDaemon(t, initCairn(t))

	res, err := d.Publish(daemon.PublishRequest{
		Actor: "operator",
		Body:  "the PeerAdd handler failed err=ECONNREFUSED on run 0190a1b2-c3d4-7e5f-8901-0000000000ff",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "unrelated note about scheduling and calendars",
	}); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"eerAdd", "7e5f-8901", "CONNREFUSED"} {
		out, err := d.Search(daemon.SearchOptions{Query: q, K: 10, BudgetChars: 4000})
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		found := false
		for _, r := range out.Results {
			if r.MessageID == res.MessageID {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q: substring query returned %d results, none the identifier message", q, len(out.Results))
		}
	}
}

// A projection schema bump (C2 raised it to v7) is only safe because the
// daemon rebuilds the DERIVED projection from the log on drift. Prove the
// path, not just the constant: an on-disk projection stamped with an older
// version must be discarded, replayed, and searchable again — with the
// operator told it happened (R45: nothing rebuilds silently).
func TestProjectionSchemaDriftRebuilds(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "drift rebuild canary body"})
	if err != nil {
		t.Fatal(err)
	}
	dbPath := projection.DBPath(dir)
	d.Close()

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE meta SET value='1' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var warn strings.Builder
	d2, err := daemon.Start(daemon.Options{Dir: dir, Warn: &warn})
	if err != nil {
		t.Fatalf("daemon refused to start on schema drift: %v", err)
	}
	defer d2.Close()
	if !strings.Contains(warn.String(), "rebuilding the derived projection") {
		t.Fatalf("schema drift rebuilt silently; warnings were: %q", warn.String())
	}
	out, err := d2.Search(daemon.SearchOptions{Query: "canary", K: 10, BudgetChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].MessageID != res.MessageID {
		t.Fatalf("corpus not replayed after rebuild: %+v", out.Results)
	}
	// the rebuild also repopulates the C2 companion index: "anary" is not a
	// token, so only the trigram side can answer it.
	out, err = d2.Search(daemon.SearchOptions{Query: "anary", K: 10, BudgetChars: 4000})
	if err != nil {
		t.Fatalf("trigram companion missing after rebuild: %v", err)
	}
	if len(out.Results) != 1 || out.Results[0].MessageID != res.MessageID {
		t.Fatalf("trigram companion not repopulated by the rebuild: %+v", out.Results)
	}
}
