package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinary compiles the real cairn binary once per test run.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cairn")
	cmd := exec.Command("go", "build", "-tags", "sqlite_fts5", "-o", bin, ".")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building binary: %v\n%s", err, out)
	}
	return bin
}

// M8 acceptance: the restore drill (through the REAL shell scripts) proves
// a portable-only restore is refused without identity and `init --adopt`
// creates a usable new origin with history preserved.
func TestBackupRestoreDrillScripts(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync unavailable")
	}
	bin := buildBinary(t)
	repoRoot, _ := filepath.Abs("../..")

	// a live cairn with content
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	src := filepath.Join(t.TempDir(), "cairn")
	run := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run(nil, bin, "init", "--dir", src)

	// backup via the real script (excludes .cairn/ and device identity)
	backup := filepath.Join(t.TempDir(), "backup")
	run(nil, filepath.Join(repoRoot, "scripts", "cairn-backup.sh"), src, backup)
	if _, err := os.Stat(filepath.Join(backup, "cairn.toml")); err != nil {
		t.Fatal("backup missing cairn.toml")
	}
	if _, err := os.Stat(filepath.Join(backup, ".cairn")); err == nil {
		t.Fatal("backup contains derived state")
	}

	// the drill runs on a "fresh machine": new device-state home
	restore := filepath.Join(t.TempDir(), "restored")
	out := run([]string{"CAIRN_DEVICE_STATE_DIR=" + t.TempDir()},
		filepath.Join(repoRoot, "scripts", "cairn-restore-drill.sh"), backup, restore, bin)
	if !strings.Contains(out, "DRILL PASSED") {
		t.Fatalf("drill output:\n%s", out)
	}
	// new origin dir + archived history exist
	matches, _ := filepath.Glob(filepath.Join(restore, "events-preadopt-*"))
	if len(matches) != 1 {
		t.Fatalf("archived history missing: %v", matches)
	}
}

// setup-agent creates the view skeleton and config.
func TestSetupAgentCLI(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	out, err := runCLI(t, "setup-agent", "claude-project-a", "--dir", dir, "--interest", "zebra api decisions")
	if err != nil || !strings.Contains(out, "outbox") {
		t.Fatalf("setup-agent: %v\n%s", err, out)
	}
	for _, sub := range []string{"outbox", "fetched", "conflicts"} {
		if fi, err := os.Stat(filepath.Join(dir, "views", "claude-project-a", sub)); err != nil || !fi.IsDir() {
			t.Fatalf("missing %s: %v", sub, err)
		}
	}
	blob, err := os.ReadFile(filepath.Join(dir, "views", "claude-project-a", "view.json"))
	if err != nil || !strings.Contains(string(blob), "zebra api decisions") {
		t.Fatalf("view.json: %v\n%s", err, blob)
	}
	// path metacharacters refused
	if _, err := runCLI(t, "setup-agent", "../evil", "--dir", dir); err == nil {
		t.Fatal("path traversal name accepted")
	}
}
