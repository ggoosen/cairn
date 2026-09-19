package main

// D4 (CLI surface): `--budget-tokens` beside `--budget`, exactly one of them,
// and the tokenizer named on stderr. Exercised through the real cobra
// commands against a real daemon, because the flag-defaulting rule (a
// --budget the user never typed must not manufacture the both-budgets
// refusal) exists only in the CLI and cannot be seen from the Go API.

import (
	"context"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
)

func TestD4BudgetTokensCLI(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	t.Cleanup(func() { cancel(); d.Close() })
	waitForSocket(t, dir)

	for i := 0; i < 5; i++ {
		if out, err := runCLI(t, "send", "a note about widget assembly with enough words to cost tokens", "--dir", dir); err != nil {
			t.Fatalf("send: %v\n%s", err, out)
		}
	}

	// digest defaults --budget to 4000; asking for tokens must NOT be read as
	// "both budgets" just because the char flag has a default value.
	out, err := runCLI(t, "digest", "--budget-tokens", "120", "--dir", dir)
	if err != nil {
		t.Fatalf("digest --budget-tokens: %v\n%s", err, out)
	}
	if !strings.Contains(out, "tokenizer "+config.TokenizerApprox) {
		t.Fatalf("digest did not name the tokenizer it used:\n%s", out)
	}
	if !strings.Contains(out, "APPROXIMATE") {
		t.Fatalf("digest did not disclose that the token count is approximate:\n%s", out)
	}
	if !strings.Contains(out, "tokens") {
		t.Fatalf("digest did not report the budget mode:\n%s", out)
	}

	// typing BOTH is refused — the CLI passes it through and the daemon rules
	out, err = runCLI(t, "digest", "--budget", "500", "--budget-tokens", "120", "--dir", dir)
	if err == nil {
		t.Fatalf("digest accepted both budgets:\n%s", out)
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("refusal is not the D4 one: %v", err)
	}

	for _, args := range [][]string{
		{"search", "widget", "--budget-tokens", "80"},
		{"thread", "--budget-tokens", "80"}, // thread id appended below
	} {
		if args[0] == "thread" {
			// find a thread id: any message id works as its own thread root
			sendOut, serr := runCLI(t, "send", "thread root for the budget test", "--dir", dir)
			if serr != nil {
				t.Fatalf("send: %v\n%s", serr, sendOut)
			}
			id := extractJSONField(t, sendOut, "message_id")
			args = []string{"thread", id, "--budget-tokens", "80"}
		}
		out, err := runCLI(t, append(args, "--dir", dir)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if !strings.Contains(out, config.TokenizerApprox) {
			t.Fatalf("%v did not name the tokenizer:\n%s", args, out)
		}
	}

	// char mode still names its (exact) counter, and does not claim to be
	// approximate — an exact budget that says "approximate" is as misleading
	// as an estimate that does not.
	out, err = runCLI(t, "digest", "--budget", "600", "--dir", dir)
	if err != nil {
		t.Fatalf("digest --budget: %v\n%s", err, out)
	}
	if !strings.Contains(out, "tokenizer "+config.TokenizerChars) {
		t.Fatalf("char-budgeted digest did not name unicode-scalars:\n%s", out)
	}
	if strings.Contains(out, "APPROXIMATE") {
		t.Fatalf("an exact character budget was labelled approximate:\n%s", out)
	}
}

// extractJSONField pulls one string field out of a CLI JSON payload without
// pulling in a decoder for a two-line assertion.
func extractJSONField(t *testing.T, blob, field string) string {
	t.Helper()
	key := `"` + field + `": "`
	i := strings.Index(blob, key)
	if i < 0 {
		t.Fatalf("no %s in output:\n%s", field, blob)
	}
	rest := blob[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("malformed %s in output:\n%s", field, blob)
	}
	return rest[:j]
}
