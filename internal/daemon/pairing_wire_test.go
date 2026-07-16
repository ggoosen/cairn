package daemon_test

// P3-2c — the pairing handshake end to end over the real (loopback) transport:
// a listening node mints an invitation, a new node pairs, and the device.add
// lands durably on the inviting node. Single-use and a bad key are refused.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/peer"
)

// initCairnLoopback inits a cairn whose sync listener binds an EPHEMERAL
// loopback port (127.0.0.1:0), so the pairing-wire tests never contend with a
// real cairn daemon holding the tailnet :9700 on the dev box.
func initCairnLoopback(t *testing.T) string {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	dir := filepath.Join(t.TempDir(), "cairn")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dir, SyncListen: "127.0.0.1:0", Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// pairPayload is the opaque invite the dialer presents (no private key).
func pairPayload(t *testing.T, inv *identity.PairingInvitation) []byte {
	t.Helper()
	blob, err := json.Marshal(map[string]any{"cert": inv.Cert, "invite_id": inv.InviteID})
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

func invitePriv(t *testing.T, inv *identity.PairingInvitation) ed25519.PrivateKey {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(inv.DevicePrivB64)
	if err != nil {
		t.Fatal(err)
	}
	return ed25519.PrivateKey(raw)
}

func TestP32cPairingHandshakeAdmitsRemotely(t *testing.T) {
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	dir := initCairnLoopback(t)
	warn := &syncBuf{}
	_, cancel, addr := runListeningDaemon(t, dir, warn)
	defer cancel()

	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cairnID := loaded.Portable.CairnID
	inv := mintInvite(t, dir, time.Now())

	// a new node pairs over the wire
	evID, err := peer.PairDial(addr, cairnID, pairPayload(t, inv), invitePriv(t, inv))
	if err != nil {
		t.Fatalf("pair dial: %v", err)
	}
	if evID == "" {
		t.Fatal("pairing returned no device.add id")
	}

	// durable on the inviting node
	trust, err := identity.MeshTrust(fsx.OS{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !trust.Member(inv.Cert.DeviceID) {
		t.Fatal("paired device not admitted in the inviting node's log")
	}

	// HARD single-use: a second pairing with the same invite is refused
	if _, err := peer.PairDial(addr, cairnID, pairPayload(t, inv), invitePriv(t, inv)); err == nil {
		t.Fatal("second pairing with the same invitation accepted")
	}
}

func TestP32cPairingRejectsWrongKey(t *testing.T) {
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	dir := initCairnLoopback(t)
	warn := &syncBuf{}
	_, cancel, addr := runListeningDaemon(t, dir, warn)
	defer cancel()

	loaded, _ := identity.Load(dir)
	inv := mintInvite(t, dir, time.Now())

	// present the real (root-signed) cert but answer the challenge with a
	// DIFFERENT key — key-possession must fail.
	_, wrongPriv, _ := ed25519.GenerateKey(nil)
	if _, err := peer.PairDial(addr, loaded.Portable.CairnID, pairPayload(t, inv), wrongPriv); err == nil {
		t.Fatal("pairing accepted a dialer that does not hold the device key")
	}
	// nothing was admitted
	trust, _ := identity.MeshTrust(fsx.OS{}, dir)
	if trust.Member(inv.Cert.DeviceID) {
		t.Fatal("a failed key-possession challenge still admitted the device")
	}
}
