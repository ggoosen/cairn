// Package bench implements `cairn bench golden` (FIX-F8.2, Codex
// AUDIT2-002): the golden-corpus retrieval benchmark, reproducible from the
// shipped binary without reading test code. It loads the checked-in corpus
// (testdata/corpus/, embedded here with a drift test) into a TEMPORARY
// mesh via the real daemon paths and reports Success@5 (hybrid, dev
// embedder) and lexical-only top-10 against the P0 gates. This is also the
// P2 ranking-calibration harness.
package bench

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	cairnembed "github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/identity"
)

//go:embed corpus/messages.json corpus/queries.json
var corpusFS embed.FS

// Message and Query are the corpus fixture records.
type Message struct {
	Key  string `json:"key"`
	Body string `json:"body"`
}

type Query struct {
	Query    string   `json:"query"`
	Relevant []string `json:"relevant"`
}

// Corpus returns the embedded golden corpus.
func Corpus() ([]Message, []Query, error) {
	var msgs []Message
	var queries []Query
	mb, err := corpusFS.ReadFile("corpus/messages.json")
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(mb, &msgs); err != nil {
		return nil, nil, err
	}
	qb, err := corpusFS.ReadFile("corpus/queries.json")
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(qb, &queries); err != nil {
		return nil, nil, err
	}
	return msgs, queries, nil
}

// RawCorpus exposes the embedded bytes (fixture drift test).
func RawCorpus(name string) ([]byte, error) { return corpusFS.ReadFile("corpus/" + name) }

// Result of one golden run.
type Result struct {
	Messages        int     `json:"messages"`
	Queries         int     `json:"queries"`
	SuccessAt5      float64 `json:"success_at_5"`
	LexicalTop10    float64 `json:"lexical_only_top_10"`
	GateSuccess     float64 `json:"gate_success_at_5"`
	GateLexical     float64 `json:"gate_lexical_top_10"`
	PassSuccess     bool    `json:"pass_success_at_5"`
	PassLexicalOnly bool    `json:"pass_lexical_only"`
	EmbedderModel   string  `json:"embedder_model"`
}

// RunGolden executes the benchmark in a throwaway mesh and writes a
// human-readable report to w.
func RunGolden(w io.Writer) (*Result, error) {
	msgs, queries, err := Corpus()
	if err != nil {
		return nil, err
	}

	scratch, err := os.MkdirTemp("", "cairn-bench-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	// isolate device state so the bench never touches the operator's real
	// identity area (restored afterwards)
	oldEnv, hadEnv := os.LookupEnv("CAIRN_DEVICE_STATE_DIR")
	os.Setenv("CAIRN_DEVICE_STATE_DIR", filepath.Join(scratch, "devstate"))
	defer func() {
		if hadEnv {
			os.Setenv("CAIRN_DEVICE_STATE_DIR", oldEnv)
		} else {
			os.Unsetenv("CAIRN_DEVICE_STATE_DIR")
		}
	}()

	dir := filepath.Join(scratch, "mesh")
	if _, err := identity.Initialize(identity.InitOptions{
		Dir:              dir,
		AllowUnencrypted: true, // throwaway scratch mesh
		Out:              io.Discard,
	}); err != nil {
		return nil, err
	}

	fmt.Fprintf(w, "golden corpus: %d messages, %d queries (dev embedder %q)\n",
		len(msgs), len(queries), cairnembed.BagOfWords{}.ModelID())
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: cairnembed.BagOfWords{}, Warn: io.Discard})
	if err != nil {
		return nil, err
	}
	defer d.Close()

	keyToID := map[string]string{}
	start := time.Now()
	for _, m := range msgs {
		res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: m.Body})
		if err != nil {
			return nil, fmt.Errorf("publishing %s: %w", m.Key, err)
		}
		keyToID[m.Key] = res.MessageID
	}
	for {
		n, err := d.EnrichOnce(config.EnrichBatch)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	fmt.Fprintf(w, "loaded + enriched in %v\n\n", time.Since(start).Round(time.Millisecond))

	hit := func(results []daemon.RankedResult, relevant []string) bool {
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

	res := &Result{
		Messages: len(msgs), Queries: len(queries),
		GateSuccess: config.GateSuccessAt5Min, GateLexical: config.GateLexicalOnlyTop10Min,
		EmbedderModel: cairnembed.BagOfWords{}.ModelID(),
	}

	hits := 0
	for _, q := range queries {
		out, err := d.Search(daemon.SearchOptions{Query: q.Query, K: 5})
		if err != nil {
			return nil, err
		}
		ok := hit(out.Results, q.Relevant)
		if ok {
			hits++
		} else {
			fmt.Fprintf(w, "  miss@5: %q (wanted %v)\n", q.Query, q.Relevant)
		}
	}
	res.SuccessAt5 = float64(hits) / float64(len(queries))
	res.PassSuccess = res.SuccessAt5 >= res.GateSuccess

	d.SetEmbedderForTest(nil) // lexical-only pass
	hits = 0
	for _, q := range queries {
		out, err := d.Search(daemon.SearchOptions{Query: q.Query, K: 10})
		if err != nil {
			return nil, err
		}
		if out.RetrievalMode != "lexical_only" {
			return nil, fmt.Errorf("expected lexical_only, got %s", out.RetrievalMode)
		}
		if hit(out.Results, q.Relevant) {
			hits++
		}
	}
	res.LexicalTop10 = float64(hits) / float64(len(queries))
	res.PassLexicalOnly = res.LexicalTop10 >= res.GateLexical

	verdict := func(pass bool) string {
		if pass {
			return "PASS"
		}
		return "FAIL"
	}
	fmt.Fprintf(w, "\nSuccess@5 (hybrid)     %.2f  (gate ≥ %.2f)  %s\n", res.SuccessAt5, res.GateSuccess, verdict(res.PassSuccess))
	fmt.Fprintf(w, "lexical-only top-10    %.2f  (gate ≥ %.2f)  %s\n", res.LexicalTop10, res.GateLexical, verdict(res.PassLexicalOnly))
	fmt.Fprintln(w, "\nnote: scores use the deterministic dev embedder; identical inputs")
	fmt.Fprintln(w, "reproduce identical hit sets (freshness decays with wall time, so raw")
	fmt.Fprintln(w, "scores drift; hit identity and ordering do not).")
	return res, nil
}
