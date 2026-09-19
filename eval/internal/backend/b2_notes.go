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

// flatNotes is B2: one append-only markdown notes file. This is what most
// people actually do, and it is the condition README's "flat files … fail at
// deciding what is actually relevant" is asserted against (claim
// PROD-beats-alternatives).
//
// MODELLING CHOICES (revisable by the operator before E4 runs):
//   - Every item becomes a `## <time> <title>` section appended to NOTES.md.
//   - Search is the same literal matching B1 uses, but ordered NEWEST FIRST:
//     someone reading their own notes starts at the bottom of the file.
//     That ordering is the whole of B2's "ranking", and it is a real
//     advantage over B1 on recency-sensitive questions.
//   - The digest surface is the TAIL of the file up to the budget — "read
//     the last page of my notes". It is not query-aware, which is precisely
//     the deficiency Cairn's ranked digest claims to fix, so B2 having a
//     digest surface at all is what makes that claim testable.
type flatNotes struct {
	path  string
	items []Item
}

const notesFileName = "NOTES.md"

func (b *flatNotes) ID() ID { return B2FlatNotes }

func (b *flatNotes) Capabilities() Capabilities {
	return Capabilities{
		Surfaces:      []Surface{SurfaceSearch, SurfaceDigest},
		Chronological: true,
		Notes: "append-only NOTES.md; literal substring match (all-terms, then any-term) newest-first; " +
			"digest = tail of the file up to budget, not query-aware",
	}
}

func (b *flatNotes) Open(_ context.Context, cfg Config) error {
	if cfg.WorkDir == "" {
		return errors.New("B2 requires a WorkDir")
	}
	if err := refuseArm(B2FlatNotes, cfg.Arm); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return err
	}
	b.path = filepath.Join(cfg.WorkDir, notesFileName)
	b.items = nil
	return os.WriteFile(b.path, []byte("# notes\n\n"), 0o600)
}

func (b *flatNotes) Write(_ context.Context, item Item) (WriteReceipt, error) {
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
	title := item.Title
	if title == "" {
		title = item.ID
	}
	entry := fmt.Sprintf("## %s — %s\n\n%s\n\n", stamp.UTC().Format(time.RFC3339), title, item.Body)
	if _, err := f.WriteString(entry); err != nil {
		return WriteReceipt{}, err
	}
	b.items = append(b.items, item)
	return WriteReceipt{ItemID: item.ID, NativeID: item.ID, Elapsed: time.Since(start)}, nil
}

func (b *flatNotes) Retrieve(_ context.Context, req Request) (*Response, error) {
	if !b.Capabilities().Supports(req.Surface) {
		return nil, ErrUnsupportedSurface
	}
	start := time.Now()
	if req.Surface == SurfaceDigest {
		resp := b.tail(req.BudgetChars)
		resp.Elapsed = time.Since(start)
		return resp, nil
	}

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
		resp.Hits = append(resp.Hits, Hit{Rank: n + 1, ItemID: it.ID, NativeID: it.ID, Snippet: snip})
		fmt.Fprintf(&payload, "%s: %s\n%s\n\n", it.ID, it.Title, snip)
	}
	resp.Payload, _ = truncateRunes(payload.String(), req.BudgetChars)
	resp.Elapsed = time.Since(start)
	return resp, nil
}

// scan walks newest-first.
func (b *flatNotes) scan(terms []string, pred func(string, []string) bool) []Item {
	var out []Item
	for i := len(b.items) - 1; i >= 0; i-- {
		it := b.items[i]
		if pred(strings.ToLower(it.Title+"\n"+it.Body), terms) {
			out = append(out, it)
		}
	}
	return out
}

// tail models "read the last page of my notes": the most recent items that
// fit the budget, newest first, with no regard for what was asked.
func (b *flatNotes) tail(budget int) *Response {
	if budget <= 0 {
		budget = tunables.DefaultBudgetChars
	}
	resp := &Response{Surface: SurfaceDigest}
	var payload strings.Builder
	for i := len(b.items) - 1; i >= 0; i-- {
		it := b.items[i]
		chunk := fmt.Sprintf("%s: %s\n%s\n\n", it.ID, it.Title, it.Body)
		if len([]rune(payload.String()))+len([]rune(chunk)) > budget {
			break
		}
		payload.WriteString(chunk)
		resp.Hits = append(resp.Hits, Hit{Rank: len(resp.Hits) + 1, ItemID: it.ID, NativeID: it.ID})
	}
	resp.Payload, _ = truncateRunes(payload.String(), budget)
	return resp
}

func (b *flatNotes) Close(context.Context) error { return nil }

var _ Backend = (*flatNotes)(nil)
