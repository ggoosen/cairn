package daemon_test

// P3-2b — append-on-arrival admission. A live daemon admits a pre-signed pairing
// credential exactly once, refuses expired/tampered/foreign certs, and the
// device.add it appends is durable (the recovered mesh trust shows the device).

import (
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
)

func mintInvite(t *testing.T, dir string, now time.Time) *identity.PairingInvitation {
	t.Helper()
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	rootKey := filepath.Join(loaded.DeviceDir, config.RootKeyName)
	inv, err := identity.MintPairingInvitation(identity.MintPairingInvitationOptions{
		Dir: dir, RootKeyPath: rootKey, DisplayName: "laptop",
		Now: func() time.Time { return now }, Out: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestP32bAdmitPairedDeviceDurableAndSingleUse(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	inv := mintInvite(t, dir, time.Now())

	evID, err := d.AdmitPairedDevice(inv.Cert, inv.InviteID)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if evID == "" {
		t.Fatal("admit returned no event id")
	}

	// durable: the recovered mesh trust now admits the paired device
	trust, err := identity.MeshTrust(fsx.OS{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !trust.Member(inv.Cert.DeviceID) {
		t.Fatal("paired device not admitted in the durable log")
	}

	// HARD single-use: a second admission of the same invite is refused
	if _, err := d.AdmitPairedDevice(inv.Cert, inv.InviteID); err == nil {
		t.Fatal("second admission of the same invitation accepted (must be single-use)")
	}
}

func TestP32bAdmitRejectsExpired(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	// minted more than a TTL ago → expired against the daemon's real clock
	inv := mintInvite(t, dir, time.Now().Add(-config.PairingInviteTTL-time.Minute))
	if _, err := d.AdmitPairedDevice(inv.Cert, inv.InviteID); err == nil {
		t.Fatal("expired invitation admitted")
	}
	// and it left no member behind
	trust, _ := identity.MeshTrust(fsx.OS{}, dir)
	if trust.Member(inv.Cert.DeviceID) {
		t.Fatal("expired invitation admitted a device anyway")
	}
}

func TestP32bAdmitRejectsTamperedAndForgedCerts(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	// tampered: rewrite the device id, invalidating the root signature
	inv := mintInvite(t, dir, time.Now())
	tampered := inv.Cert
	tampered.DeviceID = "0190dead-0000-7000-8000-00000000beef"
	if _, err := d.AdmitPairedDevice(tampered, inv.InviteID); err == nil {
		t.Fatal("admitted a cert with a broken root signature")
	}

	// forged: a cert signed by a NON-root key (an attacker minting their own)
	pub, _, _ := ed25519.GenerateKey(nil)
	_, attackerRoot, _ := ed25519.GenerateKey(nil)
	forged := identity.DeviceCert{
		DeviceID:   "0190beef-0000-7000-8000-00000000dead",
		Pubkey:     base64.StdEncoding.EncodeToString(pub),
		Generation: config.FirstGeneration,
		IssuedAt:   time.Now().UTC().Format(config.WallTimeFormat),
	}
	if err := forged.SignCert(attackerRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AdmitPairedDevice(forged, "0190beef-0000-7000-8000-000000000001"); err == nil {
		t.Fatal("admitted a cert signed by a non-root key")
	}
}
