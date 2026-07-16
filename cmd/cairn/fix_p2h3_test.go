package main

// FIX-P2H3 (Codex P2 shakedown MAJOR-4): the documented `cairn status` command
// now exists and reports the operator health view (version, log head,
// projection health, peers, degradation) against a live daemon.

import (
	"context"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

func TestP2H3StatusCommand(t *testing.T) {
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
	defer func() { cancel(); d.Close() }()

	// human view
	out, err := runCLI(t, "status", "--dir", dir)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, want := range []string{
		"cairn status", "version:", "log next_seq:", "degradation:",
		"projection:", "clean", "sync peers:", "sync listener:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}

	// json view
	out, err = runCLI(t, "status", "--dir", dir, "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	for _, want := range []string{"\"degradation\"", "\"parked_terminal\"", "\"next_seq\""} {
		if !strings.Contains(out, want) {
			t.Fatalf("status --json missing %q:\n%s", want, out)
		}
	}
}

// status against a stopped daemon returns the standard not-running guidance.
func TestP2H3StatusNoDaemon(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	out, err := runCLI(t, "status", "--dir", dir)
	if err == nil {
		t.Fatalf("status succeeded with no daemon:\n%s", out)
	}
	if !strings.Contains(err.Error()+out, "daemon not running") {
		t.Fatalf("status without a daemon lacks not-running guidance: err=%v\n%s", err, out)
	}
}
