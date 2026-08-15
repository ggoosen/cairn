package backend

import "context"

// noMemory is B0: the cold agent. Writes are accepted and discarded;
// retrieval returns nothing.
//
// It is the control condition, and it is the reason the rest of the
// framework can say anything at all. Without a no-memory arm there is no
// counterfactual, and without a counterfactual there is no claim — only a
// description (EVAL-PLAN §2.1).
type noMemory struct{ writes int }

func (b *noMemory) ID() ID { return B0NoMemory }

func (b *noMemory) Capabilities() Capabilities {
	return Capabilities{
		Surfaces:      []Surface{SurfaceSearch, SurfaceDigest},
		Chronological: true, // trivially: nothing is stored, so nothing decays
		Notes:         "control condition: accepts writes, stores nothing, returns nothing",
	}
}

func (b *noMemory) Open(context.Context, Config) error { return nil }

func (b *noMemory) Write(_ context.Context, item Item) (WriteReceipt, error) {
	b.writes++
	return WriteReceipt{ItemID: item.ID}, nil
}

func (b *noMemory) Retrieve(_ context.Context, req Request) (*Response, error) {
	if !b.Capabilities().Supports(req.Surface) {
		return nil, ErrUnsupportedSurface
	}
	return &Response{Surface: req.Surface, Elapsed: 0}, nil
}

func (b *noMemory) Close(context.Context) error { return nil }

var _ Backend = (*noMemory)(nil)
