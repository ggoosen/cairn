package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/eval/internal/score"
)

// This file is the sprint's own guard rail.
//
// S4 built the measurement apparatus while every kill criterion in
// eval/claims.yaml is still `signoff: pending`. The standing rule is that the
// apparatus may run, but nothing may report a verdict, a PASS/FAIL, or a
// claim-supporting summary. The rule is easy to state and easy to erode: one
// helpful "here's the number anyway" print statement is all it takes. So the
// rule is asserted here, over the real commands, on real output.
//
// If one of these fails, the correct response is almost never to relax the
// test.

// verdictWords are comparatives and judgments. "Cairn beats B1" is a verdict
// with the number left off, and it is exactly as forbidden as the number.
var verdictWords = []string{
	"passed", "failed the", "pass:", "fail:", "verdict",
	"beats", "outperform", "wins", "loses to", "better than", "worse than",
	"proves", "confirms", "demonstrates that",
}

// metricValue matches a METRIC NAME IMMEDIATELY FOLLOWED BY A VALUE — the
// shape a leaked measurement takes ("nDCG@5: 0.71", "success_at 5 = 0.9").
//
// Naming a metric is not forbidden and must not be: the ablation catalogue has
// to be able to say "ordering metrics (nDCG, MRR) move honestly but Recall@K
// cannot", and a guard that banned the words would push those caveats out of
// the tool that most needs to state them. What is forbidden is a name with a
// number attached to it, because that is a result.
var metricValue = regexp.MustCompile(`(?i)\b(ndcg|mrr|recall|precision|success)\b\s*(@\s*\d+|_at\s*\d*)?\s*[:=]?\s*\d`)

func assertNoVerdictLanguage(t *testing.T, what, output string) {
	t.Helper()
	lower := strings.ToLower(output)
	for _, w := range verdictWords {
		if strings.Contains(lower, w) {
			t.Fatalf("%s printed %q — S4 is dark: the apparatus runs, nothing reports.\n%s", what, w, output)
		}
	}
	if m := metricValue.FindString(output); m != "" {
		t.Fatalf("%s printed %q — a metric with a value attached is a number reported as evidence.\n%s", what, m, output)
	}
}

func capture(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if runErr != nil {
		t.Fatalf("command failed: %v\n%s", runErr, buf.String())
	}
	return buf.String()
}

// The gate readout must say plainly that nothing may be reported.
func TestClaimsCommandShowsTheGateIsShut(t *testing.T) {
	t.Setenv(ClaimsEnvVar, filepath.Join("..", "..", "claims.yaml"))
	out := capture(t, func() error { return runClaims(nil) })
	if !strings.Contains(out, "NO MEASUREMENT MAY BE REPORTED AS EVIDENCE") {
		t.Fatalf("the gate readout does not state the rule:\n%s", out)
	}
	if strings.Contains(out, "SIGNED") {
		t.Fatalf("a claim reports as signed; the dark-mode assumptions need re-reading:\n%s", out)
	}
}

// The catalogue commands describe the apparatus. They must not describe a
// result.
func TestCatalogueCommandsReportNothing(t *testing.T) {
	assertNoVerdictLanguage(t, "cairn-eval backends", capture(t, func() error { return runBackends(nil) }))
	assertNoVerdictLanguage(t, "cairn-eval ablations -v", capture(t, func() error { return runAblations([]string{"-v"}) }))
	assertNoVerdictLanguage(t, "cairn-eval adversarial -list", capture(t, func() error { return runAdversarial(context.Background(), []string{"-list"}) }))
}

// The full E4 measurement path over the sample corpus, with the file-backed
// baselines so the test stays offline and fast. It must: run, compute, write a
// scorecard whose numbers are real — and print not one of them.
func TestMeasureComputesButReportsNothing(t *testing.T) {
	t.Setenv(ClaimsEnvVar, mustAbs(t, filepath.Join("..", "..", "claims.yaml")))
	dir := t.TempDir()
	out := capture(t, func() error {
		return runMeasure(context.Background(), []string{
			"-corpus", mustAbs(t, filepath.Join("..", "..", "corpora", "sample-plumbing-v1")),
			"-backends", "B0,B1,B2",
			"-split", "dev",
			"-out", dir,
		})
	})

	if !strings.Contains(out, "NO NUMBERS ARE SHOWN") {
		t.Fatalf("the measurement command did not announce the gate:\n%s", out)
	}
	assertNoVerdictLanguage(t, "cairn-eval measure", out)

	// The scorecard must exist, must carry actual metrics, and must stamp
	// itself not-evidence. Computing is allowed; reporting is not.
	card, err := score.ReadFile(filepath.Join(dir, "scorecard-e4.json"))
	if err != nil {
		t.Fatal(err)
	}
	if card.Evidence {
		t.Fatal("the scorecard stamped itself as evidence while every criterion is unsigned")
	}
	if card.NotEvidenceReason == "" {
		t.Fatal("the scorecard does not say why it is not evidence")
	}
	if len(card.Sections) == 0 {
		t.Fatal("no sections: the apparatus did not actually measure anything")
	}
	found := false
	for _, s := range card.Sections {
		if len(s.Metrics) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("no metric was computed — a gate over an empty apparatus proves nothing")
	}

	// And every bearing claim's signoff state travels inside the file, so a
	// scorecard that escapes into a document argues against itself.
	if len(card.BearsOn) == 0 {
		t.Fatal("the scorecard names no claim, so nothing could ever block it")
	}
	for _, c := range card.BearsOn {
		if c.Signed {
			t.Fatalf("claim %s reports as signed", c.ID)
		}
	}
}

// An independent corpus must be refused outright while the criteria are
// unsigned: that run would produce the first half of real evidence.
func TestIndependentCorpusIsRefused(t *testing.T) {
	t.Setenv(ClaimsEnvVar, mustAbs(t, filepath.Join("..", "..", "claims.yaml")))
	src := mustAbs(t, filepath.Join("..", "..", "corpora", "sample-plumbing-v1"))
	fake := t.TempDir()
	for _, name := range []string{"items.jsonl", "queries.jsonl"} {
		blob, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fake, name), blob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(src, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	flipped := strings.Replace(string(manifest), `"independent": false`, `"independent": true`, 1)
	if err := os.WriteFile(filepath.Join(fake, "manifest.json"), []byte(flipped), 0o600); err != nil {
		t.Fatal(err)
	}

	err = runMeasure(context.Background(), []string{"-corpus", fake, "-backends", "B1", "-out", t.TempDir()})
	if err == nil {
		t.Fatal("an independent corpus ran while the kill criteria were unsigned")
	}
	if !strings.Contains(err.Error(), "no override flag") {
		t.Fatalf("the refusal does not close the door: %v", err)
	}
}

// The run records themselves must stay verdict-free: internal/result asserts
// the schema, this asserts what the runner actually wrote.
func TestRunRecordsCarryNoMetrics(t *testing.T) {
	t.Setenv(ClaimsEnvVar, mustAbs(t, filepath.Join("..", "..", "claims.yaml")))
	dir := t.TempDir()
	_ = capture(t, func() error {
		return runMeasure(context.Background(), []string{
			"-corpus", mustAbs(t, filepath.Join("..", "..", "corpora", "sample-plumbing-v1")),
			"-backends", "B1", "-surfaces", "search", "-split", "dev", "-out", dir,
		})
	})
	files, err := filepath.Glob(filepath.Join(dir, "e4-*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no run records: %v", err)
	}
	for _, f := range files {
		blob, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var generic map[string]any
		if err := json.Unmarshal(blob, &generic); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"ndcg", "mrr", "success_at", "verdict", "evidence"} {
			if _, present := generic[forbidden]; present {
				t.Fatalf("%s: run record has a top-level %q field; observations and judgments must stay apart", f, forbidden)
			}
		}
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
