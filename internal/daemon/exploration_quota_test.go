package daemon

// FIX-P2H4 (spec §9.2): the 10% exploration quota reserves a fraction of the K
// result slots for NEW items (below SalienceMinImpressions qualified
// impressions) so cold-start content gets exposure instead of being buried by
// items that have already accumulated salience.

import (
	"testing"

	"github.com/ggoosen/cairn/internal/rank"
)

func scoredIDs(ids ...string) []rank.Scored {
	out := make([]rank.Scored, len(ids))
	for i, id := range ids {
		out[i] = rank.Scored{Candidate: rank.Candidate{MessageID: id}}
	}
	return out
}

func idsOf(s []rank.Scored) []string {
	out := make([]string, len(s))
	for i := range s {
		out[i] = s[i].MessageID
	}
	return out
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// A new item just below the top-K cut is promoted into the reserved slot,
// displacing the lowest merit item; global rank order is preserved.
func TestExplorationQuotaPromotesNewItem(t *testing.T) {
	// 12 candidates, K=10 → quota=1, merit=9. Indices 0..8 are the merit slots;
	// index 9 is an OLD item (would take the 10th slot without a quota); indices
	// 10,11 are NEW. The reserved exploration slot must go to the best new item
	// in the cut region (index 10), NOT the old index-9 item.
	scored := scoredIDs("m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "old9", "new10", "new11")
	newItems := map[string]bool{"new10": true, "new11": true}
	out := applyExplorationQuota(scored, 10, func(id string) bool { return newItems[id] })

	got := idsOf(out)
	if len(got) != 10 {
		t.Fatalf("expected 10 results, got %d: %v", len(got), got)
	}
	if !contains(got, "new10") {
		t.Fatalf("exploration quota did not promote the new item: %v", got)
	}
	if contains(got, "old9") {
		t.Fatalf("old item at the cut kept its slot; quota did not displace it: %v", got)
	}
	// global rank order preserved (output is a subsequence of the input order)
	last := -1
	pos := map[string]int{}
	for i := range scored {
		pos[scored[i].MessageID] = i
	}
	for _, id := range got {
		if pos[id] <= last {
			t.Fatalf("output not in rank order at %q: %v", id, got)
		}
		last = pos[id]
	}
}

// When no new items exist in the cut region, the reserved slot backfills with
// the next-best by merit — the quota never wastes budget.
func TestExplorationQuotaBackfillsWhenNoNewItems(t *testing.T) {
	scored := scoredIDs("m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "m10", "m11")
	out := applyExplorationQuota(scored, 10, func(id string) bool { return false })
	got := idsOf(out)
	if len(got) != 10 {
		t.Fatalf("expected 10 results, got %d: %v", len(got), got)
	}
	// exactly the plain top-10
	for i := 0; i < 10; i++ {
		if got[i] != scored[i].MessageID {
			t.Fatalf("backfill diverged from top-K at %d: got %q want %q\n%v", i, got[i], scored[i].MessageID, got)
		}
	}
}

// A result set that already fits within K is returned untouched.
func TestExplorationQuotaNoCut(t *testing.T) {
	scored := scoredIDs("a", "b", "c")
	out := applyExplorationQuota(scored, 10, func(id string) bool { return true })
	if len(out) != 3 || out[0].MessageID != "a" || out[2].MessageID != "c" {
		t.Fatalf("unexpected result when nothing is cut: %v", idsOf(out))
	}
}

// K too small to reserve a whole slot (K < 1/fraction) keeps the plain top-K
// cut — no tiny-K starvation of the best result by an exploration slot.
func TestExplorationQuotaTinyKNoStarvation(t *testing.T) {
	scored := scoredIDs("best", "new1", "new2")
	out := applyExplorationQuota(scored, 1, func(id string) bool { return id != "best" })
	got := idsOf(out)
	if len(got) != 1 || got[0] != "best" {
		t.Fatalf("tiny-K search should keep the single best result, got %v", got)
	}
}
