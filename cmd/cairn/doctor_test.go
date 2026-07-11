package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// M1 acceptance at the CLI level: doctor reports clean on a fresh cairn and
// detects a deliberately corrupted frame.
func TestDoctorCleanThenDetectsCorruption(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	out, err := runCLI(t, "doctor", "--dir", dir)
	if err != nil {
		t.Fatalf("doctor on healthy log: %v\n%s", err, out)
	}
	if !strings.Contains(out, "doctor: clean") {
		t.Fatalf("expected clean report:\n%s", out)
	}

	// bit-flip the genesis segment
	var segPath string
	filepath.WalkDir(filepath.Join(dir, "events"), func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".seg") {
			segPath = p
		}
		return nil
	})
	if segPath == "" {
		t.Fatal("no segment found")
	}
	blob, err := os.ReadFile(segPath)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)/2] ^= 0x01
	if err := os.WriteFile(segPath, blob, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err = runCLI(t, "doctor", "--dir", dir)
	if err == nil {
		t.Fatalf("doctor passed on corrupted segment:\n%s", out)
	}
	if !strings.Contains(out, "PROBLEM") {
		t.Fatalf("no PROBLEM line in report:\n%s", out)
	}
}
