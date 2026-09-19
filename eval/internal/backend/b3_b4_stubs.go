package backend

import (
	"context"
	"fmt"
)

// stub is a DECLARED, not accidental, gap: B3 (naive vector-DB RAG) and B4
// (full-context stuffing) are part of BUILD-PLAN §5-E4's baseline set but
// neither can be built honestly in the T0 tier.
//
//   - B3 needs an embedding model and a vector store. Building it with
//     Cairn's own embedder would make it a Cairn ablation wearing a
//     baseline's name — the comparison would be rigged in a way nobody
//     could see from the numbers. It belongs in T1, with its own model
//     choice stated.
//   - B4 only means something inside an agent loop with a real context
//     window: "does budget discipline matter below the context limit"
//     cannot be answered by a retrieval function. It belongs in E5.
//
// They fail loudly rather than returning empty results, because an empty
// result from a baseline that was never implemented would silently become a
// zero in someone's table, and a fabricated zero in favour of the project
// is exactly the failure mode this whole framework exists to prevent.
type stub struct {
	id  ID
	why string
}

func (b *stub) ID() ID { return b.id }

// IsStub reports whether a backend is a declared gap rather than a working
// condition. Callers use it to say so out loud; nothing may use it to
// substitute a default result for a missing baseline.
func IsStub(b Backend) bool {
	_, ok := b.(*stub)
	return ok
}

func (b *stub) Capabilities() Capabilities {
	return Capabilities{Notes: "NOT IMPLEMENTED — " + b.why}
}

func (b *stub) err() error { return fmt.Errorf("%w: %s: %s", ErrNotImplemented, b.id, b.why) }

func (b *stub) Open(context.Context, Config) error { return b.err() }

func (b *stub) Write(context.Context, Item) (WriteReceipt, error) { return WriteReceipt{}, b.err() }

func (b *stub) Retrieve(context.Context, Request) (*Response, error) { return nil, b.err() }

func (b *stub) Close(context.Context) error { return nil }

var _ Backend = (*stub)(nil)
