package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ggoosen/cairn/eval/internal/cairnctl"
	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// cairnBackend is B5: Cairn itself, driven as a black box through the CLI —
// a throwaway mesh provisioned per run, torn down after.
//
// It has no privileged access of any kind. It sends with `cairn send`,
// searches with `cairn search`, digests with `cairn digest`, and reads only
// what those verbs print. That is the same surface B1 and B2 get, and the
// same surface an agent gets.
//
// One asymmetry is unavoidable and must be stated rather than hidden: Cairn
// is the only backend here that IS a daemon, so its Elapsed includes process
// start, IPC and fsync'd durability, while B1/B2 measure an in-process
// scan. Latency across backends is therefore not comparable and must not be
// reported as if it were; retrieval QUALITY is what this interface exists to
// compare.
type cairnBackend struct {
	inst *cairnctl.Instance
	view string

	// byMessage maps a Cairn message id back to the corpus item id, so a
	// hit can be scored against ground truth the harness owns.
	byMessage map[string]string
}

// EvalView is the agent view B5 writes to and reads from. A dedicated view
// (not "operator") keeps the evaluation's digest surface separate from the
// operator's own.
const EvalView = "eval"

func (b *cairnBackend) ID() ID { return B5Cairn }

func (b *cairnBackend) Capabilities() Capabilities {
	return Capabilities{
		Surfaces:      []Surface{SurfaceSearch, SurfaceDigest},
		Chronological: false, // true only with the E9 clock hook; see Instance options
		Notes: "black-box CLI: send / search / digest against a throwaway mesh; " +
			"semantic search is opt-in (retrieval_mode reports full vs lexical_only)",
	}
}

func (b *cairnBackend) Open(ctx context.Context, cfg Config) error {
	if cfg.WorkDir == "" {
		return errors.New("B5 requires a WorkDir")
	}
	inst, err := cairnctl.Provision(ctx, cairnctl.Options{Binary: cfg.Binary, Root: cfg.WorkDir})
	if err != nil {
		return err
	}
	if err := inst.StartDaemon(ctx); err != nil {
		_ = inst.Close()
		return err
	}
	b.inst = inst
	b.view = EvalView
	b.byMessage = map[string]string{}
	return nil
}

// Instance exposes the provisioned cairn so a caller can record its version
// or drive verbs this interface deliberately does not model. It is nil
// before Open.
func (b *cairnBackend) Instance() *cairnctl.Instance { return b.inst }

func (b *cairnBackend) Write(ctx context.Context, item Item) (WriteReceipt, error) {
	if b.inst == nil {
		return WriteReceipt{}, errors.New("B5: Open first")
	}
	start := time.Now()
	topics := item.Topics
	if len(topics) == 0 {
		topics = []string{"eval/corpus"}
	}
	// The corpus item id travels in the body's first line. Cairn assigns its
	// own ids and the harness must be able to map a hit back to ground
	// truth; the alternative — a side table keyed on body hash — breaks the
	// moment a corpus contains two identical bodies, which real corpora do.
	body := fmt.Sprintf("[corpus-item %s] %s\n\n%s", item.ID, item.Title, item.Body)
	res, err := b.inst.Send(ctx, cairnctl.SendOptions{
		Body:     body,
		Topics:   topics,
		Priority: item.Priority,
		Actor:    "eval-harness",
	})
	if err != nil {
		return WriteReceipt{}, err
	}
	b.byMessage[res.MessageID] = item.ID
	return WriteReceipt{ItemID: item.ID, NativeID: res.MessageID, Elapsed: time.Since(start)}, nil
}

func (b *cairnBackend) Retrieve(ctx context.Context, req Request) (*Response, error) {
	if b.inst == nil {
		return nil, errors.New("B5: Open first")
	}
	if !b.Capabilities().Supports(req.Surface) {
		return nil, ErrUnsupportedSurface
	}
	view := req.View
	if view == "" {
		view = b.view
	}
	start := time.Now()

	if req.Surface == SurfaceDigest {
		budget := req.BudgetChars
		if budget <= 0 {
			budget = tunables.DefaultBudgetChars
		}
		out, err := b.inst.Digest(ctx, view, budget)
		if err != nil {
			return nil, err
		}
		return &Response{
			Surface: SurfaceDigest,
			Payload: out.Payload,
			Raw:     out.Raw,
			Hits:    b.hitsFromDigest(out.Payload),
			Elapsed: time.Since(start),
		}, nil
	}

	k := req.K
	if k <= 0 {
		k = tunables.DefaultK
	}
	res, err := b.inst.Search(ctx, cairnctl.SearchOptions{
		Query: req.Query, K: k, BudgetChars: req.BudgetChars,
	})
	if err != nil {
		return nil, err
	}
	resp := &Response{
		Surface:       SurfaceSearch,
		Payload:       res.Payload,
		Raw:           res.Raw,
		Partial:       res.Partial,
		PartialReason: res.PartialReason,
		InteractionID: res.InteractionID,
		RetrievalMode: res.RetrievalMode,
		Elapsed:       time.Since(start),
	}
	for _, r := range res.Results {
		resp.Hits = append(resp.Hits, Hit{
			Rank:     r.Rank,
			ItemID:   b.byMessage[r.MessageID],
			NativeID: r.MessageID,
			Score:    r.Score,
			Snippet:  r.Snippet,
		})
	}
	return resp, nil
}

// hitsFromDigest recovers corpus item ids from a digest payload. The digest
// is markdown for a human/agent, not a machine surface, so this reads back
// the marker Write embedded rather than pretending to parse the format.
func (b *cairnBackend) hitsFromDigest(payload string) []Hit {
	var hits []Hit
	rest := payload
	for {
		i := strings.Index(rest, "[corpus-item ")
		if i < 0 {
			return hits
		}
		rest = rest[i+len("[corpus-item "):]
		j := strings.IndexAny(rest, "] \n")
		if j < 0 {
			return hits
		}
		hits = append(hits, Hit{Rank: len(hits) + 1, ItemID: rest[:j]})
		rest = rest[j:]
	}
}

func (b *cairnBackend) Close(context.Context) error {
	if b.inst == nil {
		return nil
	}
	err := b.inst.Close()
	b.inst = nil
	return err
}

var _ Backend = (*cairnBackend)(nil)
