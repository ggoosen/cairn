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

	"github.com/ggoosen/cairn/internal/config"
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

// inviteTrust is the mesh membership the invitation proves from genesis — what
// the joining node authenticates the INVITING node against (P3-5).
func inviteTrust(t *testing.T, inv *identity.PairingInvitation) *identity.Trust {
	t.Helper()
	trust, _, err := identity.VerifyPairingInvitation(inv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return trust
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
	meshTrust := inviteTrust(t, inv)

	// a new node pairs over the wire
	evID, err := peer.PairDial(addr, cairnID, pairPayload(t, inv), invitePriv(t, inv), meshTrust)
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
	if _, err := peer.PairDial(addr, cairnID, pairPayload(t, inv), invitePriv(t, inv), meshTrust); err == nil {
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
	if _, err := peer.PairDial(addr, loaded.Portable.CairnID, pairPayload(t, inv), wrongPriv, inviteTrust(t, inv)); err == nil {
		t.Fatal("pairing accepted a dialer that does not hold the device key")
	}
	// nothing was admitted
	trust, _ := identity.MeshTrust(fsx.OS{}, dir)
	if trust.Member(inv.Cert.DeviceID) {
		t.Fatal("a failed key-possession challenge still admitted the device")
	}
}

// P3-2d: a device admitted live via pairing can complete the N5 membership
// handshake against the inviting node WITHOUT restarting it — the sync listener
// reads the refreshed trust. Before pairing the same handshake is refused.
func TestP32dPairedDeviceSyncsWithoutRestart(t *testing.T) {
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
	priv := invitePriv(t, inv)
	newIdent := peer.Identity{CairnID: cairnID, DeviceID: inv.Cert.DeviceID, Priv: priv}
	dialerTrust, _, err := identity.VerifyPairingInvitation(inv, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// before pairing: not a member → membership handshake refused
	if _, err := peer.Ping(addr, newIdent, dialerTrust); err == nil {
		t.Fatal("un-paired device completed the membership handshake")
	}

	// pair over the wire
	if _, err := peer.PairDial(addr, cairnID, pairPayload(t, inv), priv, dialerTrust); err != nil {
		t.Fatalf("pair: %v", err)
	}

	// after pairing, WITHOUT restarting the daemon: the handshake now succeeds
	if _, err := peer.Ping(addr, newIdent, dialerTrust); err != nil {
		t.Fatalf("paired device cannot sync without a restart (live trust not refreshed): %v", err)
	}
}

// P3-2f: the full new-node flow. Node A invites; node B (a SEPARATE device-state
// base) installs its identity from the token, pairs over the wire, and can then
// authenticate the mesh via its pairing bootstrap trust — WITHOUT A restarting.
func TestP32fPairJoinInstallAndAuthenticate(t *testing.T) {
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")

	// --- node A: inviting node with a listening daemon ---
	baseA := t.TempDir()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dirA, SyncListen: "127.0.0.1:0", Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	_, cancel, addr := runListeningDaemon(t, dirA, &syncBuf{})
	defer cancel()
	loadedA, err := identity.Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	inv := mintInvite(t, dirA, time.Now())

	// carry the token (exercise encode → decode)
	token, err := identity.EncodeInvitation(inv)
	if err != nil {
		t.Fatal(err)
	}
	inv2, err := identity.DecodeInvitation(token)
	if err != nil {
		t.Fatal(err)
	}

	// --- node B: install from the token under a SEPARATE device-state base ---
	baseB := t.TempDir()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	dirB := filepath.Join(t.TempDir(), "cairnB")
	priv, err := identity.PairJoinInstall(identity.PairJoinOptions{
		Invitation: inv2, Dir: dirB, Now: time.Now, Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("pair join install: %v", err)
	}

	// B's bootstrap trust admits the mesh (the inviting node) but NOT B yet
	deviceDirB, err := config.DeviceStateDir(inv2.CairnID)
	if err != nil {
		t.Fatal(err)
	}
	bt, err := identity.PairingBootstrapTrust(deviceDirB)
	if err != nil {
		t.Fatalf("pairing bootstrap trust: %v", err)
	}
	if !bt.Member(loadedA.Device.DeviceID) {
		t.Fatal("bootstrap trust does not admit the inviting node")
	}
	if bt.Member(inv2.Cert.DeviceID) {
		t.Fatal("bootstrap trust admits self before pairing (device.add is append-on-arrival)")
	}

	// pair over the wire
	payload, err := json.Marshal(map[string]any{"cert": inv2.Cert, "invite_id": inv2.InviteID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.PairDial(addr, inv2.CairnID, payload, priv, bt); err != nil {
		t.Fatalf("pair dial: %v", err)
	}

	// B authenticates the mesh (membership handshake against A) via bootstrap trust
	newIdent := peer.Identity{CairnID: inv2.CairnID, DeviceID: inv2.Cert.DeviceID, Priv: priv}
	if _, err := peer.Ping(addr, newIdent, bt); err != nil {
		t.Fatalf("paired node cannot authenticate the mesh via bootstrap trust: %v", err)
	}

	// re-install is idempotent (safe to retry after a network hiccup)
	if _, err := identity.PairJoinInstall(identity.PairJoinOptions{
		Invitation: inv2, Dir: dirB, Now: time.Now, Out: io.Discard,
	}); err != nil {
		t.Fatalf("pair join install not idempotent: %v", err)
	}
}

// P3-5: an invitation minted by one mesh cannot pair with a node of ANOTHER
// mesh — over the real wire, with two real daemons' worth of identity. The
// refusal is mutual by construction: the node refuses a dialer claiming a cairn
// that is not its own, and the dialer would refuse that node in turn because it
// is absent from the invitation's genesis-verified chain. Nothing is admitted.
func TestP35PairingRefusesForeignMesh(t *testing.T) {
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")

	// --- mesh A: mints an invitation (no daemon needed) ---
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dirA, SyncListen: "127.0.0.1:0", Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	loadedA, err := identity.Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	inv := mintInvite(t, dirA, time.Now())
	trustA := inviteTrust(t, inv)

	// --- mesh B: a completely separate cairn, listening ---
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	dirB := filepath.Join(t.TempDir(), "cairnB")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dirB, SyncListen: "127.0.0.1:0", Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	_, cancel, addrB := runListeningDaemon(t, dirB, &syncBuf{})
	defer cancel()

	if _, err := peer.PairDial(addrB, loadedA.Portable.CairnID, pairPayload(t, inv), invitePriv(t, inv), trustA); err == nil {
		t.Fatal("an invitation for mesh A paired with a node of mesh B")
	}
	trustB, err := identity.MeshTrust(fsx.OS{}, dirB)
	if err != nil {
		t.Fatal(err)
	}
	if trustB.Member(inv.Cert.DeviceID) {
		t.Fatal("mesh B admitted a device from another mesh's invitation")
	}
}
