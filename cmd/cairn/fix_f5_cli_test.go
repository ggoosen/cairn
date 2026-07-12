package main

// F5: identity export-root (verified export, mesh-dir refusal, one-way
// removal) and nonzero exits for unknown subcommands (Codex AUDIT-004).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestF5ExportRoot(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	// refuse a destination inside the portable mesh dir
	inside := filepath.Join(dir, "exports", "root.key")
	if out, err := runCLI(t, "identity", "export-root", "--out", inside, "--dir", dir); err == nil {
		t.Fatalf("export INSIDE the mesh dir allowed:\n%s", out)
	} else if !strings.Contains(err.Error(), "INSIDE") {
		t.Fatalf("unhelpful refusal: %v", err)
	}

	// verified export, keep local copy (non-interactive default)
	dest := filepath.Join(t.TempDir(), "root-export.key")
	out, err := runCLI(t, "identity", "export-root", "--out", dest, "--dir", dir)
	if err != nil {
		t.Fatalf("export-root: %v\n%s", err, out)
	}
	if !strings.Contains(out, "verified") || !strings.Contains(out, "OFFLINE") {
		t.Fatalf("missing instructions:\n%s", out)
	}
	exported, err := os.ReadFile(dest)
	if err != nil || len(exported) == 0 {
		t.Fatalf("export file: %v", err)
	}
	// never overwrite existing key material
	if _, err := runCLI(t, "identity", "export-root", "--out", dest, "--dir", dir); err == nil {
		t.Fatal("overwrote an existing export")
	}

	// export with --remove-local: device copy gone, migrate now demands it
	dest2 := filepath.Join(t.TempDir(), "root-export-2.key")
	out, err = runCLI(t, "identity", "export-root", "--out", dest2, "--remove-local", "--dir", dir)
	if err != nil {
		t.Fatalf("export-root --remove-local: %v\n%s", err, out)
	}
	if !strings.Contains(out, "removed") {
		t.Fatalf("removal not reported:\n%s", out)
	}
	// a further export must fail: the local copy is gone
	if _, err := runCLI(t, "identity", "export-root", "--out", filepath.Join(t.TempDir(), "x.key"), "--dir", dir); err == nil {
		t.Fatal("exported a root key that no longer exists locally")
	}
	// identity show still works (root key not needed for reads)
	if out, err := runCLI(t, "identity", "show", "--dir", dir); err != nil {
		t.Fatalf("identity show after removal: %v\n%s", err, out)
	}
}

func TestF5UnknownSubcommandsExitNonzero(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	cases := [][]string{
		{"identity", "bogus-sub"},
		{"identity"}, // bare group
		{"topic", "bogus"},
		{"reserve", "bogus"},
		{"ingest", "bogus"},
		{"completely-unknown-command"},
	}
	for _, args := range cases {
		args = append(args, "--dir", dir)
		if _, err := runCLI(t, args...); err == nil {
			t.Fatalf("%v exited zero", args)
		}
	}
}
