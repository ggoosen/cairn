package main

// FIX-H7: stale-binary detection. The version-mismatch warning is the guard
// against a daemon running old code (which cost an entire audit run).

import (
	"strings"
	"testing"
)

func TestH7VersionMismatchWarning(t *testing.T) {
	// equal → no warning
	if _, warn := versionMismatchWarning("p1-abc", "p1-abc"); warn {
		t.Fatal("identical versions must not warn")
	}
	// different → warn, naming both
	msg, warn := versionMismatchWarning("p0-old", "p1-new")
	if !warn {
		t.Fatal("differing versions must warn")
	}
	if !strings.Contains(msg, "p0-old") || !strings.Contains(msg, "p1-new") || !strings.Contains(msg, "STALE") {
		t.Fatalf("warning missing detail: %q", msg)
	}
	// empty running (pre-H7 daemon that never reported a version) → warn
	msg, warn = versionMismatchWarning("", "p1-new")
	if !warn || !strings.Contains(msg, "unknown") {
		t.Fatalf("empty running version should warn as unknown: %q", msg)
	}
}

// `cairn --version` prints the build version and exits 0.
func TestH7VersionCommandPrints(t *testing.T) {
	out, err := runCLI(t, "--version")
	if err != nil {
		t.Fatalf("cairn --version: %v\n%s", err, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "p1") {
		t.Fatalf("cairn --version did not print the p1 version: %q", out)
	}
}
