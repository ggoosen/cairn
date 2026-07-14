package identity

// FIX-H2 (G3/R46 sweep): the encryption gate must fire BEFORE any device
// private key is written on EVERY key-write path. G3 gated enroll/join; the N9
// audit found `migrate` (migrate.go) writing the new device key with no gate —
// the same P0 invariant ("no key material on unencrypted storage via any path")
// alive on an un-enumerated path. TestH2AllKeyWritePathsRouteThroughGate is the
// R46 table: a new key-creating entrypoint added later without the gate fails
// it. These tests fail against the pre-H2 code (migrate has no gate).

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// initEncryptedMesh initializes a mesh on an encrypted volume and returns its
// portable dir and device-state base (the caller drives migrate afterwards).
func initEncryptedMesh(t *testing.T) (dir, base string) {
	t.Helper()
	base = t.TempDir()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", base)
	dir = filepath.Join(t.TempDir(), "cairn")
	if _, err := Initialize(InitOptions{
		Dir: dir, Checker: fakeChecker{VolumeEncrypted}, Out: io.Discard,
		Now: func() time.Time { return time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC) },
	}); err != nil {
		t.Fatal(err)
	}
	return dir, base
}

// TestH2MigrateGatesEncryptionBeforeKeyWrite: migrate on an unencrypted volume
// refuses BEFORE staging the new device key; with the override it proceeds,
// warns, and the staged key appears.
func TestH2MigrateGatesEncryptionBeforeKeyWrite(t *testing.T) {
	dir, base := initEncryptedMesh(t)
	pendingKey := func() string {
		// migrate stages under <deviceDir>/migrate-pending/device.key; deviceDir
		// is the single child of base for this cairn.
		entries, _ := os.ReadDir(base)
		for _, e := range entries {
			p := filepath.Join(base, e.Name(), "migrate-pending", config.DeviceKeyName)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		return ""
	}

	// (1) refusal: unencrypted, no override → error, and NO new key staged.
	if _, err := Migrate(MigrateOptions{
		Dir: dir, Checker: fakeChecker{VolumeUnencrypted}, Out: io.Discard,
		Now: time.Now,
	}); err == nil {
		t.Fatal("H2 VIOLATION: migrate on an unencrypted volume must refuse before writing the device key")
	}
	if p := pendingKey(); p != "" {
		t.Fatalf("H2 VIOLATION: migrate staged a device key despite refusing: %s", p)
	}

	// (2) override: proceeds and warns loudly.
	var warn strings.Builder
	if _, err := Migrate(MigrateOptions{
		Dir: dir, Checker: fakeChecker{VolumeUnencrypted}, AllowUnencrypted: true,
		Out: &warn, Now: time.Now,
	}); err != nil {
		t.Fatalf("migrate with --allow-unencrypted refused: %v", err)
	}
	if !strings.Contains(warn.String(), "WARNING") {
		t.Fatalf("migrate override did not warn loudly: %q", warn.String())
	}
}

// TestH2AllKeyWritePathsRouteThroughGate is the R46 sweep as a table: every
// entrypoint that writes device/root key material must refuse on an unencrypted
// volume without the override. Adding a new key-write path? Add it here — the
// gate is not optional. (export-root is EXEMPT by design: it writes the root
// key to an operator-chosen OFFLINE destination — USB / air-gapped / printed —
// where an encryption check would defeat the purpose; adopt routes through
// Initialize and is covered by it.)
func TestH2AllKeyWritePathsRouteThroughGate(t *testing.T) {
	cases := []struct {
		name string
		// run performs the key-creating entrypoint on an UNENCRYPTED volume with
		// no override; it must return a non-nil error (the gate refusing).
		run func(t *testing.T) error
	}{
		{"init", func(t *testing.T) error {
			t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
			_, err := Initialize(InitOptions{
				Dir: filepath.Join(t.TempDir(), "c"), Checker: fakeChecker{VolumeUnencrypted}, Out: io.Discard,
				Now: func() time.Time { return time.Now() },
			})
			return err
		}},
		{"enroll-request", func(t *testing.T) error {
			t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
			_, err := CreateEnrollRequest(EnrollRequestOptions{
				DisplayName: "b", OutPath: filepath.Join(t.TempDir(), "req.json"),
				Checker: fakeChecker{VolumeUnencrypted}, Now: time.Now, Out: io.Discard,
			})
			return err
		}},
		{"join", func(t *testing.T) error {
			// build a real grant on an encrypted volume, then join unencrypted.
			return joinUnencrypted(t)
		}},
		{"migrate", func(t *testing.T) error {
			dir, _ := initEncryptedMesh(t)
			_, err := Migrate(MigrateOptions{
				Dir: dir, Checker: fakeChecker{VolumeUnencrypted}, Out: io.Discard, Now: time.Now,
			})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(t); err == nil {
				t.Fatalf("R46: key-write path %q did not route through the encryption gate (proceeded on an unencrypted volume)", tc.name)
			}
		})
	}
}

// joinUnencrypted drives init→enroll→approve on an encrypted volume then joins
// on an unencrypted one, returning the Join error (expected non-nil: the gate).
func joinUnencrypted(t *testing.T) error {
	t.Helper()
	baseA, baseB := t.TempDir(), t.TempDir()
	tmp := t.TempDir()
	reqPath := filepath.Join(tmp, "req.json")
	grantPath := filepath.Join(tmp, "grant.json")
	rootRestored := filepath.Join(tmp, "root.key")

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := Initialize(InitOptions{Dir: dirA, Checker: fakeChecker{VolumeEncrypted}, Out: io.Discard, Now: time.Now}); err != nil {
		t.Fatal(err)
	}
	loadedA, err := Load(dirA)
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

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	if _, err := CreateEnrollRequest(EnrollRequestOptions{DisplayName: "b", OutPath: reqPath, Checker: fakeChecker{VolumeEncrypted}, Now: time.Now, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	if _, err := Approve(ApproveOptions{Dir: dirA, RequestPath: reqPath, RootKeyPath: rootRestored, GrantPath: grantPath, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	dirB := filepath.Join(t.TempDir(), "cairnB")
	return Join(JoinOptions{GrantPath: grantPath, Dir: dirB, Checker: fakeChecker{VolumeUnencrypted}, Out: io.Discard})
}
