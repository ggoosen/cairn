package daemon_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/rank"
)

// --- golden corpus (BUILD-PLAN M6 / TESTING.md §4) ---------------------------
// ~200 messages across 4 synthetic projects; ~30 queries with known-relevant
// sets. Deterministic (generated in code). CI numbers use the deterministic
// dev embedder; real-model numbers are recorded during M8 dogfood.

type corpusQuery struct {
	query    string
	relevant []string // message keys
}

type corpusMsg struct {
	key  string
	body string
}

func goldenCorpus() ([]corpusMsg, []corpusQuery) {
	projects := []struct {
		name  string
		vocab []string
	}{
		{"zebra-api", []string{"endpoint", "authentication", "token", "handler", "middleware"}},
		{"billing", []string{"invoice", "subscription", "proration", "refund", "ledger"}},
		{"deploy-infra", []string{"kubernetes", "rollout", "canary", "terraform", "autoscaler"}},
		{"ml-pipeline", []string{"embedding", "training", "checkpoint", "tokenizer", "inference"}},
	}
	var msgs []corpusMsg
	for _, p := range projects {
		for i := 0; i < 44; i++ {
			w := p.vocab[i%len(p.vocab)]
			msgs = append(msgs, corpusMsg{
				key: fmt.Sprintf("%s-%d", p.name, i),
				body: fmt.Sprintf("%s note %d: routine %s maintenance detail for the %s project, nothing unusual.",
					p.name, i, w, p.name),
			})
		}
	}
	// distinctive anchor documents the queries target
	anchors := []corpusMsg{
		{"zebra-auth-fix", "zebra-api decision: the authentication token rotation bug was fixed by double-submitting the refresh handler nonce."},
		{"zebra-rate", "zebra-api conclusion: rate limiting uses a sliding window of ninety seconds per caller identity."},
		{"billing-proration", "billing decision: proration credits apply at invoice finalization, never at subscription change time."},
		{"billing-tax", "billing note: tax calculation for australian customers uses the gst inclusive ledger path."},
		{"deploy-canary", "deploy-infra runbook: canary rollout aborts automatically when error budget burn exceeds two percent."},
		{"deploy-dns", "deploy-infra postmortem: the dns outage came from terraform destroying the private zone during autoscaler cleanup."},
		{"ml-tokenizer", "ml-pipeline finding: the tokenizer truncates unicode grapheme clusters, corrupting embedding checkpoints."},
		{"ml-gpu", "ml-pipeline capacity: training jobs need four gpus with gradient checkpoint sharding enabled."},
	}
	msgs = append(msgs, anchors...)

	queries := []corpusQuery{
		{"authentication token rotation bug", []string{"zebra-auth-fix"}},
		{"refresh handler nonce fix", []string{"zebra-auth-fix"}},
		{"rate limiting sliding window", []string{"zebra-rate"}},
		{"ninety seconds per caller", []string{"zebra-rate"}},
		{"proration credits invoice", []string{"billing-proration"}},
		{"when do proration credits apply", []string{"billing-proration"}},
		{"australian gst tax calculation", []string{"billing-tax"}},
		{"canary rollout abort error budget", []string{"deploy-canary"}},
		{"error budget burn two percent", []string{"deploy-canary"}},
		{"dns outage terraform private zone", []string{"deploy-dns"}},
		{"autoscaler cleanup destroyed zone", []string{"deploy-dns"}},
		{"tokenizer unicode grapheme truncation", []string{"ml-tokenizer"}},
		{"corrupted embedding checkpoints", []string{"ml-tokenizer"}},
		{"training gpu capacity", []string{"ml-gpu"}},
		{"gradient checkpoint sharding", []string{"ml-gpu"}},
		{"zebra authentication middleware", []string{"zebra-auth-fix", "zebra-rate"}},
		{"invoice finalization subscription", []string{"billing-proration"}},
		{"kubernetes canary error", []string{"deploy-canary"}},
		{"grapheme clusters corrupt", []string{"ml-tokenizer"}},
		{"token rotation refresh", []string{"zebra-auth-fix"}},
		{"sliding window caller identity", []string{"zebra-rate"}},
		{"gst inclusive ledger", []string{"billing-tax"}},
		{"terraform destroying zone", []string{"deploy-dns"}},
		{"four gpus training", []string{"ml-gpu"}},
		{"proration at subscription change", []string{"billing-proration"}},
		{"burn exceeds two percent", []string{"deploy-canary"}},
		{"unicode tokenizer finding", []string{"ml-tokenizer"}},
		{"tax australian customers", []string{"billing-tax"}},
		{"nonce double submit", []string{"zebra-auth-fix"}},
		{"rollout aborts automatically", []string{"deploy-canary"}},
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
		res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: m.body})
		if err != nil {
			t.Fatal(err)
		}
		keyToID[m.key] = res.MessageID
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
		out, err := d.Search(daemon.SearchOptions{Query: q.query, K: 5})
		if err != nil {
			t.Fatal(err)
		}
		if out.RetrievalMode != "full" {
			t.Fatalf("corpus search not in full mode: %s", out.RetrievalMode)
		}
		if inTop(out.Results, keyToID, q.relevant) {
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
		out, err := d.Search(daemon.SearchOptions{Query: q.query, K: 10})
		if err != nil {
			t.Fatal(err)
		}
		if out.RetrievalMode != "lexical_only" {
			t.Fatalf("expected lexical_only, got %s", out.RetrievalMode)
		}
		if inTop(out.Results, keyToID, q.relevant) {
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
		recomputed := rank.ParseDec(rec.R)*rank.ParseDec(rec.Weights.R) +
			rank.ParseDec(rec.F)*rank.ParseDec(rec.Weights.F) +
			rank.ParseDec(rec.Peff)*rank.ParseDec(rec.Weights.P)
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
