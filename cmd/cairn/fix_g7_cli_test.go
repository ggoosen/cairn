package main

// FIX-G7 (minor batch): version string reads p1 (G7.5); `identity show` on a
// fresh bare-enrolled node reports the BOOTSTRAP state instead of hard-erroring
// on the missing local genesis (G7.2). (G7.1 async sync-now, G7.3 push-side
// fork detection, and G7.4 revoke-bundling are covered in the daemon suite.)

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestG7VersionIsP1(t *testing.T) {
	if !strings.HasPrefix(version, "p1") {
		t.Fatalf("version string still not p1 on a P1 build: %q", version)
	}
}

// TestG7IdentityShowBootstrapState runs the enrol ceremony to a JOINED node
// that has a device identity + bootstrap grant chain but NO local genesis yet
// (the log arrives via N6). `identity show` must report that bootstrap state
// and exit 0, not hard-error with "no genesis event found".
func TestG7IdentityShowBootstrapState(t *testing.T) {
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	baseA, baseB := t.TempDir(), t.TempDir()
	tmp := t.TempDir()
	reqPath := filepath.Join(tmp, "req.json")
	grantPath := filepath.Join(tmp, "grant.json")
	rootRestored := filepath.Join(tmp, "root.key")

	// machine A: init the mesh, stash the root key offline
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dirA, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	loadedA, err := identity.Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(filepath.Join(loadedA.DeviceDir, config.RootKeyName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootRestored, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	// machine B: enroll → (A approves) → join. No sync runs, so B has no genesis.
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	if _, err := identity.CreateEnrollRequest(identity.EnrollRequestOptions{DisplayName: "b", OutPath: reqPath}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	if _, err := identity.Approve(identity.ApproveOptions{Dir: dirA, RequestPath: reqPath, RootKeyPath: rootRestored, GrantPath: grantPath, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	dirB := filepath.Join(t.TempDir(), "cairnB")
	if err := identity.Join(identity.JoinOptions{GrantPath: grantPath, Dir: dirB, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}

	// identity show on the bare-enrolled node: reports bootstrap state, exit 0.
	out, err := runCLI(t, "identity", "show", "--dir", dirB)
	if err != nil {
		t.Fatalf("G7.2: identity show hard-errored on a bare-enrolled node: %v\n%s", err, out)
	}
	if !strings.Contains(out, "BOOTSTRAP STATE") {
		t.Fatalf("identity show did not report the bootstrap state:\n%s", out)
	}
	if strings.Contains(out, "no genesis event found") {
		t.Fatalf("identity show still surfaces the raw genesis error:\n%s", out)
	}
}
