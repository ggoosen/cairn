package identity

// FIX-G3: the encryption gate must fire BEFORE any device private key is
// written on the enrol/join path (a P0 invariant: keys never land on
// unencrypted storage — previously the gate only fired at runtime startup).
// --allow-unencrypted must be honored on enroll/join with the same persisted
// device-local override + per-startup warning as init. These tests fail
// against the pre-G3 code (no gate on the key-write path).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// TestG3EnrollGatesEncryptionBeforeKeyWrite: enrol on an unencrypted volume
// refuses BEFORE staging the private key; with the override it proceeds, warns,
// and the key is written.
func TestG3EnrollGatesEncryptionBeforeKeyWrite(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", base)
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "unencrypted")
	tmp := t.TempDir()

	// (1) refusal: no override → error, and NO staged key exists anywhere.
	reqPath := filepath.Join(tmp, "req.json")
	if _, err := CreateEnrollRequest(EnrollRequestOptions{DisplayName: "b", OutPath: reqPath, Now: time.Now}); err == nil {
		t.Fatal("enroll on an unencrypted volume must refuse before writing key material")
	}
	if keys := stagedKeys(t, base); len(keys) != 0 {
		t.Fatalf("G3 VIOLATION: enroll wrote a private key despite refusing: %v", keys)
	}

	// (2) override: proceeds, warns, and the staged key now exists.
	var warn strings.Builder
	if _, err := CreateEnrollRequest(EnrollRequestOptions{
		DisplayName: "b", OutPath: reqPath, AllowUnencrypted: true, Now: time.Now, Out: &warn,
	}); err != nil {
		t.Fatalf("enroll with --allow-unencrypted refused: %v", err)
	}
	if !strings.Contains(warn.String(), "WARNING") {
		t.Fatalf("override did not warn loudly: %q", warn.String())
	}
	if keys := stagedKeys(t, base); len(keys) == 0 {
		t.Fatal("enroll with override did not stage the private key")
	}
}

// TestG3JoinGatesEncryptionAndPersistsOverride runs the full ceremony to a
// grant (under an encrypted volume), then joins on an unencrypted volume:
// refused before the device key is written; with the override it proceeds and
// PERSISTS allow_unencrypted device-local (per-startup warning parity).
func TestG3JoinGatesEncryptionAndPersistsOverride(t *testing.T) {
	baseA, baseB := t.TempDir(), t.TempDir()
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	tmp := t.TempDir()
	reqPath := filepath.Join(tmp, "req.json")
	grantPath := filepath.Join(tmp, "grant.json")
	rootRestored := filepath.Join(tmp, "restored-root.key")

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := Initialize(InitOptions{Dir: dirA, Out: nil}); err != nil {
		t.Fatal(err)
	}
	loadedA, err := Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	rootLocal := filepath.Join(loadedA.DeviceDir, config.RootKeyName)
	blob, err := os.ReadFile(rootLocal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootRestored, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	if _, err := CreateEnrollRequest(EnrollRequestOptions{DisplayName: "b", OutPath: reqPath, Now: time.Now}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	grant, err := Approve(ApproveOptions{Dir: dirA, RequestPath: reqPath, RootKeyPath: rootRestored, GrantPath: grantPath, Out: nil})
	if err != nil {
		t.Fatal(err)
	}

	// join on an UNENCRYPTED volume
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "unencrypted")
	dirB := filepath.Join(t.TempDir(), "cairnB")

	// (1) refusal before the device key is written.
	if err := Join(JoinOptions{GrantPath: grantPath, Dir: dirB, Out: nil}); err == nil {
		t.Fatal("join on an unencrypted volume must refuse before writing the device key")
	}
	deviceDir, err := config.DeviceStateDir(grant.CairnID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(deviceDir, config.DeviceKeyName)); err == nil {
		t.Fatal("G3 VIOLATION: join wrote the device key despite refusing")
	}

	// (2) override: proceeds, warns, and PERSISTS the device-local override.
	var warn strings.Builder
	if err := Join(JoinOptions{GrantPath: grantPath, Dir: dirB, AllowUnencrypted: true, Out: &warn}); err != nil {
		t.Fatalf("join with --allow-unencrypted refused: %v", err)
	}
	if !strings.Contains(warn.String(), "WARNING") {
		t.Fatalf("join override did not warn: %q", warn.String())
	}
	loadedB, err := Load(dirB)
	if err != nil {
		t.Fatal(err)
	}
	if !loadedB.Device.AllowUnencrypted {
		t.Fatal("G3: join did not persist allow_unencrypted device-local (would force a TOML hand-edit)")
	}
}

// stagedKeys lists device.key files staged under the enroll-pending tree.
func stagedKeys(t *testing.T, base string) []string {
	t.Helper()
	var found []string
	pending := filepath.Join(base, "enroll-pending")
	entries, err := os.ReadDir(pending)
	if err != nil {
		return nil // no staging dir at all
	}
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(pending, e.Name(), config.DeviceKeyName)); err == nil {
			found = append(found, e.Name())
		}
	}
	return found
}
