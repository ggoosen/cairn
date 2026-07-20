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
