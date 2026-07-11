package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

// startTestDaemon runs a real daemon + IPC for CLI-level tests.
func startTestDaemon(t *testing.T, dir string) {
	t.Helper()
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	t.Cleanup(func() {
		cancel()
		d.Close()
	})
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(loaded.DeviceDir, "daemon.sock.path")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon socket never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// CLI end to end: init → daemon → send → search → peek → retract → fetch.
func TestCLISendSearchPeekRetract(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)

	out, err := runCLI(t, "send", "an elephant walked through the CLI", "--dir", dir, "--priority", "2")
	if err != nil {
		t.Fatalf("send: %v\n%s", err, out)
	}
	var pub daemon.PublishResult
	if err := json.Unmarshal([]byte(out), &pub); err != nil {
		t.Fatalf("send output not JSON: %v\n%s", err, out)
	}

	out, err = runCLI(t, "search", "elephant", "--dir", dir)
	if err != nil || !strings.Contains(out, pub.MessageID) {
		t.Fatalf("search: %v\n%s", err, out)
	}

	out, err = runCLI(t, "peek", pub.MessageID, "--dir", dir)
	if err != nil || !strings.Contains(out, pub.RevisionID) {
		t.Fatalf("peek: %v\n%s", err, out)
	}

	out, err = runCLI(t, "fetch", pub.MessageID, "--dir", dir, "--view", "cli-view")
	if err != nil || !strings.Contains(out, "body_path") {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	if out, err = runCLI(t, "retract", pub.MessageID, "--dir", dir, "--reason", "test"); err != nil {
		t.Fatalf("retract: %v\n%s", err, out)
	}
	out, _ = runCLI(t, "search", "elephant", "--dir", dir)
	if strings.Contains(out, pub.MessageID) {
		t.Fatalf("retracted message still visible:\n%s", out)
	}

	// mutations without a daemon must fail (rulings §6), reads too (IPC-only)
	// — covered by stopping the daemon in cleanup; here check stubs:
	if _, err := runCLI(t, "digest", "--dir", dir); err == nil || !strings.Contains(err.Error(), "M6") {
		t.Fatalf("digest stub: %v", err)
	}
	if _, err := runCLI(t, "found", "--dir", dir); err == nil || !strings.Contains(err.Error(), "M7") {
		t.Fatalf("found stub: %v", err)
	}
}

// Mutations fail cleanly when no daemon is running.
func TestCLIMutationsRequireDaemon(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "send", "orphan message", "--dir", dir); err == nil {
		t.Fatal("send succeeded without a daemon")
	} else if !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

// M5 CLI flow: export → edit body → ingest → revised; doctor conflicts clean.
func TestCLIExportIngest(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)

	out, err := runCLI(t, "send", "exportable content line\n", "--dir", dir)
	if err != nil {
		t.Fatalf("send: %v\n%s", err, out)
	}
	var pub daemon.PublishResult
	json.Unmarshal([]byte(out), &pub)

	out, err = runCLI(t, "export", pub.MessageID, "--dir", dir)
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	path := strings.TrimSpace(out)
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), "message_id: "+pub.MessageID) {
		t.Fatalf("export file: %v\n%s", err, content)
	}

	edited := strings.Replace(string(content), "exportable content line", "exportable content line EDITED", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, "export", "ingest", path, "--dir", dir)
	if err != nil || !strings.Contains(out, `"revised"`) {
		t.Fatalf("ingest: %v\n%s", err, out)
	}

	out, err = runCLI(t, "doctor", "conflicts", "--dir", dir)
	if err != nil || !strings.Contains(out, "no unresolved conflicts") {
		t.Fatalf("doctor conflicts: %v\n%s", err, out)
	}
}
