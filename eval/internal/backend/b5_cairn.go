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
	mcp  *cairnctl.MCPSession
	view string
	arm  ArmConfig

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
		Notes: "black-box: `cairn send`/`cairn search` over the CLI and cairn_digest over MCP " +
			"(the agent surface, and the only one that returns the digest's interaction id) " +
			"against a throwaway mesh; semantic search is opt-in (retrieval_mode reports full vs " +
			"lexical_only); the digest is generated after declaring the query as the view's LOCAL " +
			"standing interest, because a view without one has no relevance component at all",
	}
}

func (b *cairnBackend) Open(ctx context.Context, cfg Config) error {
	if cfg.WorkDir == "" {
		return errors.New("B5 requires a WorkDir")
	}
	// A native ablation arm is applied as process environment for the whole
	// instance — provisioning, daemon and every verb. One environment rather
	// than two that could drift, and it means the arm is in force for
	// enrichment as well as for retrieval.
	inst, err := cairnctl.Provision(ctx, cairnctl.Options{
		Binary: cfg.Binary, Root: cfg.WorkDir, ExtraEnv: cfg.Arm.Env,
	})
	if err != nil {
		return err
	}
	if err := inst.StartDaemon(ctx); err != nil {
		_ = inst.Close()
		return err
	}
	// The MCP session is the DIGEST surface (see Retrieve). Its failure is a
	// hard error rather than a fallback to `cairn digest`: falling back would
	// silently drop the interaction id, and an ablation arm that quietly lost
	// its ranking arithmetic would score as "no effect" — which is a result
	// somebody would report.
	sess, err := inst.StartMCP(ctx, EvalView, "")
	if err != nil {
		_ = inst.Close()
		return fmt.Errorf("B5 needs the MCP surface for the digest's interaction id: %w", err)
	}
	b.inst = inst
	b.mcp = sess
	b.view = EvalView
	b.arm = cfg.Arm
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
	opts := cairnctl.SendOptions{
		Body:     body,
		Topics:   topics,
		Priority: item.Priority,
		Actor:    "eval-harness",
	}
	// The mandatory-inclusion arm is a WRITE-side change: addressing an item to
	// the view is what gives it the "recipient" inclusion class, which sorts
	// ahead of score entirely in the digest. There is no query-side switch for
	// it, so the arm has to be a different corpus load.
	if b.arm.AddressToView {
		opts.View = b.view
	}
	res, err := b.inst.Send(ctx, opts)
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
		if view != b.view {
			// The MCP server's view is fixed at launch; asking for another one
			// here would return the launch view's digest under the requested
			// view's name.
			return nil, fmt.Errorf("B5's digest surface is bound to view %q (the MCP session's), not %q", b.view, view)
		}
		// The digest is NOT query-driven: it ranks against the view's standing
		// interest, not against whatever was just asked. A view with no
		// interest query gets a uniform relevance component (R = 1.0 for every
		// candidate), so its digest is ordered by freshness and priority
		// alone — and any relevance ablation measured on it would be measuring
		// a surface with no relevance in it.
		//
		// So the harness does what CLAUDE.md tells agents to do: declares the
		// question as a LOCAL standing interest (R25 session tier — no events,
		// own view only) before generating the digest. That is the digest an
		// agent who has shaped its own view actually receives, and it is the
		// only version of this surface on which E4's ablations mean anything.
		if req.Query != "" {
			if err := b.inst.Subscribe(ctx, b.view, req.Query); err != nil {
				return nil, fmt.Errorf("declaring the digest's standing interest: %w", err)
			}
		}
		env, err := b.mcp.DigestViaMCP(ctx, budget)
		if err != nil {
			return nil, err
		}
		return &Response{
			Surface:       SurfaceDigest,
			Payload:       env.Text(),
			Raw:           env.Raw,
			Hits:          b.hitsFromDigest(env.Text()),
			Partial:       env.Partial,
			PartialReason: env.PartialReason,
			InteractionID: env.InteractionID,
			RetrievalMode: env.RetrievalMode,
			Elapsed:       time.Since(start),
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

// hitsFromDigest recovers corpus item ids AND cairn message ids from a digest
// payload. The digest is markdown for a human/agent, not a machine surface, so
// this reads back the marker Write embedded rather than pretending to parse the
// format — but it also walks the numbered entry headers, because the message id
// is what `cairn why-ranked` is keyed on and E4's recomputed ablations need it.
//
// An entry looks like:
//
//  3. <message-id> [recipient] score=0.41
//     from eval-harness · 2026-08-16T… · eval/corpus
//     > [CAIRN] [corpus-item s-004] title…
//
// The corpus marker is attributed to the most recent header seen. If either
// half is missing the hit still records what it has: an unmapped hit occupies
// its rank slot as a non-relevant result, which is exactly what it is to the
// agent.
func (b *cairnBackend) hitsFromDigest(payload string) []Hit {
	var hits []Hit
	var pending *Hit
	flush := func() {
		if pending != nil {
			pending.Rank = len(hits) + 1
			hits = append(hits, *pending)
			pending = nil
		}
	}
	for _, line := range strings.Split(payload, "\n") {
		if id, ok := digestEntryHeader(line); ok {
			flush()
			pending = &Hit{NativeID: id, ItemID: b.byMessage[id]}
			continue
		}
		if i := strings.Index(line, "[corpus-item "); i >= 0 && pending != nil && pending.ItemID == "" {
			rest := line[i+len("[corpus-item "):]
			if j := strings.IndexAny(rest, "] \n"); j >= 0 {
				pending.ItemID = rest[:j]
			}
		}
	}
	flush()
	return hits
}

// digestEntryHeader matches "<n>. <message-id> …" and returns the message id.
func digestEntryHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	dot := strings.Index(trimmed, ". ")
	if dot <= 0 {
		return "", false
	}
	for _, r := range trimmed[:dot] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	fields := strings.Fields(trimmed[dot+2:])
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// Explain returns the parsed ranking arithmetic for one result of a prior
// interaction. It is B5-only by nature: no baseline has published arithmetic,
// which is itself part of what the explainability claim asserts.
func (b *cairnBackend) Explain(ctx context.Context, interactionID, messageID string) (string, error) {
	if b.inst == nil {
		return "", errors.New("B5: Open first")
	}
	return b.inst.WhyRanked(ctx, interactionID, messageID)
}

func (b *cairnBackend) Close(context.Context) error {
	if b.inst == nil {
		return nil
	}
	if b.mcp != nil {
		_ = b.mcp.Close()
		b.mcp = nil
	}
	err := b.inst.Close()
	b.inst = nil
	return err
}

var _ Backend = (*cairnBackend)(nil)
