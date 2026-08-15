package cairnctl

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// The structs below mirror the JSON the CLI prints. They are deliberate
// duplicates of the daemon's output types, not shared code — see the package
// comment. Only the fields the harness actually uses are declared; the raw
// JSON is kept alongside every call so a result file can be re-read later
// for fields nobody thought to parse at the time.

// PublishResult is `cairn send` / `cairn reply` output.
type PublishResult struct {
	EventIDs   []string `json:"event_ids"`
	MessageID  string   `json:"message_id"`
	RevisionID string   `json:"revision_id"`
	BodyHash   string   `json:"body_hash"`
	TextClass  string   `json:"text_class"`
	Downgraded bool     `json:"downgraded,omitempty"`
	Raw        string   `json:"-"`
}

// SearchResult is `cairn search` output.
type SearchResult struct {
	InteractionID string       `json:"interaction_id"`
	RetrievalMode string       `json:"retrieval_mode"` // full | lexical_only
	Results       []RankedItem `json:"results"`
	Payload       string       `json:"payload"`
	Omitted       int          `json:"omitted,omitempty"`
	Partial       bool         `json:"partial,omitempty"`
	PartialReason string       `json:"partial_reason,omitempty"`
	RemoteSource  string       `json:"remote_source,omitempty"`
	Raw           string       `json:"-"`
}

// RankedItem is one scored hit. Snippet, sender and topics are MESH CONTENT:
// untrusted data, never instructions (R18/R53). The harness treats them as
// opaque strings and must never interpolate them into an agent prompt except
// through a condition that is deliberately measuring exactly that (E6).
type RankedItem struct {
	Rank       int      `json:"rank"`
	MessageID  string   `json:"message_id"`
	RevisionID string   `json:"revision_id"`
	BodyHash   string   `json:"body_hash"`
	TextClass  string   `json:"text_class"`
	Score      float64  `json:"score"`
	Mandatory  string   `json:"mandatory,omitempty"`
	Sender     string   `json:"sender,omitempty"`
	Created    string   `json:"created,omitempty"`
	Topics     []string `json:"topics,omitempty"`
	Snippet    string   `json:"snippet,omitempty"`
}

// FetchResult is `cairn fetch` output.
type FetchResult struct {
	MessageID    string `json:"message_id"`
	RevisionID   string `json:"revision_id"`
	BodyHash     string `json:"body_hash"`
	SourceEvent  string `json:"source_event_id"`
	Trust        string `json:"trust"`
	Retracted    bool   `json:"retracted,omitempty"`
	ManifestPath string `json:"manifest_path"`
	BodyPath     string `json:"body_path"`
	Expired      bool   `json:"content_expired,omitempty"`
	NotDelivered bool   `json:"ephemeral_not_delivered,omitempty"`
	Raw          string `json:"-"`
}

// MessageInfo is `cairn peek` output. Field set is intentionally minimal:
// the harness reads created_at for E9's recall-over-age curves.
type MessageInfo struct {
	MessageID      string `json:"message_id"`
	HeadRevisionID string `json:"head_revision_id"`
	BodyHash       string `json:"body_hash"`
	Sender         string `json:"sender,omitempty"`
	CreatedAt      string `json:"created_at"`
	TextClass      string `json:"text_class"`
	Priority       int    `json:"declared_priority"`
	ThreadID       string `json:"thread_id,omitempty"`
	Retracted      bool   `json:"retracted"`
	Raw            string `json:"-"`
}

// SendOptions are the `cairn send` flags the harness uses.
type SendOptions struct {
	Body     string
	Topics   []string
	Priority int
	View     string // --to recipient view (mandatory-inclusion experiments)
	Summary  string
	Thread   string
	Actor    string
	TaskID   string
}

// Send publishes one message. The body goes over stdin, not argv: corpus
// items routinely exceed ARG_MAX and contain shell-hostile bytes.
func (i *Instance) Send(ctx context.Context, opts SendOptions) (*PublishResult, error) {
	args := []string{"send", "-"}
	for _, t := range opts.Topics {
		args = append(args, "--topic", t)
	}
	if opts.Priority != 0 {
		args = append(args, "--priority", strconv.Itoa(opts.Priority))
	}
	if opts.View != "" {
		args = append(args, "--to", opts.View)
	}
	if opts.Summary != "" {
		args = append(args, "--summary", opts.Summary)
	}
	if opts.Thread != "" {
		args = append(args, "--thread", opts.Thread)
	}
	if opts.Actor != "" {
		args = append(args, "--actor", opts.Actor)
	}
	if opts.TaskID != "" {
		args = append(args, "--task-id", opts.TaskID)
	}
	out, err := i.RunStdin(ctx, []byte(opts.Body), args...)
	if err != nil {
		return nil, err
	}
	var res PublishResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("parsing send output: %w\n%s", err, out)
	}
	res.Raw = out
	return &res, nil
}

// SearchOptions are the `cairn search` flags the harness uses.
type SearchOptions struct {
	Query       string
	K           int
	BudgetChars int
	Topics      []string
	Sender      string
	Thread      string
}

// Search runs the hybrid search surface — BUILD-PLAN §3.4 E9's MEMORY surface,
// the one that is not allowed to forget.
func (i *Instance) Search(ctx context.Context, opts SearchOptions) (*SearchResult, error) {
	args := []string{"search", opts.Query}
	if opts.K > 0 {
		args = append(args, "--k", strconv.Itoa(opts.K))
	}
	if opts.BudgetChars > 0 {
		args = append(args, "--budget", strconv.Itoa(opts.BudgetChars))
	}
	for _, t := range opts.Topics {
		args = append(args, "--topic", t)
	}
	if opts.Sender != "" {
		args = append(args, "--sender", opts.Sender)
	}
	if opts.Thread != "" {
		args = append(args, "--thread", opts.Thread)
	}
	out, err := i.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var res SearchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("parsing search output: %w\n%s", err, out)
	}
	res.Raw = out
	return &res, nil
}

// DigestResult is what `cairn digest` writes to stdout. The CLI prints the
// payload text, not JSON, so unlike the other verbs there is no structured
// envelope to parse — the payload IS the result.
type DigestResult struct {
	Payload string
	Raw     string
}

// Digest generates the ranked, budget-capped digest for a view — BUILD-PLAN
// §9.1's WORKING-SET surface, which is allowed to forget.
func (i *Instance) Digest(ctx context.Context, view string, budgetChars int) (*DigestResult, error) {
	if view == "" {
		view = "operator"
	}
	if budgetChars <= 0 {
		return nil, fmt.Errorf("digest requires budget_chars > 0")
	}
	out, err := i.Run(ctx, "digest", "--view", view, "--budget", strconv.Itoa(budgetChars))
	if err != nil {
		return nil, err
	}
	return &DigestResult{Payload: out, Raw: out}, nil
}

// Fetch materializes one message body with provenance.
func (i *Instance) Fetch(ctx context.Context, messageID, view string) (*FetchResult, error) {
	args := []string{"fetch", messageID}
	if view != "" {
		args = append(args, "--view", view)
	}
	out, err := i.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var res FetchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("parsing fetch output: %w\n%s", err, out)
	}
	res.Raw = out
	return &res, nil
}

// Peek returns one message's metadata without its body.
func (i *Instance) Peek(ctx context.Context, messageID string) (*MessageInfo, error) {
	out, err := i.Run(ctx, "peek", messageID)
	if err != nil {
		return nil, err
	}
	var res MessageInfo
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("parsing peek output: %w\n%s", err, out)
	}
	res.Raw = out
	return &res, nil
}

// Outcome records a retrieval outcome (found | not_found | manual_workaround)
// against an interaction, binding it to a message where the verb allows.
// E7's telemetry substrate runs through here.
func (i *Instance) Outcome(ctx context.Context, kind, interactionID, messageID string) error {
	args := []string{kind, interactionID}
	if messageID != "" {
		args = append(args, "--message", messageID)
	}
	_, err := i.Run(ctx, args...)
	return err
}

// Status returns the daemon's one-shot health JSON.
func (i *Instance) Status(ctx context.Context) (map[string]any, error) {
	out, err := i.Run(ctx, "status", "--json")
	if err != nil {
		return nil, err
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("parsing status output: %w\n%s", err, out)
	}
	return res, nil
}

// Subscribe declares a LOCAL standing interest for a view (R25 session tier;
// never --durable, which is the operator-only replicated tier).
func (i *Instance) Subscribe(ctx context.Context, view, interest string) error {
	_, err := i.Run(ctx, "subscribe", interest, "--view", view)
	return err
}
