package main

// P3-4b: `cairn net` reports transport, role, listener and peers against a live
// daemon. A thin node reports role thin; the default transport is tcp-tailnet.

import (
	"context"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

func TestP34bNetCommand(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir, "--thin"); err != nil {
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

	out, err := runCLI(t, "net", "--dir", dir)
	if err != nil {
		t.Fatalf("net: %v\n%s", err, out)
	}
	for _, want := range []string{"transport:", "tcp-tailnet", "role:", "thin", "listener:", "peers:", "relays:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("net output missing %q:\n%s", want, out)
		}
	}

	// json view returns the raw status
	out, err = runCLI(t, "net", "--dir", dir, "--json")
	if err != nil {
		t.Fatalf("net --json: %v\n%s", err, out)
	}
	if !strings.Contains(out, "\"transport\"") || !strings.Contains(out, "\"role\"") {
		t.Fatalf("net --json missing fields:\n%s", out)
	}
}
