package daemon_test

// D4 — `budget_tokens` at the IPC boundary, which is the surface the CLI and
// MCP both go through. The renderer-level property (every renderer, both
// modes, hard budget) lives in retrieve_test.go; what is asserted here is the
// contract a CALLER sees: exactly one budget, a named tokenizer in every
// response, and the D3 capability cap composing with a token budget without
// anybody inventing a characters-per-token exchange rate.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/rank"
)

// bothBudgetsOps enumerates every op that accepts a budget, so a new budgeted
// op cannot quietly skip the refusal.
func bothBudgetsRequests(threadID string) map[string]daemon.Request {
	return map[string]daemon.Request{
		"digest": {Op: "digest", AgentView: "operator", BudgetChars: 500, BudgetTokens: 500},
		"thread": {Op: "thread", ThreadID: threadID, BudgetChars: 500, BudgetTokens: 500},
		"search": {Op: "search", Search2: &daemon.SearchOptions{
			Query: "widgets", BudgetChars: 500, BudgetTokens: 500}},
	}
}

func setupBudgetFixture(t *testing.T) (func(daemon.Request) (*daemon.Response, error), string) {
	t.Helper()
	dir := initCairn(t)
	callD := serveSession(t, dir, nil)
	var root string
	for i := 0; i < 6; i++ {
		resp, err := callD(daemon.Request{Op: "publish", Publish: &daemon.PublishRequest{
			Actor: "operator",
			Body: "a note about widgets and their assembly, long enough to cost " +
				"a measurable number of tokens when counted by any tokenizer at all",
			ReplyToMessageID: root,
		}})
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
		if root == "" {
			root = resp.Publish.MessageID
		}
	}
	return callD, root
}

// D4 acceptance: a request carrying BOTH budgets is refused, not silently
// resolved by a precedence rule the caller cannot see.
func TestD4BothBudgetsAreRefused(t *testing.T) {
	callD, root := setupBudgetFixture(t)
	for name, req := range bothBudgetsRequests(root) {
		resp, err := callD(req)
		if err == nil {
			t.Fatalf("%s accepted budget_chars AND budget_tokens together: %+v", name, resp)
		}
		if !strings.Contains(err.Error(), "budget_tokens") || !strings.Contains(err.Error(), "budget_chars") {
			t.Fatalf("%s: the refusal must name both budgets so the caller can fix it, got %q", name, err)
		}
	}
}

// D4 acceptance: the response NAMES the tokenizer and the mode, on every
// budgeted op, in both modes. A token count against an unnamed tokenizer is
// not a measurement.
func TestD4ResponseNamesTokenizerAndMode(t *testing.T) {
	callD, root := setupBudgetFixture(t)

	type call struct {
		name string
		run  func(chars, tokens int) (string, rank.Report, error)
	}
	calls := []call{
		{"digest", func(c, tk int) (string, rank.Report, error) {
			r, err := callD(daemon.Request{Op: "digest", AgentView: "operator", BudgetChars: c, BudgetTokens: tk})
			if err != nil {
				return "", rank.Report{}, err
			}
			return r.Digest.Payload, r.Digest.Budget, nil
		}},
		{"search", func(c, tk int) (string, rank.Report, error) {
			r, err := callD(daemon.Request{Op: "search", Search2: &daemon.SearchOptions{
				Query: "widgets", K: 20, BudgetChars: c, BudgetTokens: tk}})
			if err != nil {
				return "", rank.Report{}, err
			}
			return r.Search.Payload, r.Search.Budget, nil
		}},
		{"thread", func(c, tk int) (string, rank.Report, error) {
			r, err := callD(daemon.Request{Op: "thread", ThreadID: root, BudgetChars: c, BudgetTokens: tk})
			if err != nil {
				return "", rank.Report{}, err
			}
			return r.Thread.Payload, r.Thread.Budget, nil
		}},
	}

	for _, c := range calls {
		payload, rep, err := c.run(0, 120)
		if err != nil {
			t.Fatalf("%s token-budgeted: %v", c.name, err)
		}
		if rep.Mode != rank.BudgetModeTokens {
			t.Fatalf("%s reported mode %q for a token budget", c.name, rep.Mode)
		}
		if rep.Tokenizer != config.TokenizerApprox {
			t.Fatalf("%s reported tokenizer %q, want %q", c.name, rep.Tokenizer, config.TokenizerApprox)
		}
		if !rep.Approximate {
			t.Fatalf("%s did not flag the approximate tokenizer as approximate", c.name)
		}
		if !strings.Contains(rep.Tokenizer, "approx") {
			t.Fatalf("%s: an approximate counter must say so in its NAME, got %q", c.name, rep.Tokenizer)
		}
		if got := rank.ApproxTokenCounter().Count(payload); got > 120 || rep.Used != got {
			t.Fatalf("%s: payload costs %d tokens (reported %d), budget 120", c.name, got, rep.Used)
		}

		payload, rep, err = c.run(400, 0)
		if err != nil {
			t.Fatalf("%s char-budgeted: %v", c.name, err)
		}
		if rep.Mode != rank.BudgetModeChars || rep.Tokenizer != config.TokenizerChars || rep.Approximate {
			t.Fatalf("%s char-budget report is wrong: %+v", c.name, rep)
		}
		if n := utf8.RuneCountInString(payload); n > 400 || rep.Used != n {
			t.Fatalf("%s: payload is %d chars (reported %d), budget 400", c.name, n, rep.Used)
		}
	}
}

// D4 × D3: a session capped in CHARACTERS may still budget in TOKENS. The cap
// is applied as a second hard limit, not converted — and it is reported,
// because a limit the agent cannot see is a limit it cannot report to a human.
func TestD4TokenBudgetUnderACharacterCap(t *testing.T) {
	f := setupConfineFixture(t)
	token := mkConfined(t, f.call, daemon.Selectors{MaxBudgetChars: 200})

	resp, err := f.call(daemon.Request{Op: "digest", Session: token,
		AgentView: "narrow", BudgetTokens: 5000})
	if err != nil {
		t.Fatalf("confined token-budgeted digest: %v", err)
	}
	if n := utf8.RuneCountInString(resp.Digest.Payload); n > 200 {
		t.Fatalf("the session's character cap was ignored under a token budget: %d chars", n)
	}
	if resp.Digest.Budget.Mode != rank.BudgetModeTokens || resp.Digest.Budget.Limit != 5000 {
		t.Fatalf("the caller's token budget was rewritten: %+v", resp.Digest.Budget)
	}
	if resp.Digest.Budget.CeilingChars != 200 {
		t.Fatalf("the character ceiling is not reported in the budget: %+v", resp.Digest.Budget)
	}
	if resp.Capability == nil || resp.Capability.BudgetCeilingChars != 200 {
		t.Fatalf("the ceiling was applied SILENTLY: %+v", resp.Capability)
	}
	// and it is NOT reported as a clamp: nothing was converted, so claiming a
	// budget_chars clamp would misdescribe what happened.
	if resp.Capability.BudgetClamped {
		t.Fatal("a token budget must not be reported as a budget_chars clamp")
	}
}

// D4: the hard-budget property is unchanged — an oversized item is dropped
// WHOLE, never truncated mid-item, in token mode as in char mode.
func TestD4OversizedItemsAreDroppedWhole(t *testing.T) {
	callD, root := setupBudgetFixture(t)
	resp, err := callD(daemon.Request{Op: "thread", ThreadID: root, BudgetTokens: 40})
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	full, err := callD(daemon.Request{Op: "thread", ThreadID: root})
	if err != nil {
		t.Fatalf("unbudgeted thread: %v", err)
	}
	if resp.Thread.Omitted == 0 {
		t.Skip("corpus did not overflow the budget; nothing to assert about dropping")
	}
	// every line of the truncated payload (bar the marker) appears verbatim in
	// the unbudgeted rendering: a mid-item cut would produce a partial line.
	for _, line := range strings.Split(strings.TrimSuffix(resp.Thread.Payload, "\n"), "\n") {
		if strings.Contains(line, "truncated") {
			continue
		}
		if !strings.Contains(full.Thread.Payload, line) {
			t.Fatalf("line %q is not present verbatim in the full rendering — an item was cut mid-way", line)
		}
	}
}
