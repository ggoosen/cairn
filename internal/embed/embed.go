// Package embed defines the embedding interface (rulings §7: all-MiniLM-L6-v2,
// 384-d float32, pinned model ID + artifact hash, cosine distance) and its
// P0 implementations.
//
// DEVIATION (recorded in PROGRESS.md, sanctioned by CLAUDE.md's fallback
// rule): the ONNX in-process binding requires a runtime onnxruntime dylib
// plus a WordPiece tokenizer, unavailable in this environment — the
// real-model path is the documented Python-venv subprocess (sentence-
// transformers, SAME model, identical interface). Without a provisioned
// venv the daemon serves lexical_only (degradation ladder step 4).
package embed

import (
	"math"
	"strings"
	"unicode"

	"lukechampine.com/blake3"

	"github.com/ggoosen/cairn/internal/config"
)

// Embedder turns text into unit-length float32 vectors. Implementations
// must be deterministic for identical input.
type Embedder interface {
	// ModelID identifies the model; vectors from different models are NEVER
	// compared (rulings §7).
	ModelID() string
	Dim() int
	Embed(texts []string) ([][]float32, error)
	Close() error
}

// Cosine computes cosine similarity of two unit vectors (dot product).
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return -1
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

// --- deterministic dev/test embedder ----------------------------------------

// BagOfWords is the deterministic DEV/TEST embedder: hashed bag-of-words
// projected into config.EmbeddingDim dimensions, L2-normalized. Real enough
// that token overlap ⇒ vector similarity, so the entire vector path (fusion,
// enrichment, reindex) is exercised honestly in CI. Never the default in
// production — its ModelID is distinct so its vectors can never be compared
// with the real model's.
type BagOfWords struct{}

func (BagOfWords) ModelID() string { return "dev-bagofwords-v1" }
func (BagOfWords) Dim() int        { return config.EmbeddingDim }
func (BagOfWords) Close() error    { return nil }

func (b BagOfWords) Embed(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec := make([]float32, config.EmbeddingDim)
		for _, tok := range tokenize(t) {
			sum := blake3.Sum256([]byte(tok))
			idx := (int(sum[0])<<8 | int(sum[1])) % config.EmbeddingDim
			sign := float32(1)
			if sum[2]&1 == 1 {
				sign = -1
			}
			vec[idx] += sign
		}
		normalize(vec)
		out[i] = vec
	}
	return out, nil
}

func tokenize(t string) []string {
	return strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func normalize(v []float32) {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return
	}
	inv := float32(1.0 / math.Sqrt(norm))
	for i := range v {
		v[i] *= inv
	}
}
