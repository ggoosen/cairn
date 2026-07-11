package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// fakeChecker simulates volume states for the fail-closed tests.
type fakeChecker struct{ status VolumeStatus }

func (f fakeChecker) Status(string) (VolumeStatus, string, error) {
	return f.status, "fake", nil
}

func testOpts(t *testing.T, status VolumeStatus, allow bool) InitOptions {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	return InitOptions{
		Dir:              filepath.Join(t.TempDir(), "cairn"),
		AllowUnencrypted: allow,
		Checker:          fakeChecker{status},
		Now:              func() time.Time { return time.Date(2026, 7, 11, 1, 2, 3, 4000, time.UTC) },
		Out:              io.Discard,
	}
}

func TestKeySaveLoadRoundTrip(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "k.key")
	if err := SaveKey(path, priv); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != config.KeyFilePerm {
		t.Fatalf("key perm %o, want %o", fi.Mode().Perm(), config.KeyFilePerm)
	}
	got, err := LoadKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatal("key round trip mismatch")
	}
	// never overwrite an existing key
	if err := SaveKey(path, priv); err == nil {
		t.Fatal("SaveKey overwrote an existing key file")
	}
}

func TestCertSignVerify(t *testing.T) {
	rootPub, rootPriv, _ := GenerateKey()
	devPub, _, _ := GenerateKey()
	cert := DeviceCert{
		DeviceID:   "0190a1b2-c3d4-7e5f-8901-234567890def",
		Pubkey:     base64.StdEncoding.EncodeToString(devPub),
		Generation: 1,
		IssuedAt:   "2026-07-11T00:00:00.000000Z",
	}
	if err := cert.SignCert(rootPriv); err != nil {
		t.Fatal(err)
	}
	if err := cert.Verify(rootPub); err != nil {
		t.Fatal(err)
	}
	tampered := cert
	tampered.Generation = 2
	if err := tampered.Verify(rootPub); err == nil {
		t.Fatal("tampered cert verified")
	}
	otherPub, _, _ := GenerateKey()
	if err := cert.Verify(otherPub); err == nil {
		t.Fatal("cert verified against wrong root")
	}
}

func TestInitializeProducesVerifiableGenesis(t *testing.T) {
	opts := testOpts(t, VolumeEncrypted, false)
	res, err := Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(opts.Dir)
	if err != nil {
		t.Fatal(err)
	}
	env, pl, err := loaded.GenesisRecord()
	if err != nil {
		t.Fatal(err)
	}
	if env.EventID != res.GenesisEventID {
		t.Fatalf("genesis event id mismatch: %s vs %s", env.EventID, res.GenesisEventID)
	}
	if env.OriginSequence != config.FirstSequence || env.OriginGeneration != config.FirstGeneration {
		t.Fatalf("bad genesis sequencing: %+v", env)
	}
	if pl.InitialDeviceCert.DeviceID != res.DeviceID {
		t.Fatal("cert device mismatch")
	}
}

// Acceptance: portable/device-local separation — no key material under the
// portable dir, and the device dir holds the keys with 0600.
func TestNoKeyMaterialUnderPortableDir(t *testing.T) {
	opts := testOpts(t, VolumeEncrypted, false)
	res, err := Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}

	err = filepath.WalkDir(opts.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".key") {
			t.Errorf("key file under portable dir: %s", path)
		}
		blob, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(blob), "private_key_b64") {
			t.Errorf("private key material under portable dir: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{config.DeviceKeyName, config.RootKeyName} {
		p := filepath.Join(res.DeviceDir, name)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
		if fi.Mode().Perm() != config.KeyFilePerm {
			t.Errorf("%s perm %o, want %o", p, fi.Mode().Perm(), config.KeyFilePerm)
		}
	}
}

// Acceptance: unencrypted volume refuses start without the flag; unknown
// fails closed; the flag persists device-local and the override warns.
func TestEncryptionFailClosed(t *testing.T) {
	for _, status := range []VolumeStatus{VolumeUnencrypted, VolumeUnknown} {
		opts := testOpts(t, status, false)
		if _, err := Initialize(opts); err == nil {
			t.Fatalf("init succeeded on %s volume without override", status)
		}
	}
}

func TestAllowUnencryptedPersistsAndWarns(t *testing.T) {
	opts := testOpts(t, VolumeUnencrypted, true)
	var initLog strings.Builder
	opts.Out = &initLog
	if _, err := Initialize(opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(initLog.String(), "WARNING") {
		t.Fatal("no warning printed for allowed-unencrypted init")
	}

	loaded, err := Load(opts.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Device.AllowUnencrypted {
		t.Fatal("allow_unencrypted not persisted device-local")
	}
	// every subsequent start must warn but proceed
	var startLog strings.Builder
	if err := loaded.StartupCheck(fakeChecker{VolumeUnencrypted}, &startLog); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(startLog.String(), "WARNING") {
		t.Fatal("startup with persisted override did not warn")
	}
}

func TestReinitRefused(t *testing.T) {
	opts := testOpts(t, VolumeEncrypted, false)
	if _, err := Initialize(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(opts); err == nil {
		t.Fatal("re-init over an initialized dir succeeded")
	}
}

// Restore case: portable data present, device-local identity missing →
// refuse with a message pointing at the adopt path.
func TestRestoredDataWithoutIdentityRefused(t *testing.T) {
	opts := testOpts(t, VolumeEncrypted, false)
	res, err := Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(res.DeviceDir); err != nil {
		t.Fatal(err)
	}
	_, err = Load(opts.Dir)
	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("want restore-detected error, got %v", err)
	}
}

func TestParseFdesetup(t *testing.T) {
	cases := []struct {
		out  string
		want VolumeStatus
	}{
		{"FileVault is On.\n", VolumeEncrypted},
		{"FileVault is Off.\n", VolumeUnencrypted},
		{"FileVault is On, but no users are enabled.\n", VolumeEncrypted},
		{"something unexpected", VolumeUnknown},
	}
	for _, c := range cases {
		got, _, err := parseFdesetup(c.out)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("parseFdesetup(%q) = %s, want %s", c.out, got, c.want)
		}
	}
}
