package main

// `cairn setup` orchestration: it creates the mesh idempotently. The daemon and
// MCP steps are skipped here — they touch real launchd/systemd and user client
// configs, which are exercised by their own package tests; this test covers the
// wizard's own logic (init-if-absent + idempotent re-run).

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/identity"
)

func TestSetupInitsMeshIdempotently(t *testing.T) {
	dir := setupEnv(t)

	out, err := runCLI(t, "setup", "--skip-daemon", "--skip-mcp", "--dir", dir)
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if !strings.Contains(out, "created at") {
		t.Fatalf("first setup did not create the mesh:\n%s", out)
	}
	if _, err := identity.Load(dir); err != nil {
		t.Fatalf("mesh not initialized after setup: %v", err)
	}
	if !strings.Contains(out, "Cairn is set up") {
		t.Fatalf("setup did not print the done summary:\n%s", out)
	}

	// re-run: must NOT re-init, must report the existing mesh
	out2, err := runCLI(t, "setup", "--skip-daemon", "--skip-mcp", "--dir", dir)
	if err != nil {
		t.Fatalf("re-run setup: %v\n%s", err, out2)
	}
	if !strings.Contains(out2, "already initialized") {
		t.Fatalf("re-run setup did not detect the existing mesh (not idempotent):\n%s", out2)
	}
	if strings.Contains(out2, "created at") {
		t.Fatalf("re-run setup re-created the mesh:\n%s", out2)
	}
}

// Fable-review MAJOR #1: an unencrypted volume must not dead-end. setup gives an
// actionable --allow-unencrypted hint, and CAIRN_ALLOW_UNENCRYPTED proceeds (so
// the one-command install.sh / make deploy path can carry it).
func TestSetupUnencryptedVolumeGuidance(t *testing.T) {
	dir := setupEnv(t)
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "unencrypted")

	_, err := runCLI(t, "setup", "--skip-daemon", "--skip-mcp", "--dir", dir)
	if err == nil {
		t.Fatal("expected setup to refuse an unencrypted volume")
	}
	if !strings.Contains(err.Error(), "--allow-unencrypted") {
		t.Fatalf("no actionable hint for the unencrypted case: %v", err)
	}

	t.Setenv("CAIRN_ALLOW_UNENCRYPTED", "1")
	if out, err := runCLI(t, "setup", "--skip-daemon", "--skip-mcp", "--dir", dir); err != nil {
		t.Fatalf("CAIRN_ALLOW_UNENCRYPTED=1 did not proceed: %v\n%s", err, out)
	}
	if _, err := identity.Load(dir); err != nil {
		t.Fatalf("mesh not created under the env escape hatch: %v", err)
	}
}

// Fable-review MAJOR #2: on a restored/second-machine copy (portable data
// present, no device identity here) setup must point at `init --adopt`, NOT at
// --allow-unencrypted.
func TestSetupRestoredCopyGuidance(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "setup", "--skip-daemon", "--skip-mcp", "--dir", dir); err != nil {
		t.Fatalf("initial setup: %v\n%s", err, out)
	}
	// point device-state at a fresh empty dir → the portable dir now looks like a
	// restored copy with no local identity (identity.ErrRestoredCopy).
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())

	_, err := runCLI(t, "setup", "--skip-daemon", "--skip-mcp", "--dir", dir)
	if err == nil {
		t.Fatal("expected restored-copy guidance, got success")
	}
	if !strings.Contains(err.Error(), "init --adopt") {
		t.Fatalf("restored-copy path did not point to `init --adopt`: %v", err)
	}
	if strings.Contains(err.Error(), "allow-unencrypted") {
		t.Fatalf("restored-copy path wrongly suggested --allow-unencrypted: %v", err)
	}
}
