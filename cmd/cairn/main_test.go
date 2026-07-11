package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI executes the cairn CLI in-process and returns stdout+stderr.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func setupEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	return filepath.Join(t.TempDir(), "cairn")
}

// M0 acceptance: `cairn init` produces a verifiable genesis event and
// `cairn identity show` verifies and reports it.
func TestInitAndIdentityShow(t *testing.T) {
	dir := setupEnv(t)

	out, err := runCLI(t, "init", "--dir", dir, "--display-name", "test-mac")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, want := range []string{"Initialized cairn", "genesis event:", "back up the root key"} {
		if !strings.Contains(out, want) {
			t.Errorf("init output missing %q:\n%s", want, out)
		}
	}

	out, err = runCLI(t, "identity", "show", "--dir", dir)
	if err != nil {
		t.Fatalf("identity show: %v\n%s", err, out)
	}
	for _, want := range []string{"cairn_id:", "device_id:", "generation 1", "root key id:", "verified: envelope signature, event_id, root-signed cert", "test-mac"} {
		if !strings.Contains(out, want) {
			t.Errorf("identity show output missing %q:\n%s", want, out)
		}
	}
}

// M0 acceptance: an unencrypted volume refuses start without the flag.
func TestInitRefusesUnencryptedWithoutFlag(t *testing.T) {
	dir := setupEnv(t)
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "unencrypted")

	if out, err := runCLI(t, "init", "--dir", dir); err == nil {
		t.Fatalf("init succeeded on unencrypted volume:\n%s", out)
	}

	// with the override it proceeds, warns, and the warning repeats on show
	out, err := runCLI(t, "init", "--dir", dir, "--allow-unencrypted")
	if err != nil {
		t.Fatalf("init --allow-unencrypted: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("no warning on allowed-unencrypted init:\n%s", out)
	}
	out, err = runCLI(t, "identity", "show", "--dir", dir)
	if err != nil {
		t.Fatalf("identity show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "OVERRIDDEN") {
		t.Errorf("persisted override did not warn on start:\n%s", out)
	}
}

// Indeterminate status fails closed too.
func TestInitRefusesUnknownVolume(t *testing.T) {
	dir := setupEnv(t)
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "unknown")
	if out, err := runCLI(t, "init", "--dir", dir); err == nil {
		t.Fatalf("init succeeded on unknown volume:\n%s", out)
	}
}

func TestReinitRefusedCLI(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "init", "--dir", dir); err == nil {
		t.Fatalf("re-init succeeded:\n%s", out)
	}
}
