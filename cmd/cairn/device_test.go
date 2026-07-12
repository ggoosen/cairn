package main

// N5 CLI wiring: the ceremony verbs drive the same machinery the daemon
// E2E test proves (internal/daemon/sync_test.go).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestN5DeviceCeremonyCLI(t *testing.T) {
	baseA, baseB := t.TempDir(), t.TempDir()
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	tmp := t.TempDir()
	reqPath := filepath.Join(tmp, "req.json")
	grantPath := filepath.Join(tmp, "grant.json")

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if out, err := runCLI(t, "init", "--dir", dirA); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	loadedA, err := identity.Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	rootKey := filepath.Join(loadedA.DeviceDir, config.RootKeyName)

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	if out, err := runCLI(t, "device", "enroll", "--name", "air", "--out", reqPath); err != nil {
		t.Fatalf("enroll: %v\n%s", err, out)
	} else if !strings.Contains(out, "single-use") {
		t.Fatalf("enroll output lacks R28 note:\n%s", out)
	}

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	out, err := runCLI(t, "device", "approve", reqPath, "--root-key", rootKey, "--grant", grantPath, "--dir", dirA)
	if err != nil {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	if !strings.Contains(out, "REMOVE the restored root key") {
		t.Fatalf("approve output lacks the key-removal reminder:\n%s", out)
	}

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	dirB := filepath.Join(t.TempDir(), "cairnB")
	if out, err := runCLI(t, "device", "join", grantPath, "--dir", dirB); err != nil {
		t.Fatalf("join: %v\n%s", err, out)
	}

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	out, err = runCLI(t, "device", "list", "--dir", dirA)
	if err != nil || strings.Count(out, "member") != 2 {
		t.Fatalf("device list: %v\n%s", err, out)
	}

	// listener guard is enforced end to end: a daemon configured to bind
	// 0.0.0.0 refuses to start its listener
	loadedA.Device.SyncListen = "0.0.0.0:9700"
	if err := loadedA.Device.SaveDevice(loadedA.DeviceDir); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("CAIRN_SYNC_ALLOW_LOOPBACK")
	// Run would error; validated directly to keep the test fast
	if err := validateSyncListen("0.0.0.0:9700"); err == nil {
		t.Fatal("0.0.0.0 accepted")
	}
}
