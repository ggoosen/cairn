package main

// FIX-P2H2 (Codex P2 shakedown MAJOR-1): `reindex --lexical` against a LIVE
// daemon atomically swaps index.sqlite under the daemon's open handle and
// silently splits the daemon's view from disk until restart. The fix refuses
// the reindex while a daemon is reachable, with guidance; with the daemon
// stopped the reindex proceeds normally.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestP2H2ReindexRefusesLiveDaemon(t *testing.T) {
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
	waitForSocket(t, dir)

	out, err := runCLI(t, "reindex", "--lexical", "--dir", dir)
	if err == nil {
		t.Fatalf("reindex --lexical succeeded against a live daemon (silent split not prevented):\n%s", out)
	}
	msg := err.Error() + out
	if !strings.Contains(msg, "daemon is running") || !strings.Contains(msg, "Stop the daemon") {
		t.Fatalf("refusal lacks the stop-the-daemon guidance: err=%v\n%s", err, out)
	}

	// With the daemon stopped, the same reindex proceeds.
	cancel()
	d.Close()
	waitForDaemonDown(t, dir)

	out, err = runCLI(t, "reindex", "--lexical", "--dir", dir)
	if err != nil {
		t.Fatalf("reindex --lexical failed with the daemon stopped: %v\n%s", err, out)
	}
	if !strings.Contains(out, "lexical projection rebuilt") {
		t.Fatalf("reindex did not report a rebuild:\n%s", out)
	}
}

// waitForDaemonDown polls until the daemon IPC is unreachable, so the reindex
// liveness probe sees a stopped daemon deterministically.
func waitForDaemonDown(t *testing.T, dir string) {
	t.Helper()
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := daemon.Call(loaded.DeviceDir, daemon.Request{Op: "status"}); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never became unreachable after Close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
