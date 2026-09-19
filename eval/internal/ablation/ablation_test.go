package ablation

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/claims"
	"github.com/ggoosen/cairn/eval/internal/explain"
)

// Every arm must name a claim the register actually holds. An arm bearing on
// nothing could never be blocked by the reporting gate, so it could never be
// dark — which is how an unfalsifiable number gets published.
func TestEveryArmBearsOnARealClaim(t *testing.T) {
	reg, err := claims.Load(filepath.Join("..", "..", claims.DefaultPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range Catalogue() {
		if len(a.BearsOn) == 0 {
			t.Fatalf("arm %q bears on no claim", a.ID)
		}
		for _, id := range a.BearsOn {
			if _, ok := reg.Get(id); !ok {
				t.Fatalf("arm %q bears on %q, which is not in the register", a.ID, id)
			}
		}
	}
}

// Every arm must state what it cannot show, and every recomputed arm must
// carry the shared ceiling verbatim. A limitation that can be edited per-arm is
// a limitation that gets softened on the arm somebody most wants to quote.
func TestEveryArmDeclaresItsLimits(t *testing.T) {
	for _, a := range Catalogue() {
		if a.Fidelity == Unavailable {
			if a.Why == "" {
				t.Fatalf("unavailable arm %q gives no reason", a.ID)
			}
			continue
		}
		if strings.TrimSpace(a.Limits) == "" {
			t.Fatalf("arm %q states no limits", a.ID)
		}
		if a.Fidelity == Recomputed && !strings.Contains(a.Limits, "Recall@K at the requested K CANNOT MOVE") {
			t.Fatalf("recomputed arm %q dropped the shared ceiling statement", a.ID)
		}
	}
}

// An unavailable arm must fail loudly. Silently running the default and
// labelling it with the arm's name is a fabricated result.
func TestUnavailableArmsFailLoudly(t *testing.T) {
	a, err := Get(PriorityUndecayed)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fidelity != Unavailable {
		t.Fatalf("%s is no longer declared unavailable; if it was implemented this test should assert the implementation", a.ID)
	}
	if !errors.Is(a.Err(), ErrUnavailable) {
		t.Fatalf("Err() = %v", a.Err())
	}
	for _, other := range Catalogue() {
		if other.Fidelity != Unavailable && other.Err() != nil {
			t.Fatalf("available arm %q returns an error", other.ID)
		}
	}
}

// Only native arms may carry backend configuration; a recomputed arm that also
// changed the system would be measuring two things at once.
func TestOnlyNativeArmsConfigureTheSystem(t *testing.T) {
	for _, a := range Catalogue() {
		cfg := a.BackendConfig()
		if a.Fidelity != Native && !cfg.IsDefault() {
			t.Fatalf("non-native arm %q changes the system under test: %+v", a.ID, cfg)
		}
		if a.Fidelity == Recomputed && a.Rerank == nil {
			t.Fatalf("recomputed arm %q has no rerank function", a.ID)
		}
		if a.Fidelity != Recomputed && a.Rerank != nil {
			t.Fatalf("arm %q is %s but carries a rerank function", a.ID, a.Fidelity)
		}
	}
}

func mkRanked(id string, rank int, terms string) Ranked {
	e, err := explain.Parse(id + "  rank " + itoa(rank) + " (profile=search-P0)\n" + terms)
	if err != nil {
		panic(err)
	}
	return Ranked{Hit: backend.Hit{Rank: rank, ItemID: id, NativeID: id}, Exp: e}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

const (
	// old but highly relevant; F contributes nothing
	oldRelevant = "  R     1 × 0.9 = 0.9\n  S     0 × 0 = 0\n  F     0 × 0.07 = 0\n  P_eff 0 × 0.03 = 0\n  I     0 × 0 = 0\n  N     0 × 0 = 0\n  total 0.9\n"
	// fresh but weakly relevant; F carries it
	freshWeak = "  R     0.95 × 0.9 = 0.855\n  S     0 × 0 = 0\n  F     1 × 0.07 = 0.07\n  P_eff 0 × 0.03 = 0\n  I     0 × 0 = 0\n  N     0 × 0 = 0\n  total 0.925\n"
)

// Removing freshness must reorder exactly the pair freshness inverted, and
// nothing else.
func TestNoFreshnessRerank(t *testing.T) {
	a, _ := Get(NoFreshness)
	in := []Ranked{mkRanked("fresh", 1, freshWeak), mkRanked("old", 2, oldRelevant)}
	out := a.Rerank(in)
	if len(out) != 2 || out[0].Hit.ItemID != "old" || out[0].Hit.Rank != 1 {
		t.Fatalf("dropping F did not promote the older, more relevant item: %+v", out)
	}
}

// Vector-only must DROP what the vector retriever never returned, not demote
// it: a vector-only condition does not have those documents at all.
func TestVectorOnlyDropsLexicalOnlyHits(t *testing.T) {
	a, _ := Get(VectorOnly)
	withVec := "  R     1 × 0.9 = 0.9   (lex rank 5, vec rank 1, RRF 0.03)\n  S     0 × 0 = 0\n  F     0 × 0.07 = 0\n  P_eff 0 × 0.03 = 0\n  I     0 × 0 = 0\n  N     0 × 0 = 0\n  total 0.9\n"
	lexOnly := "  R     1 × 0.9 = 0.9   (lex rank 1, vec rank 0, RRF 0.016)\n  S     0 × 0 = 0\n  F     0 × 0.07 = 0\n  P_eff 0 × 0.03 = 0\n  I     0 × 0 = 0\n  N     0 × 0 = 0\n  total 0.9\n"
	out := a.Rerank([]Ranked{mkRanked("lex", 1, lexOnly), mkRanked("vec", 2, withVec)})
	if len(out) != 1 || out[0].Hit.ItemID != "vec" {
		t.Fatalf("vector-only kept a hit the vector retriever never returned: %+v", out)
	}
}

// A hit whose arithmetic could not be read must sort to the bottom rather than
// take an invented position.
func TestUnexplainedHitsSortLast(t *testing.T) {
	a, _ := Get(NoFreshness)
	out := a.Rerank([]Ranked{
		{Hit: backend.Hit{Rank: 1, ItemID: "unknown"}},
		mkRanked("known", 2, oldRelevant),
	})
	if out[0].Hit.ItemID != "known" {
		t.Fatalf("an unexplained hit outranked an explained one: %+v", out)
	}
}

// A baseline must refuse an arm it cannot realize, rather than running its
// default under the arm's name.
func TestBaselinesRefuseNativeArms(t *testing.T) {
	p2, _ := Get(ProfileP2)
	for _, id := range []backend.ID{backend.B0NoMemory, backend.B1GrepTranscript, backend.B2FlatNotes} {
		b, err := backend.New(id)
		if err != nil {
			t.Fatal(err)
		}
		err = b.Open(t.Context(), backend.Config{WorkDir: t.TempDir(), Arm: p2.BackendConfig()})
		if !errors.Is(err, backend.ErrArmUnrealizable) {
			t.Fatalf("%s accepted a Cairn-only ablation arm: %v", id, err)
		}
	}
}
