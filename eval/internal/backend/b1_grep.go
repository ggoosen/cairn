package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// grepTranscripts is B1: raw session transcripts on disk, searched literally.
//
// This is THE baseline to beat (EVAL-PLAN §7): it is roughly what shipping
// agent harnesses do today, it costs nothing to build, and if Cairn's
// ranking and curation layer cannot beat it then that layer is not earning
// its complexity. The kill criterion attached to it is the most important
// row in eval/claims.yaml.
//
// MODELLING CHOICES (all recorded in Capabilities.Notes, all revisable — by
// the operator, before E4 runs, not after seeing results):
//   - Each written item is appended to a single transcript file in write
//     order, the way a session log accumulates.
//   - Retrieval is literal substring matching over items: first all query
//     terms (a phrase-ish grep), and only if that returns nothing, any term
//     (the second thing a person types).
//   - Results come back in FILE ORDER — oldest first. There is no ranking,
//     because grep has none. That is the baseline's actual weakness and
//     hiding it would flatter Cairn.
//   - There is no digest surface. Transcripts do not summarize themselves.
//
// The transcript FILE is the durable artifact (kept for inspection after a
// run); the in-memory slice mirrors it exactly and is what the scan walks,
// so a run is deterministic and does not depend on a platform grep.
type grepTranscripts struct {
	path  string
	items []Item
}

const transcriptFileName = "transcript.log"

func (b *grepTranscripts) ID() ID { return B1GrepTranscript }

func (b *grepTranscripts) Capabilities() Capabilities {
	return Capabilities{
		Surfaces:      []Surface{SurfaceSearch},
		Chronological: true,
		Notes: "append-only transcript; literal substring match (all-terms, then any-term); " +
			"results in file order, no ranking; no digest surface",
	}
}

func (b *grepTranscripts) Open(_ context.Context, cfg Config) error {
	if cfg.WorkDir == "" {
		return errors.New("B1 requires a WorkDir")
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return err
	}
	b.path = filepath.Join(cfg.WorkDir, transcriptFileName)
	b.items = nil
	// Truncate: a backend Open is the start of a run, and a stale transcript
	// from an earlier run would silently become part of this one's corpus.
	return os.WriteFile(b.path, nil, 0o600)
}

func (b *grepTranscripts) Write(_ context.Context, item Item) (WriteReceipt, error) {
	start := time.Now()
	f, err := os.OpenFile(b.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return WriteReceipt{}, err
	}
	defer f.Close()
	stamp := item.CreatedAt
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	entry := fmt.Sprintf("[%s] item=%s %s\n%s\n\n",
		stamp.UTC().Format(time.RFC3339), item.ID, item.Title, item.Body)
	if _, err := f.WriteString(entry); err != nil {
		return WriteReceipt{}, err
	}
	b.items = append(b.items, item)
	return WriteReceipt{ItemID: item.ID, NativeID: item.ID, Elapsed: time.Since(start)}, nil
}

func (b *grepTranscripts) Retrieve(_ context.Context, req Request) (*Response, error) {
	if !b.Capabilities().Supports(req.Surface) {
		return nil, ErrUnsupportedSurface
	}
	start := time.Now()
	terms := tokenize(req.Query)
	k := req.K
	if k <= 0 {
		k = tunables.DefaultK
	}

	matched := b.scan(terms, containsAll)
	if len(matched) == 0 {
		matched = b.scan(terms, containsAny)
	}
	if len(matched) > k {
		matched = matched[:k]
	}

	resp := &Response{Surface: req.Surface}
	var payload strings.Builder
	for n, it := range matched {
		snip := snippet(it.Title+"\n"+it.Body, terms, tunables.GrepContextChars)
		resp.Hits = append(resp.Hits, Hit{
			Rank: n + 1, ItemID: it.ID, NativeID: it.ID, Snippet: snip,
		})
		fmt.Fprintf(&payload, "%s: %s\n%s\n\n", it.ID, it.Title, snip)
	}
	resp.Payload, _ = truncateRunes(payload.String(), req.BudgetChars)
	resp.Elapsed = time.Since(start)
	return resp, nil
}

func (b *grepTranscripts) scan(terms []string, pred func(string, []string) bool) []Item {
	var out []Item
	for _, it := range b.items {
		if pred(strings.ToLower(it.Title+"\n"+it.Body), terms) {
			out = append(out, it)
		}
	}
	return out
}

func (b *grepTranscripts) Close(context.Context) error { return nil }

var _ Backend = (*grepTranscripts)(nil)
