package identity

// P3-2a — pairing invitation mint + verification core. A minted invitation must
// verify from genesis, carry a root-signed cert whose private key it holds, and
// enforce expiry; every tamper must be caught.

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// mintTestMesh initializes a mesh under a fresh device-state base, moves the
// root key out to a "restored" path (offline-storage simulation), and returns
// the portable dir + restored root key path.
func mintTestMesh(t *testing.T) (dir, rootRestored string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", base)
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	dir = filepath.Join(t.TempDir(), "cairn")
	if _, err := Initialize(InitOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	rootRestored = filepath.Join(t.TempDir(), "restored-root.key")
	rootLocal := filepath.Join(loaded.DeviceDir, config.RootKeyName)
	blob, err := os.ReadFile(rootLocal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootRestored, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, rootRestored
}

func TestP32MintAndVerifyRoundTrip(t *testing.T) {
	dir, root := mintTestMesh(t)
	now := time.Now()
	inv, err := MintPairingInvitation(MintPairingInvitationOptions{
		Dir: dir, RootKeyPath: root, DisplayName: "laptop",
		Now: func() time.Time { return now }, Out: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Cert.RootSignature == "" || inv.DevicePrivB64 == "" || inv.InviteID == "" {
		t.Fatalf("invitation incomplete: %+v", inv)
	}
	if len(inv.Chain) < 1 {
		t.Fatal("invitation carries no identity chain")
	}

	trust, priv, err := VerifyPairingInvitation(inv, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	// the private key in the token must match the certified public key
	if priv == nil || len(priv) != 64 {
		t.Fatalf("verify did not return the device key")
	}
	// the device is NOT yet a member — it is admitted on arrival (append-on-arrival)
	if trust.Member(inv.Cert.DeviceID) {
		t.Fatal("paired device is a member at mint time (must be admitted on arrival)")
	}
	// but the cert IS root-signed against the mesh root the chain proved
	if err := inv.Cert.Verify(trust.RootPub); err != nil {
		t.Fatalf("cert not root-signed against the verified root: %v", err)
	}
}

func TestP32ExpiredInvitationRefused(t *testing.T) {
	dir, root := mintTestMesh(t)
	mint := time.Now()
	inv, err := MintPairingInvitation(MintPairingInvitationOptions{
		Dir: dir, RootKeyPath: root, DisplayName: "laptop",
		Now: func() time.Time { return mint }, Out: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	// just inside the window: OK
	if _, _, err := VerifyPairingInvitation(inv, mint.Add(config.PairingInviteTTL-time.Second)); err != nil {
		t.Fatalf("invitation refused inside its window: %v", err)
	}
	// past expiry: refused
	if _, _, err := VerifyPairingInvitation(inv, mint.Add(config.PairingInviteTTL+time.Second)); err == nil {
		t.Fatal("expired invitation accepted")
	}
}

func TestP32TamperedInvitationRefused(t *testing.T) {
	dir, root := mintTestMesh(t)
	now := time.Now()
	mk := func() *PairingInvitation {
		inv, err := MintPairingInvitation(MintPairingInvitationOptions{
			Dir: dir, RootKeyPath: root, DisplayName: "laptop",
			Now: func() time.Time { return now }, Out: io.Discard,
		})
		if err != nil {
			t.Fatal(err)
		}
		return inv
	}
	at := now.Add(time.Minute)

	// swapped private key (attacker substitutes their own key for the certified one)
	t.Run("mismatched private key", func(t *testing.T) {
		inv := mk()
		_, other, _ := GenerateKey()
		inv.DevicePrivB64 = base64.StdEncoding.EncodeToString(other)
		if _, _, err := VerifyPairingInvitation(inv, at); err == nil {
			t.Fatal("accepted an invitation whose private key does not match the cert")
		}
	})

	// forged cert (attacker rewrites the device id, invalidating the root signature)
	t.Run("tampered cert", func(t *testing.T) {
		inv := mk()
		inv.Cert.DeviceID = "0190dead-0000-7000-8000-00000000beef"
		if _, _, err := VerifyPairingInvitation(inv, at); err == nil {
			t.Fatal("accepted an invitation with a tampered cert")
		}
	})

	// truncated chain (cannot verify from genesis)
	t.Run("broken chain", func(t *testing.T) {
		inv := mk()
		inv.Chain = inv.Chain[:0]
		if _, _, err := VerifyPairingInvitation(inv, at); err == nil {
			t.Fatal("accepted an invitation with no verifiable chain")
		}
	})

	// wrong version
	t.Run("bad version", func(t *testing.T) {
		inv := mk()
		inv.Version = 2
		if _, _, err := VerifyPairingInvitation(inv, at); err == nil {
			t.Fatal("accepted an unsupported invitation version")
		}
	})
}

func TestP32MintRejectsWrongRootKey(t *testing.T) {
	dir, _ := mintTestMesh(t)
	// a root key from a DIFFERENT mesh must be refused
	otherDir, otherRoot := mintTestMesh(t)
	_ = otherDir
	if _, err := MintPairingInvitation(MintPairingInvitationOptions{
		Dir: dir, RootKeyPath: otherRoot, DisplayName: "laptop", Out: io.Discard,
	}); err == nil {
		t.Fatal("minted an invitation with a foreign root key")
	}
}
