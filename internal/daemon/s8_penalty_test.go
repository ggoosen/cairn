package daemon_test

// S8 (spec §9.1): the P2 duplicate and thread-saturation penalties, checked at
// the surface an auditor actually has — `cairn why-ranked` — with the penalties
// PROVABLY firing. The R47/R51 bar is not "the trace looks right": it is that a
// plain IEEE-754 recompute of the printed decimals reproduces the score the
// agent received, sourced from OUTSIDE the explanation record, under every
// profile. These tests reuse reconcileAgainstReturned (fix_p2_1_test.go) so
// there is exactly one recompute in the suite and it is the external one.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/rank"
)

// buildPenaltyCorpus publishes a corpus that fires BOTH penalties on the probe
// query "zzpen": three byte-identical bodies (one content address ⇒ duplicate
// penalty on the two later copies) and a five-message thread (⇒ saturation
// grading up to the cap), plus unique material so the result set is not made
// entirely of penalised items.
func buildPenaltyCorpus(t *testing.T, d *daemon.Daemon) (dupBody string, threadRoot string) {
	t.Helper()
	const twin = "zzpen the cache eviction policy decision, recorded verbatim"
	for i := 0; i < 3; i++ {
		res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: twin})
		if err != nil {
			t.Fatal(err)
		}
		dupBody = res.MessageID
	}
	root, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzpen thread root: rotating the deploy token"})
	if err != nil {
		t.Fatal(err)
	}
	threadRoot = root.MessageID
	for i := 0; i < 4; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator",
			Body:             fmt.Sprintf("zzpen reply %d on the deploy token rotation thread", i),
			ReplyToMessageID: root.MessageID}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator",
			Body: fmt.Sprintf("zzpen unrelated standalone note number %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	return dupBody, threadRoot
}

// penaltyProducts pulls the two penalty products out of a parsed trace.
func penaltyProducts(comp map[string][2]string) (dup, sat float64) {
	return rank.ParseDec(comp["DUP"][0]) * rank.ParseDec(comp["DUP"][1]),
		rank.ParseDec(comp["SAT"][0]) * rank.ParseDec(comp["SAT"][1])
}

// search-P2, penalties live: every returned result's trace must recompute to
// the score in SearchOutput.Results (not to the trace's own total), and both
// penalties must actually have fired somewhere in the set — otherwise the
// reconciliation would be the pre-S8 one wearing a new name.
func TestS8SearchP2ReconcilesWithPenaltiesFiring(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetRankProfileP2ForTest(true)
	buildPenaltyCorpus(t, d)

	out, err := d.Search(daemon.SearchOptions{Query: "zzpen", K: 20, TaskID: "s8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) < 8 {
		t.Fatalf("only %d results: the corpus is not exercising the penalties", len(out.Results))
	}
	dupFired, satFired := 0, 0
	for _, r := range out.Results {
		text, err := d.WhyRanked(out.InteractionID, r.MessageID)
		if err != nil {
			t.Fatalf("why-ranked %s: %v", r.MessageID, err)
		}
		comp := reconcileAgainstReturned(t, text, r.Score)
		dup, sat := penaltyProducts(comp)
		if dup < 0 {
			dupFired++
		}
		if sat < 0 {
			satFired++
		}
		// §9.1: each penalty capped at 0.15, individually
		if dup < -config.PenaltyCap || sat < -config.PenaltyCap {
			t.Fatalf("penalty breached the cap (%v): dup=%v sat=%v\n%s", config.PenaltyCap, dup, sat, text)
		}
	}
	if dupFired != 2 {
		t.Fatalf("duplicate penalty fired on %d results, want 2 (three identical bodies, first is free)", dupFired)
	}
	if satFired != 4 {
		t.Fatalf("thread-saturation penalty fired on %d results, want 4 (a five-message thread)", satFired)
	}
}

// digest-P2, penalties live: same reconciliation, with the returned score taken
// from the rendered digest payload — the number the agent actually reads.
func TestS8DigestP2ReconcilesWithPenaltiesFiring(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetRankProfileP2ForTest(true)
	buildPenaltyCorpus(t, d)

	dout, err := d.Digest(daemon.DigestOptions{AgentView: "op", BudgetChars: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	reconciled, dupFired, satFired := 0, 0, 0
	for _, line := range strings.Split(dout.Payload, "\n") {
		m := digestScoreLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text, err := d.WhyRanked(dout.InteractionID, m[1])
		if err != nil {
			t.Fatalf("why-ranked %s: %v", m[1], err)
		}
		comp := reconcileAgainstReturned(t, text, rank.ParseDec(m[2]))
		reconciled++
		dup, sat := penaltyProducts(comp)
		if dup < 0 {
			dupFired++
		}
		if sat < 0 {
			satFired++
		}
		if dup < -config.PenaltyCap || sat < -config.PenaltyCap {
			t.Fatalf("penalty breached the cap (%v): dup=%v sat=%v\n%s", config.PenaltyCap, dup, sat, text)
		}
	}
	if reconciled < 8 {
		t.Fatalf("digest reconciled only %d entries", reconciled)
	}
	if dupFired != 2 || satFired != 4 {
		t.Fatalf("digest penalties fired dup=%d sat=%d, want 2 and 4", dupFired, satFired)
	}
}

// The penalties must CHANGE THE ORDER, not merely appear in the arithmetic.
// The sharp version of that claim is between the identical copies themselves:
// three byte-identical bodies have the same relevance, the same freshness band
// and the same salience, so the ONLY thing that can separate them is the
// duplicate penalty — and it must put the free copy first. (The weaker claim,
// "penalised items sort last overall", is false and should be: a duplicate of
// the best answer can still beat a weak unique result, which is why the penalty
// is a bounded 0.15 and not an exclusion.)
func TestS8DuplicatesSinkBelowTheirTwin(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetRankProfileP2ForTest(true)
	buildPenaltyCorpus(t, d)

	out, err := d.Search(daemon.SearchOptions{Query: "zzpen", K: 20, TaskID: "s8-order"})
	if err != nil {
		t.Fatal(err)
	}
	// group the results by the body key the DUP line names, then check the free
	// copy of the repeated body outranks every charged copy of it.
	freeRank, chargedRanks := 0, []int(nil)
	for _, r := range out.Results {
		if !strings.Contains(r.Snippet, "cache eviction policy decision, recorded verbatim") {
			continue // not one of the three identical copies
		}
		text, err := d.WhyRanked(out.InteractionID, r.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		comp, _ := parseTrace(t, text)
		dup, _ := penaltyProducts(comp)
		if dup == 0 {
			freeRank = r.Rank
			continue
		}
		chargedRanks = append(chargedRanks, r.Rank)
		// what the copy would have scored without the penalty, recomputed from
		// the SAME published trace: the cost must be exactly the cap, not
		// "roughly the cap" and not the cap plus a hidden adjustment.
		unpenalised := r.Score - dup
		if diff := unpenalised - r.Score; diff < config.PenaltyCap-1e-12 || diff > config.PenaltyCap+1e-12 {
			t.Fatalf("duplicate cost %v, want the cap %v\n%s", diff, config.PenaltyCap, text)
		}
	}
	if freeRank == 0 || len(chargedRanks) != 2 {
		t.Fatalf("expected one free and two charged copies, got free=%d charged=%v", freeRank, chargedRanks)
	}
	for _, cr := range chargedRanks {
		if cr < freeRank {
			t.Fatalf("a charged duplicate ranked %d, above its free twin at %d", cr, freeRank)
		}
	}
}

// §9.1 gives P0 no penalty term, and P0 is what every shipped default runs.
// The trace must say so — DUP and SAT printed (R47: every component, always)
// and both provably zero — on the SAME corpus that penalises heavily under P2.
func TestS8P0AppliesNoPenalties(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	buildPenaltyCorpus(t, d) // default profile: P0

	out, err := d.Search(daemon.SearchOptions{Query: "zzpen", K: 20, TaskID: "s8-p0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) < 8 {
		t.Fatalf("only %d results", len(out.Results))
	}
	for _, r := range out.Results {
		text, err := d.WhyRanked(out.InteractionID, r.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		comp := reconcileAgainstReturned(t, text, r.Score)
		for _, name := range []string{"DUP", "SAT"} {
			if v, w := rank.ParseDec(comp[name][0]), rank.ParseDec(comp[name][1]); v != 0 || w != 0 {
				t.Fatalf("P0 applied a %s penalty (value %v, weight %v) — §9.1 gives P0 no penalty term:\n%s",
					name, v, w, text)
			}
		}
	}
	// and the digest side, which has its own profile
	dout, err := d.Digest(daemon.DigestOptions{AgentView: "op", BudgetChars: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(dout.Payload, "\n") {
		m := digestScoreLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text, err := d.WhyRanked(dout.InteractionID, m[1])
		if err != nil {
			t.Fatal(err)
		}
		comp := reconcileAgainstReturned(t, text, rank.ParseDec(m[2]))
		dup, sat := penaltyProducts(comp)
		if dup != 0 || sat != 0 {
			t.Fatalf("digest-P0 applied penalties dup=%v sat=%v:\n%s", dup, sat, text)
		}
	}
}

// The trace must carry the EVIDENCE for each penalty, not just its value: how
// many earlier results shared the key, and which key. Without it the feature is
// asserted rather than recomputable, and R51's "external verifier" has nothing
// to verify against.
func TestS8TraceCarriesPenaltyEvidence(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetRankProfileP2ForTest(true)
	buildPenaltyCorpus(t, d)

	out, err := d.Search(daemon.SearchOptions{Query: "zzpen", K: 20, TaskID: "s8-evidence"})
	if err != nil {
		t.Fatal(err)
	}
	sawDupEvidence, sawSatEvidence := false, false
	for _, r := range out.Results {
		text, err := d.WhyRanked(out.InteractionID, r.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) < 6 {
				continue
			}
			switch f[0] {
			case "DUP":
				if rank.ParseDec(f[1]) != 0 {
					if !strings.Contains(line, "earlier result") || !strings.Contains(line, "share") {
						t.Fatalf("DUP penalty printed without its evidence:\n%s", line)
					}
					sawDupEvidence = true
				}
			case "SAT":
				if rank.ParseDec(f[1]) != 0 {
					if !strings.Contains(line, "thread") || !strings.Contains(line, "full at") {
						t.Fatalf("SAT penalty printed without its evidence:\n%s", line)
					}
					sawSatEvidence = true
				}
			}
		}
	}
	if !sawDupEvidence || !sawSatEvidence {
		t.Fatalf("no penalty evidence observed (dup=%v sat=%v)", sawDupEvidence, sawSatEvidence)
	}
}
