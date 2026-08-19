package peer

// P3-5 — mutual pairing authentication. These tests exercise the wire itself
// (no daemon, no identity chain): the dialer must authenticate the inviting node
// against the invitation's trust BEFORE handing over its credential, and every
// way that can fail must refuse rather than downgrade.

import (
	"bufio"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// pairingServer starts a node whose OnPair accepts the given device key and
// records what it was asked to admit.
type pairRecorder struct {
	mu       sync.Mutex
	payloads [][]byte
	admits   int
}

func (p *pairRecorder) seen() ([][]byte, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.payloads))
	copy(out, p.payloads)
	return out, p.admits
}

func startPairServer(t *testing.T, tr Transport, addr string, ident Identity, trust Trust, devicePub ed25519.PublicKey) (*Server, *pairRecorder, *lockedBuf) {
	t.Helper()
	warn := &lockedBuf{}
	srv, err := NewServerWithTransport(tr, addr, ident, trust, warn)
	if err != nil {
		t.Fatal(err)
	}
	rec := &pairRecorder{}
	srv.OnPair = func(payload []byte) (ed25519.PublicKey, func() (string, error), error) {
		rec.mu.Lock()
		rec.payloads = append(rec.payloads, append([]byte(nil), payload...))
		rec.mu.Unlock()
		return devicePub, func() (string, error) {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			rec.admits++
			return "event-1", nil
		}, nil
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return srv, rec, warn
}

// The happy path: both directions authenticate and the device is admitted.
func TestP35PairingAuthenticatesBothDirections(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	host := newIdent(t, "mesh-1", "device-host", trust, true)
	newPub, newPriv, _ := ed25519.GenerateKey(nil)
	_, rec, _ := startPairServer(t, tr, "node-a", host, trust, newPub)

	ev, err := PairDialWithTransport(tr, "node-a", "mesh-1", []byte(`{"invite_id":"i1"}`), newPriv, trust)
	if err != nil {
		t.Fatalf("mutual pairing failed: %v", err)
	}
	if ev != "event-1" {
		t.Fatalf("event id: %q", ev)
	}
	if _, admits := rec.seen(); admits != 1 {
		t.Fatalf("admits: %d", admits)
	}
}

// The property the whole change exists for: a listener that cannot prove mesh
// membership NEVER receives the credential. The dialer must abort after the
// server's proof and before writing its payload.
func TestP35RogueNodeNeverSeesTheCredential(t *testing.T) {
	tr := newMemTransport()
	// dialerTrust knows the real mesh; the rogue's device is NOT in it.
	dialerTrust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	newIdent(t, "mesh-1", "device-host", dialerTrust, true) // the real node, absent from the wire

	rogueTrust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	rogue := newIdent(t, "mesh-1", "device-rogue", rogueTrust, true)
	_, newPriv, _ := ed25519.GenerateKey(nil)
	rpub, _, _ := ed25519.GenerateKey(nil)
	_, rec, _ := startPairServer(t, tr, "rogue", rogue, rogueTrust, rpub)

	_, err := PairDialWithTransport(tr, "rogue", "mesh-1", []byte(`{"cert":"SECRET","invite_id":"i1"}`), newPriv, dialerTrust)
	if err == nil {
		t.Fatal("paired with a node that is not a mesh member")
	}
	if !strings.Contains(err.Error(), "NOT handing it the invitation") {
		t.Fatalf("refusal does not name the reason: %v", err)
	}
	payloads, admits := rec.seen()
	if len(payloads) != 0 {
		t.Fatalf("credential leaked to a rogue node: %q", payloads)
	}
	if admits != 0 {
		t.Fatalf("rogue admitted %d devices", admits)
	}
}

// A REVOKED inviting node is refused even though it is in the chain (R27's
// second half — the same rule the N5 handshake applies).
func TestP35RevokedNodeRefused(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	host := newIdent(t, "mesh-1", "device-host", trust, true)
	trust.revoked["device-host"] = true
	newPub, newPriv, _ := ed25519.GenerateKey(nil)
	_, rec, _ := startPairServer(t, tr, "node-a", host, trust, newPub)

	if _, err := PairDialWithTransport(tr, "node-a", "mesh-1", []byte(`{"invite_id":"i1"}`), newPriv, trust); err == nil {
		t.Fatal("paired with a REVOKED node")
	}
	if payloads, _ := rec.seen(); len(payloads) != 0 {
		t.Fatal("credential handed to a revoked node")
	}
}

// nil trust is refused outright: mutual authentication has no opt-out.
func TestP35NilTrustRefused(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	host := newIdent(t, "mesh-1", "device-host", trust, true)
	newPub, newPriv, _ := ed25519.GenerateKey(nil)
	_, rec, _ := startPairServer(t, tr, "node-a", host, trust, newPub)

	if _, err := PairDialWithTransport(tr, "node-a", "mesh-1", []byte(`{"invite_id":"i1"}`), newPriv, nil); err == nil {
		t.Fatal("pairing proceeded with no trust to authenticate the peer against")
	}
	if payloads, _ := rec.seen(); len(payloads) != 0 {
		t.Fatal("credential sent despite no trust")
	}
}

// A node whose signature does not verify (right device id, wrong key) is
// refused — the identity claim alone is not enough.
func TestP35ForgedServerSignatureRefused(t *testing.T) {
	tr := newMemTransport()
	// the dialer's trust binds device-host to a DIFFERENT key than the one the
	// listener signs with: an impostor claiming the real node's id.
	realPub, _, _ := ed25519.GenerateKey(nil)
	dialerTrust := &fakeTrust{devices: map[string]ed25519.PublicKey{"device-host": realPub}, revoked: map[string]bool{}}

	impostorTrust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	impostor := newIdent(t, "mesh-1", "device-host", impostorTrust, true)
	newPub, newPriv, _ := ed25519.GenerateKey(nil)
	_, rec, _ := startPairServer(t, tr, "node-a", impostor, impostorTrust, newPub)

	_, err := PairDialWithTransport(tr, "node-a", "mesh-1", []byte(`{"invite_id":"i1"}`), newPriv, dialerTrust)
	if err == nil {
		t.Fatal("paired with an impostor holding the right device id but the wrong key")
	}
	if !strings.Contains(err.Error(), "invalid handshake signature") {
		t.Fatalf("refusal reason: %v", err)
	}
	if payloads, _ := rec.seen(); len(payloads) != 0 {
		t.Fatal("credential handed to an impostor")
	}
}

// A node on another mesh is refused before the credential moves.
func TestP35WrongCairnRefused(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	host := newIdent(t, "mesh-OTHER", "device-host", trust, true)
	newPub, newPriv, _ := ed25519.GenerateKey(nil)
	_, rec, warn := startPairServer(t, tr, "node-a", host, trust, newPub)

	if _, err := PairDialWithTransport(tr, "node-a", "mesh-1", []byte(`{"invite_id":"i1"}`), newPriv, trust); err == nil {
		t.Fatal("paired across meshes")
	}
	if !strings.Contains(warn.String(), "claims cairn") {
		t.Fatalf("refusal not logged: %s", warn.String())
	}
	if payloads, _ := rec.seen(); len(payloads) != 0 {
		t.Fatal("credential sent to a node on another mesh")
	}
}

// The server refuses to act as a signing oracle for a dialer it will not serve:
// a wrong-cairn or pairing-disabled dialer gets a refusal with NO signature.
func TestP35ServerSignsNothingForARefusedDialer(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	host := newIdent(t, "mesh-1", "device-host", trust, true)
	srv, err := NewServerWithTransport(tr, "node-a", host, trust, &lockedBuf{})
	if err != nil {
		t.Fatal(err)
	}
	// OnPair left nil: pairing disabled
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })

	conn, err := tr.Dial("node-a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := writeMsg(conn, hello{V: 1, CairnID: "mesh-1", Mode: "pair", Nonce: "n"}); err != nil {
		t.Fatal(err)
	}
	m, err := readPair(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	if m.Sig != "" || m.Nonce != "" {
		t.Fatalf("a refused dialer received a signature: %+v", m)
	}
	if m.OK || m.Err == "" {
		t.Fatalf("refusal not stated: %+v", m)
	}
}

// A pre-P3-5 dialer (hello with no nonce, then the credential immediately) is
// refused INSTRUCTIVELY and never admitted — the protocol is never downgraded.
// Run over TCP, not the synchronous mem pipe: a legacy dialer writes its payload
// before reading, which only a buffered transport can absorb.
func TestP35LegacyDialerRefusedNotDowngraded(t *testing.T) {
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	host := newIdent(t, "mesh-1", "device-host", trust, true)
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	newPub, _, _ := ed25519.GenerateKey(nil)
	srv, rec, warn := startPairServer(t, DefaultTransport, "127.0.0.1:0", host, trust, newPub)

	conn, err := net.DialTimeout("tcp", srv.Addr(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	// the v1 wire, verbatim: hello{mode:pair} with no nonce, then the payload
	if err := writeMsg(conn, hello{V: 1, CairnID: "mesh-1", Mode: "pair"}); err != nil {
		t.Fatal(err)
	}
	if err := writePair(conn, pairMsg{Payload: json.RawMessage(`{"invite_id":"i1"}`)}); err != nil {
		t.Fatal(err)
	}
	m, err := readPair(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("legacy dialer got no refusal (deadlock?): %v", err)
	}
	if m.OK {
		t.Fatal("legacy pairing accepted — the protocol was downgraded")
	}
	if !strings.Contains(m.Err, "MUTUAL") || !strings.Contains(m.Err, "upgrade") {
		t.Fatalf("refusal is not instructive: %q", m.Err)
	}
	if _, admits := rec.seen(); admits != 0 {
		t.Fatal("legacy dialer was admitted")
	}
	if !strings.Contains(warn.String(), "pre-mutual") {
		t.Fatalf("refusal not logged: %s", warn.String())
	}
}

// A dialer that presents the credential but signs the key-possession challenge
// with the wrong key is still refused (the pre-existing invariant, unchanged by
// the added direction).
func TestP35WrongDeviceKeyStillRefused(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	host := newIdent(t, "mesh-1", "device-host", trust, true)
	certPub, _, _ := ed25519.GenerateKey(nil) // the certified key
	_, wrongPriv, _ := ed25519.GenerateKey(nil)
	_, rec, _ := startPairServer(t, tr, "node-a", host, trust, certPub)

	if _, err := PairDialWithTransport(tr, "node-a", "mesh-1", []byte(`{"invite_id":"i1"}`), wrongPriv, trust); err == nil {
		t.Fatal("admitted a dialer that does not hold the device key")
	}
	if _, admits := rec.seen(); admits != 0 {
		t.Fatal("a failed key-possession challenge still admitted")
	}
}

// The dialer's proof is bound to the server device and BOTH nonces: a signature
// captured from one session cannot be replayed into another. Exercised by
// signing the old (nonce-only) transcript and watching the server refuse.
func TestP35DialerProofIsSessionBound(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	host := newIdent(t, "mesh-1", "device-host", trust, true)
	devPub, devPriv, _ := ed25519.GenerateKey(nil)
	_, rec, _ := startPairServer(t, tr, "node-a", host, trust, devPub)

	conn, err := tr.Dial("node-a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(conn)
	if err := writeMsg(conn, hello{V: 1, CairnID: "mesh-1", Mode: "pair", Nonce: "dialer-nonce"}); err != nil {
		t.Fatal(err)
	}
	ch, err := readPair(r)
	if err != nil {
		t.Fatal(err)
	}
	// sign the server nonce WITHOUT binding the dialer nonce / server device —
	// i.e. the v1 transcript shape.
	stale := ed25519.Sign(devPriv, []byte(config.PairingHelloDomain+"|mesh-1|"+ch.Nonce))
	if err := writePair(conn, pairMsg{
		Payload: json.RawMessage(`{"invite_id":"i1"}`),
		Sig:     base64.StdEncoding.EncodeToString(stale),
	}); err != nil {
		t.Fatal(err)
	}
	verdict, err := readPair(r)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.OK {
		t.Fatal("a proof over the old, unbound transcript was accepted")
	}
	if _, admits := rec.seen(); admits != 0 {
		t.Fatal("unbound proof admitted a device")
	}
}

// The inviting node's proof is bound to the DIALER's nonce, so it cannot be
// harvested and replayed: a rogue that records a genuine proof (by dialing the
// real node itself) and replays it verbatim to a joining node is refused, and
// the credential still never moves.
func TestP35ServerProofCannotBeReplayed(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	host := newIdent(t, "mesh-1", "device-host", trust, true)
	newPub, newPriv, _ := ed25519.GenerateKey(nil)
	startPairServer(t, tr, "real", host, trust, newPub)

	// the rogue harvests a genuine proof from the real node
	harvest, err := tr.Dial("real", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer harvest.Close()
	harvest.SetDeadline(time.Now().Add(2 * time.Second))
	if err := writeMsg(harvest, hello{V: 1, CairnID: "mesh-1", Mode: "pair", Nonce: "rogue-nonce"}); err != nil {
		t.Fatal(err)
	}
	stolen, err := readPair(bufio.NewReader(harvest))
	if err != nil {
		t.Fatal(err)
	}
	if stolen.Sig == "" {
		t.Fatalf("harvest did not capture a proof: %+v", stolen)
	}

	// the rogue listens and replays it to a joining node
	ln, err := tr.Listen("rogue")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotPayload := make(chan bool, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		if _, rerr := readMsg(r); rerr != nil {
			return
		}
		_ = writePair(conn, *stolen)
		conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
		_, rerr := readPair(r)
		gotPayload <- rerr == nil
	}()

	if _, err := PairDialWithTransport(tr, "rogue", "mesh-1", []byte(`{"cert":"SECRET"}`), newPriv, trust); err == nil {
		t.Fatal("a replayed proof from another session was accepted")
	}
	select {
	case leaked := <-gotPayload:
		if leaked {
			t.Fatal("credential handed to a replaying rogue")
		}
	case <-time.After(2 * time.Second):
	}
}
