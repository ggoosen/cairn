package daemon_test

// FIX-H3 (R47): a ranking profile's why-ranked explanation must RECONCILE — the
// printed additive components (R, S, F, P_eff, I, N), each × its weight, must
// sum to the returned total, under BOTH profiles. The N9 audit reproduced the
// P2 profile printing lines summing to 0.79 against a returned 0.8255 with
// S/I/N never printed. This test is R47's mandatory shape: parse the output,
// recompute the score from the printed components + weights ALONE, assert exact
// equality with the printed total — over a corpus with salience/intent/novelty
// non-zero. It FAILS against the pre-H3 renderer (which printed only R/F/P_eff).

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/rank"
)

// parseWhyRanked extracts each "NAME val × weight = product" component line and
// the "total X" line from why-ranked output.
func parseWhyRanked(t *testing.T, text string) (comp map[string][2]string, total string) {
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

// reconcile recomputes the score from the printed components and weights alone,
// in the canonical additive order (R, S, F, P_eff, I, N — matching the scorer),
// and asserts exact string equality with the printed total.
func reconcile(t *testing.T, text string) {
	t.Helper()
	comp, total := parseWhyRanked(t, text)
	if total == "" {
		t.Fatalf("no total line in why-ranked output:\n%s", text)
	}
	// S8 added DUP and SAT: R47 names penalties explicitly, so they are part of
	// "every term must be printed" and part of the recompute below.
	for _, name := range []string{"R", "S", "F", "P_eff", "I", "N", "DUP", "SAT"} {
		if _, ok := comp[name]; !ok {
			t.Fatalf("component %q missing from why-ranked output (R47: every term must be printed):\n%s", name, text)
		}
	}
	v := func(name string) float64 { return rank.ParseDec(comp[name][0]) }
	w := func(name string) float64 { return rank.ParseDec(comp[name][1]) }
	// R51: plain IEEE recompute (explicit conversions forbid FMA), matching the
	// external-verifier semantics the scorer now guarantees.
	sum := float64(v("R")*w("R")) + float64(v("S")*w("S")) + float64(v("F")*w("F")) +
		float64(v("P_eff")*w("P_eff")) + float64(v("I")*w("I")) + float64(v("N")*w("N")) +
		float64(v("DUP")*w("DUP")) + float64(v("SAT")*w("SAT"))
	if got := rank.Dec(sum); got != total {
		t.Fatalf("R47 VIOLATION: printed components recompute to %s, returned total %s\n%s", got, total, text)
	}
}

// boostSalient makes hot outrank cold: found across three tasks, a reply, and an
// operator signal — so S, I, and N are all non-zero for the P2 reconciliation.
func boostSalient(t *testing.T, d *daemon.Daemon) (hot string) {
	t.Helper()
	h, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "salience zzquery frequently used decision"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "salience zzquery ignored note"}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"t1", "t2", "t3"} {
		out, err := d.Search(daemon.SearchOptions{Query: "salience", TaskID: task})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Outcome(out.InteractionID, "found", h.MessageID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "building on it", ReplyToMessageID: h.MessageID}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Signal(h.MessageID, "priority_confirm", 3, "operator"); err != nil {
		t.Fatal(err)
	}
	return h.MessageID
}

func TestH3WhyRankedReconcilesBothProfiles(t *testing.T) {
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

			hot := boostSalient(t, d)

			out, err := d.Search(daemon.SearchOptions{Query: "zzquery", TaskID: "final"})
			if err != nil {
				t.Fatal(err)
			}
			// reconcile EVERY returned result, not just the top one.
			for _, r := range out.Results {
				text, err := d.WhyRanked(out.InteractionID, r.MessageID)
				if err != nil {
					t.Fatalf("why-ranked %s: %v", r.MessageID, err)
				}
				reconcile(t, text)
			}
			// and specifically the salient one (S/I/N provably non-zero under P2).
			text, err := d.WhyRanked(out.InteractionID, hot)
			if err != nil {
				t.Fatalf("why-ranked hot: %v", err)
			}
			reconcile(t, text)
		})
	}
}
