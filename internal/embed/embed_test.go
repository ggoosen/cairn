package embed

import (
	"math"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
)

func TestBagOfWordsDeterministicUnitVectors(t *testing.T) {
	b := BagOfWords{}
	v1, err := b.Embed([]string{"the quick brown fox", "the quick brown fox", "completely different text"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v1) != 3 || len(v1[0]) != config.EmbeddingDim {
		t.Fatalf("shape: %d × %d", len(v1), len(v1[0]))
	}
	// deterministic
	for i := range v1[0] {
		if v1[0][i] != v1[1][i] {
			t.Fatal("identical texts produced different vectors")
		}
	}
	// unit length
	var norm float64
	for _, x := range v1[0] {
		norm += float64(x) * float64(x)
	}
	if math.Abs(norm-1.0) > 1e-5 {
		t.Fatalf("not unit: %v", norm)
	}
	// self-similarity 1, cross-similarity lower
	if s := Cosine(v1[0], v1[1]); math.Abs(s-1.0) > 1e-6 {
		t.Fatalf("self cosine: %v", s)
	}
	if s := Cosine(v1[0], v1[2]); s > 0.9 {
		t.Fatalf("unrelated texts too similar: %v", s)
	}
	// token overlap ⇒ higher similarity than disjoint
	v2, _ := b.Embed([]string{"quick brown fox jumps", "zebra crossing tokens"})
	if Cosine(v1[0], v2[0]) <= Cosine(v1[0], v2[1]) {
		t.Fatal("overlap similarity ordering violated")
	}
	if b.ModelID() == config.EmbeddingModelID {
		t.Fatal("dev embedder must NEVER share the real model id")
	}
}

func TestCosineMismatchedDims(t *testing.T) {
	if Cosine([]float32{1}, []float32{1, 0}) != -1 {
		t.Fatal("dim mismatch must be sentinel -1")
	}
}

// FIX-G5 (R45): DetectVerbose never returns a nil embedder with an empty
// reason — a lexical-only fallback must always explain itself (the daemon logs
// it loudly at startup). With no venv provisioned it returns (nil, remedy).
func TestG5DetectVerboseAlwaysExplains(t *testing.T) {
	t.Setenv("CAIRN_EMBED_PYTHON", "") // force "no venv" regardless of host
	e, reason := DetectVerbose(t.TempDir(), "")
	if e != nil {
		t.Skip("host has a discoverable embed venv; skipping the no-venv assertion")
	}
	if reason == "" {
		t.Fatal("R45 VIOLATION: lexical-only fallback returned an empty reason (silent)")
	}
	for _, want := range []string{"lexical-only", "cairn-embed-bootstrap.sh", "reindex --semantic"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason missing remedy substring %q: %q", want, reason)
		}
	}
}
