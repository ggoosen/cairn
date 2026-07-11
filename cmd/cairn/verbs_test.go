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

	// digest is live in M6: budget-capped payload
	dout, err := runCLI(t, "digest", "--dir", dir, "--budget", "2000")
	if err != nil || !strings.Contains(dout, "# digest — operator") {
		t.Fatalf("digest: %v\n%s", err, dout)
	}
	// outcome commands remain M7 stubs
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

// M7 CLI flow: search returns interaction_id; outcome binds to it; gates
// report renders with the automated/human-measured column.
func TestCLIOutcomesAndGates(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)

	if out, err := runCLI(t, "send", "gates test message about beacons", "--dir", dir); err != nil {
		t.Fatalf("send: %v\n%s", err, out)
	}
	out, err := runCLI(t, "search", "beacons", "--dir", dir, "--budget", "600")
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	var so struct {
		InteractionID string `json:"interaction_id"`
	}
	if err := json.Unmarshal([]byte(out), &so); err != nil || so.InteractionID == "" {
		t.Fatalf("no interaction id: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "found", so.InteractionID, "--dir", dir); err != nil {
		t.Fatalf("found: %v\n%s", err, out)
	}
	out, err = runCLI(t, "gates", "--dir", dir)
	if err != nil {
		t.Fatalf("gates: %v\n%s", err, out)
	}
	for _, want := range []string{"automated", "human-measured", "1/1 found"} {
		if !strings.Contains(out, want) {
			t.Fatalf("gates missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("gates FAIL on healthy cairn:\n%s", out)
	}
	// reserve status via CLI
	out, err = runCLI(t, "reserve", "status", "--dir", dir)
	if err != nil || !strings.Contains(out, `"present": true`) {
		t.Fatalf("reserve status: %v\n%s", err, out)
	}
}
