// Package backend defines the ONE interface every BUILD-PLAN §5-E4 memory
// condition is driven through: B0 no-memory, B1 grep-over-transcripts, B2
// flat markdown notes, B3 naive vector RAG, B4 full-context stuffing, B5
// Cairn.
//
// The point of a single interface is that the baselines are not strawmen by
// construction. Every condition gets the same items written to it in the same
// order, is asked the same questions with the same budget, and returns the
// same shape of answer. Whatever differences appear are differences in the
// memory system, not in the harness's generosity.
//
// SCOPE: this package retrieves and records. It computes no metrics and
// makes no comparison — E4 owns that, and only after the kill criteria in
// eval/claims.yaml carry an operator signoff.
package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ggoosen/cairn/eval/internal/cairnctl"
)

// ID identifies a memory condition. The letters are BUILD-PLAN §5-E4's.
type ID string

const (
	B0NoMemory       ID = "B0"
	B1GrepTranscript ID = "B1"
	B2FlatNotes      ID = "B2"
	B3VectorRAG      ID = "B3"
	B4FullContext    ID = "B4"
	B5Cairn          ID = "B5"
)

// Surface distinguishes BUILD-PLAN §3.4 E9's two retrieval surfaces, which have
// different design intent and therefore different expectations:
//
//   - SurfaceSearch is the MEMORY. It is not allowed to forget.
//   - SurfaceDigest is the WORKING SET. It is allowed to forget, by design
//     (72-hour freshness half-life against search's 90 days).
//
// Measuring long-horizon recall through the digest would condemn the design
// for doing what it was built to do; measuring it only through search would
// let the digest off a hook it should be on. Backends declare which surfaces
// they have, and a backend without a surface returns ErrUnsupportedSurface
// rather than an empty result — a missing surface is a fact to record, not a
// zero to average in.
type Surface string

const (
	SurfaceSearch Surface = "search"
	SurfaceDigest Surface = "digest"
)

// Errors a caller is expected to branch on.
var (
	// ErrUnsupportedSurface: this backend has no such surface.
	ErrUnsupportedSurface = errors.New("backend does not implement this surface")
	// ErrNotImplemented: the backend is a declared stub (B3, B4).
	ErrNotImplemented = errors.New("backend not implemented")
)

// Item is one unit of knowledge written into a memory condition. It is the
// corpus's unit, not any backend's: ID is the ground-truth key that a
// retrieved hit is matched back to.
type Item struct {
	ID       string   // corpus item id — the ground-truth key
	Title    string   // short human title, if the corpus has one
	Body     string   // the content itself
	Topics   []string // topic labels, where the corpus supplies them
	Priority int      // 0-3, Cairn's declared-priority scale; 0 elsewhere
	// CreatedAt is the item's authorship time. Backends that can honour it
	// do (E9 replays a corpus chronologically); those that cannot record it
	// as metadata only. Zero means "now".
	CreatedAt time.Time
}

// WriteReceipt is what the backend stored the item as.
type WriteReceipt struct {
	ItemID   string // echo of Item.ID
	NativeID string // backend's own identifier (Cairn message id, note key…)
	Elapsed  time.Duration
}

// Request is one retrieval, identical across backends.
type Request struct {
	Surface     Surface
	Query       string
	K           int
	BudgetChars int
	View        string // agent view, for backends that have the concept
}

// Hit is one returned item.
type Hit struct {
	Rank     int
	ItemID   string  // resolved corpus item, "" when the backend cannot map back
	NativeID string  // backend's identifier for the hit
	Score    float64 // backend-native score; NOT comparable across backends
	Snippet  string
}

// Response is one retrieval's outcome. Payload is the text the agent would
// actually be handed — the thing a budget applies to — and is what E5's
// budget-survival measurement inspects.
type Response struct {
	Surface       Surface
	Hits          []Hit
	Payload       string
	Elapsed       time.Duration
	Partial       bool // the backend says its corpus view is incomplete
	PartialReason string
	// InteractionID and RetrievalMode are Cairn-specific provenance
	// (telemetry binding, full vs lexical_only). Empty for other backends.
	InteractionID string
	RetrievalMode string
	// Raw is the backend's unparsed output, kept so a recorded run can be
	// re-inspected for fields nobody thought to parse at the time.
	Raw string
}

// Capabilities is what a backend can do, declared rather than discovered.
type Capabilities struct {
	Surfaces []Surface
	// Chronological reports whether Write honours Item.CreatedAt. Without
	// it, E9's recall-over-age curve cannot be run on this backend.
	Chronological bool
	// Notes records the MODELLING CHOICES a baseline embodies. Baselines
	// are models of what people do; the model is an assumption and it
	// belongs in the run record next to the numbers it produced.
	Notes string
}

// Supports reports whether the backend declares a surface.
func (c Capabilities) Supports(s Surface) bool {
	for _, x := range c.Surfaces {
		if x == s {
			return true
		}
	}
	return false
}

// ArmConfig is a NATIVE ablation arm: a change to how the system under test is
// configured, rather than a post-hoc re-ranking of what it returned. The
// catalogue lives in internal/ablation; this is the part a backend must
// honour, kept here (as plain data, not an import) so backend stays the leaf
// package it is.
//
// A backend that cannot realize a non-default ArmConfig must REFUSE it at Open
// with ErrArmUnrealizable. Running the default condition and letting the caller
// label the record with the arm's name would fabricate a result — in whichever
// direction happened to suit — and it is exactly the failure mode the loudly
// failing B3/B4 stubs exist to prevent.
type ArmConfig struct {
	// ID names the arm, for error messages and records.
	ID string
	// Env is extra process environment for the system under test.
	Env []string
	// AddressToView writes items addressed to the evaluation view, which is
	// what confers the "recipient" mandatory inclusion class.
	AddressToView bool
}

// IsDefault reports whether the arm asks for nothing the shipped default does
// not already do.
func (a ArmConfig) IsDefault() bool { return len(a.Env) == 0 && !a.AddressToView }

// ErrArmUnrealizable: this backend cannot be configured into the requested arm.
var ErrArmUnrealizable = errors.New("backend cannot realize this ablation arm")

// Config is the uniform construction input.
type Config struct {
	// WorkDir is a directory the backend owns exclusively.
	WorkDir string
	// Binary is the cairn binary under test (B5 only).
	Binary cairnctl.Binary
	// Seed makes any backend-internal randomness reproducible. Nothing here
	// is random today; the field exists so that a backend which acquires
	// randomness later cannot become irreproducible quietly.
	Seed int64
	// Arm is the native ablation configuration. The zero value is the shipped
	// default, which every backend can realize.
	Arm ArmConfig
}

// Backend is the uniform memory condition.
type Backend interface {
	ID() ID
	Capabilities() Capabilities
	// Open provisions storage. It must be safe to call once per run.
	Open(ctx context.Context, cfg Config) error
	// Write stores one item.
	Write(ctx context.Context, item Item) (WriteReceipt, error)
	// Retrieve answers one request.
	Retrieve(ctx context.Context, req Request) (*Response, error)
	// Close releases everything Open acquired.
	Close(ctx context.Context) error
}

// Explainer is implemented by a backend that publishes its ranking arithmetic
// for a prior interaction. Only Cairn does; that asymmetry is not a harness
// convenience, it IS one of the claims (ENG-explainability), and E4's
// recomputed ablations are only possible because of it.
type Explainer interface {
	Explain(ctx context.Context, interactionID, messageID string) (string, error)
}

// New constructs a backend by id.
func New(id ID) (Backend, error) {
	switch id {
	case B0NoMemory:
		return &noMemory{}, nil
	case B1GrepTranscript:
		return &grepTranscripts{}, nil
	case B2FlatNotes:
		return &flatNotes{}, nil
	case B3VectorRAG:
		return &stub{id: B3VectorRAG, why: "naive vector-DB RAG needs an embedding model and a vector store; both arrive with the T1 tier"}, nil
	case B4FullContext:
		return &stub{id: B4FullContext, why: "full-context stuffing is only meaningful inside an agent loop with a real context window; it arrives with E5"}, nil
	case B5Cairn:
		return &cairnBackend{}, nil
	}
	return nil, fmt.Errorf("unknown backend %q", id)
}

// All returns every registered id in a stable order.
func All() []ID {
	ids := []ID{B0NoMemory, B1GrepTranscript, B2FlatNotes, B3VectorRAG, B4FullContext, B5Cairn}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	return ids
}
