package daemon_test

// FIX-P2-1 (R51, strengthens R47/H3): why-ranked reconciliation means every
// SCORED term is printed — not merely that printed lines are formatted
// precisely. The H3 test reconciled the printed components against the printed
// "total" line; both come from the same stored record, so a term that was
// scored but never stored/printed would leave a self-consistent-but-wrong
// trace and the test would still pass. This test closes that hole:
//
//  1. the recompute is asserted against the RETURNED score (SearchOutput
//     .Results[].Score for search; the rendered "score=" in the digest payload
//     for digest) — a value that reaches the agent OUTSIDE the explanation
//     record — so any scored-but-unprinted term breaks the equality;
//  2. every additive term line (R, S, F, P_eff, I, N) must be present, and
//     under P2 the salient message must show R, S, F, I and N all NON-ZERO in
//     the trace — a term that is scored non-zero but traces as 0.0 fails here
//     directly, and also fails (1) because the sum no longer matches;
//  3. it runs under BOTH profiles (P0 search/digest and P2 search/digest)
//     over a corpus where R, S, F, I, N are all non-zero.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/rank"
)

// parseTrace extracts each "NAME value × weight = product" line and the
// "total X" line from a why-ranked trace.
func parseTrace(t *testing.T, text string) (comp map[string][2]string, total string) {
	t.Helper()
	comp = map[string][2]string{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) >= 6 && f[2] == "×" && f[4] == "=" {
			comp[f[0]] = [2]string{f[1], f[3]} // value, weight
			continue
		}
		if len(f) >= 2 && f[0] == "total" {
			total = f[1]
		}
	}
	return comp, total
}

// reconcileAgainstReturned recomputes the score from the printed components and
// weights ALONE (scorer term order: R, S, F, P_eff, I, N) and asserts exact
// equality with the RETURNED score — the number the agent actually saw, sourced
// outside the explanation record. Any scored term missing from the trace (or
// traced as the wrong value) breaks this equality.
func reconcileAgainstReturned(t *testing.T, text string, returned float64) map[string][2]string {
	t.Helper()
	comp, total := parseTrace(t, text)
	if total == "" {
		t.Fatalf("no total line in why-ranked output:\n%s", text)
	}
	for _, name := range []string{"R", "S", "F", "P_eff", "I", "N"} {
		if _, ok := comp[name]; !ok {
			t.Fatalf("scored term %q missing from why-ranked trace (every scored term must be printed):\n%s", name, text)
		}
	}
	v := func(name string) float64 { return rank.ParseDec(comp[name][0]) }
	w := func(name string) float64 { return rank.ParseDec(comp[name][1]) }
	// Recompute with PLAIN IEEE-754 semantics — each product explicitly rounded
	// (the float64 conversions forbid FMA contraction, per the Go spec), adds in
	// term order. This is what an auditor's python/bc/jq recompute does; the
	// returned score must be reproducible by it, not only by a Go expression
	// that happens to fuse the same way the scorer did.
	sum := float64(v("R")*w("R")) + float64(v("S")*w("S")) + float64(v("F")*w("F")) +
		float64(v("P_eff")*w("P_eff")) + float64(v("I")*w("I")) + float64(v("N")*w("N"))
	if got, want := rank.Dec(sum), rank.Dec(returned); got != want {
		t.Fatalf("trace does not reconcile with the RETURNED score: printed components recompute to %s, returned score is %s\n%s", got, want, text)
	}
	if got := rank.Dec(sum); got != total {
		t.Fatalf("printed total %s disagrees with recomputed components %s\n%s", total, got, text)
	}
	return comp
}

// buildAllTermsCorpus publishes a corpus where the "hot" message has every P2
// additive input non-zero: fetched across three distinct tasks (S), replied to
// (S in-degree), priority-confirmed (I=0.6, Suspended), shown in prior result
// lists (impressions ⇒ 0 < N < 1), and the best match for the probe query
// (top RRF ⇒ R=1); freshness F is always non-zero.
func buildAllTermsCorpus(t *testing.T, d *daemon.Daemon) (hot string) {
	t.Helper()
	h, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "allterms zzprobe zzprobe zzprobe frequently used decision record"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "allterms zzprobe ignored side note"}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"t1", "t2", "t3"} {
		out, err := d.Search(daemon.SearchOptions{Query: "allterms", TaskID: task})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Outcome(out.InteractionID, "found", h.MessageID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "building on the decision", ReplyToMessageID: h.MessageID}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Signal(h.MessageID, "priority_confirm", 3, "operator"); err != nil {
		t.Fatal(err)
	}
	return h.MessageID
}

func TestP2WhyRankedReconcilesWithReturnedScore(t *testing.T) {
	for _, p2 := range []bool{false, true} {
		name := "P0"
		if p2 {
			name = "P2"
		}
		t.Run(name, func(t *testing.T) {
			dir := initCairn(t)
			d, err := daemon.Start(daemon.Options{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			d.SetRankProfileP2ForTest(p2)

			hot := buildAllTermsCorpus(t, d)

			out, err := d.Search(daemon.SearchOptions{Query: "zzprobe", TaskID: "final"})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Results) < 2 {
				t.Fatalf("corpus too small: %d results", len(out.Results))
			}
			var hotComp map[string][2]string
			for _, r := range out.Results {
				text, err := d.WhyRanked(out.InteractionID, r.MessageID)
				if err != nil {
					t.Fatalf("why-ranked %s: %v", r.MessageID, err)
				}
				comp := reconcileAgainstReturned(t, text, r.Score)
				if r.MessageID == hot {
					hotComp = comp
				}
			}
			if hotComp == nil {
				t.Fatalf("hot message %s not in results", hot)
			}
			if p2 {
				// every scored term must trace NON-ZERO for the salient message:
				// a term that is scored but reads 0.0 in the trace fails here even
				// before the sum check does.
				for _, name := range []string{"R", "S", "F", "I", "N"} {
					if rank.ParseDec(hotComp[name][0]) == 0 {
						t.Fatalf("P2 term %s traces as 0.0 for the salient message (scored term zeroed/omitted in trace)", name)
					}
				}
			}
		})
	}
}

var digestScoreLine = regexp.MustCompile(`^\d+\. (\S+).* score=(\S+)$`)

// TestP2WhyRankedReconcilesDigestProfile does the same reconciliation for the
// digest profiles: the returned score is parsed from the rendered digest
// payload (what the agent reads), never from the stored explanation.
func TestP2WhyRankedReconcilesDigestProfile(t *testing.T) {
	for _, p2 := range []bool{false, true} {
		name := "P0"
		if p2 {
			name = "P2"
		}
		t.Run(name, func(t *testing.T) {
			dir := initCairn(t)
			d, err := daemon.Start(daemon.Options{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			d.SetRankProfileP2ForTest(p2)

			buildAllTermsCorpus(t, d)

			dout, err := d.Digest(daemon.DigestOptions{AgentView: "op", BudgetChars: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			reconciled := 0
			for _, line := range strings.Split(dout.Payload, "\n") {
				m := digestScoreLine.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				text, err := d.WhyRanked(dout.InteractionID, m[1])
				if err != nil {
					t.Fatalf("why-ranked %s: %v", m[1], err)
				}
				reconcileAgainstReturned(t, text, rank.ParseDec(m[2]))
				reconciled++
			}
			if reconciled < 2 {
				t.Fatalf("digest reconciled only %d entries", reconciled)
			}
		})
	}
}
