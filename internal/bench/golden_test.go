package bench_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/bench"
)

// The embedded corpus must stay byte-identical to the shipped fixtures.
func TestCorpusMatchesShippedFixtures(t *testing.T) {
	for _, name := range []string{"messages.json", "queries.json"} {
		shipped, err := os.ReadFile("../../testdata/corpus/" + name)
		if err != nil {
			t.Fatal(err)
		}
		embedded, err := bench.RawCorpus(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(shipped, embedded) {
			t.Fatalf("internal/bench/corpus/%s drifted from testdata/corpus/%s — copy it over", name, name)
		}
	}
	msgs, queries, err := bench.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 184 || len(queries) != 30 {
		t.Fatalf("corpus shape: %d msgs, %d queries", len(msgs), len(queries))
	}
}

// The live runner reproduces the published claim against the gates.
func TestRunGoldenReproducesClaim(t *testing.T) {
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	var out bytes.Buffer
	res, err := bench.RunGolden(&out, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !res.PassSuccess || res.SuccessAt5 < 0.90 { // published claim: 0.97
		t.Fatalf("Success@5 %.2f (report:\n%s)", res.SuccessAt5, out.String())
	}
	if !res.PassLexicalOnly {
		t.Fatalf("lexical top-10 %.2f", res.LexicalTop10)
	}
	// D11 ratchet. Conjunctive matching scored 0.80 here (24/30) because six of
	// the thirty queries had NO document containing every term, so the lexical
	// arm returned nothing at all for them. Disjunction moved it to 0.97
	// (29/30) — the same single miss as the hybrid pass. This bound is above the
	// old value on purpose: the gate at 0.60 cannot tell the two regimes apart,
	// and a silent return to conjunction would otherwise pass CI.
	if res.LexicalTop10 < 0.96 {
		t.Fatalf("lexical-only top-10 %.2f, want ≥ 0.96 (D11 measured 0.97 = 29/30; "+
			"a drop toward 0.80 means lexical matching went conjunctive again)", res.LexicalTop10)
	}
	for _, want := range []string{"Success@5", "lexical-only top-10", "PASS"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("report missing %q:\n%s", want, out.String())
		}
	}
}
