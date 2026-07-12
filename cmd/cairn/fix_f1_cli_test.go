package main

// F1 regression (Codex AUDIT-003 ≡ Claude CAIRN-01), CLI-level: the exact
// auditor scenario. Written RED against d0aca05: `send --topic <name>`
// poisoned the projection; restart and reindex became unrecoverable.

import (
	"context"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

func TestF1CLISendTopicRestartReindex(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	// first daemon lifetime (explicitly closed before the restart probe)
	d1, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d1.Serve(ctx)
	waitForSocket(t, dir)

	// the auditor's exact commands: three sends with brand-new topic names
	for _, topic := range []string{"proj/alpha", "proj/beta", "proj/gamma"} {
		out, err := runCLI(t, "send", "poison probe "+topic, "--dir", dir, "--topic", topic)
		if err != nil {
			t.Fatalf("send --topic %s: %v\n%s", topic, err, out)
		}
	}

	// all three visible in search
	out, err := runCLI(t, "search", "poison probe", "--dir", dir)
	if err != nil || strings.Count(out, `"message_id"`) != 3 {
		t.Fatalf("search: %v\n%s", err, out)
	}
	cancel()
	d1.Close()

	// restart the daemon over the same mesh — the poison point
	d2, err := daemon.Start(daemon.Options{Dir: dir, DBPath: t.TempDir() + "/fresh.sqlite"})
	if err != nil {
		t.Fatalf("RED (auditor scenario): daemon restart failed after --topic sends: %v", err)
	}
	hits, err := d2.Projection().SearchLexical("poison probe", 10, false)
	d2.Close()
	if err != nil || len(hits) != 3 {
		t.Fatalf("post-restart replay lost messages: %d hits, %v", len(hits), err)
	}

	// reindex must succeed
	if out, err := runCLI(t, "reindex", "--lexical", "--dir", dir); err != nil {
		t.Fatalf("reindex after --topic sends: %v\n%s", err, out)
	}
	// doctor must be clean
	if out, err := runCLI(t, "doctor", "--dir", dir); err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
}
