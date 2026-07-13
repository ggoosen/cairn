package peer

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"sync"
	"testing"
)

type fakeTrust struct {
	devices map[string]ed25519.PublicKey
	revoked map[string]bool
}

func (f fakeTrust) Member(id string) bool { _, ok := f.devices[id]; return ok }
func (f fakeTrust) Revoked(id string) bool {
	return f.revoked[id]
}
func (f fakeTrust) DevicePub(id string) (ed25519.PublicKey, bool) {
	pub, ok := f.devices[id]
	return pub, ok
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newIdent(t *testing.T, cairnID, deviceID string, trust *fakeTrust, enroll bool) Identity {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if enroll {
		trust.devices[deviceID] = pub
	}
	return Identity{CairnID: cairnID, DeviceID: deviceID, Priv: priv}
}

func startServer(t *testing.T, ident Identity, trust Trust) (*Server, *lockedBuf) {
	t.Helper()
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	warn := &lockedBuf{}
	srv, err := NewServer("127.0.0.1:0", ident, trust, warn)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return srv, warn
}

func TestN5MutualHandshake(t *testing.T) {
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	a := newIdent(t, "mesh-1", "device-a", trust, true)
	b := newIdent(t, "mesh-1", "device-b", trust, true)
	srv, _ := startServer(t, a, trust)

	got, err := Ping(srv.Addr(), b, trust)
	if err != nil {
		t.Fatal(err)
	}
	if got != "device-a" {
		t.Fatalf("responder identity: %q", got)
	}
}

func TestN5UnenrolledAndRevokedRefusedAndLogged(t *testing.T) {
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	a := newIdent(t, "mesh-1", "device-a", trust, true)
	srv, warn := startServer(t, a, trust)

	// un-enrolled: valid transport, no membership (R27)
	stranger := newIdent(t, "mesh-1", "device-stranger", trust, false)
	if _, err := Ping(srv.Addr(), stranger, trust); err == nil {
		t.Fatal("un-enrolled peer accepted")
	}
	if !strings.Contains(warn.String(), "device-stranger") || !strings.Contains(warn.String(), "not enrolled") {
		t.Fatalf("refusal did not log the presented identity:\n%s", warn.String())
	}

	// revoked: enrolled key, revoked id
	revoked := newIdent(t, "mesh-1", "device-old", trust, true)
	trust.revoked["device-old"] = true
	if _, err := Ping(srv.Addr(), revoked, trust); err == nil {
		t.Fatal("revoked peer accepted")
	}
	if !strings.Contains(warn.String(), "device-old") || !strings.Contains(warn.String(), "REVOKED") {
		t.Fatalf("revoked refusal did not log the identity:\n%s", warn.String())
	}

	// wrong mesh
	other := newIdent(t, "mesh-2", "device-x", trust, true)
	if _, err := Ping(srv.Addr(), other, trust); err == nil {
		t.Fatal("cross-mesh peer accepted")
	}

	// impostor: claims an enrolled id without its key
	impostor := newIdent(t, "mesh-1", "device-a", trust, false) // fresh key, admitted id
	if _, err := Ping(srv.Addr(), impostor, trust); err == nil {
		t.Fatal("impostor with wrong key accepted")
	}
	if !strings.Contains(warn.String(), "invalid handshake signature") {
		t.Fatalf("impostor refusal not logged:\n%s", warn.String())
	}
}

func TestN5DialerVerifiesResponder(t *testing.T) {
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	// the SERVER is not enrolled in the dialer's trust view
	rogue := newIdent(t, "mesh-1", "device-rogue", trust, false)
	srv, _ := startServer(t, rogue, trust)
	b := newIdent(t, "mesh-1", "device-b", trust, true)
	if _, err := Ping(srv.Addr(), b, trust); err == nil ||
		!strings.Contains(err.Error(), "responder failed authentication") {
		t.Fatalf("dialer accepted an unauthenticated responder: %v", err)
	}
}

func TestN5ListenAddrGuard(t *testing.T) {
	for _, bad := range []string{
		"0.0.0.0:9700", "[::]:9700", "8.8.8.8:9700", "192.168.1.5:9700", "localhost:9700",
	} {
		if err := ValidateListenAddr(bad); err == nil {
			t.Errorf("%s accepted (must be tailnet-only)", bad)
		}
	}
	for _, good := range []string{"100.64.0.1:9700", "100.100.20.3:9700", "[fd7a:115c:a1e0::1]:9700"} {
		if err := ValidateListenAddr(good); err != nil {
			t.Errorf("%s refused: %v", good, err)
		}
	}
	// loopback is env-gated dev mode
	if err := ValidateListenAddr("127.0.0.1:9700"); err == nil {
		t.Error("loopback accepted without the env acknowledgement")
	}
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	if err := ValidateListenAddr("127.0.0.1:9700"); err != nil {
		t.Errorf("loopback with env refused: %v", err)
	}
}

// FIX-G2 (R44): DetectTailnetIP finds a tailnet CGNAT address when present and
// declines otherwise; under CAIRN_SYNC_ALLOW_LOOPBACK it falls back to
// loopback so the auto path is exercisable in dev/test without a real tailnet.
// It NEVER returns an unspecified/0.0.0.0 address.
func TestG2DetectTailnetIP(t *testing.T) {
	// Without the dev acknowledgement and (typically) no tailnet interface in
	// the test sandbox, detection declines rather than binding something wrong.
	if ip, ok := DetectTailnetIP(); ok {
		if ip == "0.0.0.0" || ip == "::" {
			t.Fatalf("detected an unspecified address %q", ip)
		}
	}
	// Dev/test fallback: loopback becomes an acceptable auto target.
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	ip, ok := DetectTailnetIP()
	if !ok {
		t.Fatal("loopback fallback not offered under CAIRN_SYNC_ALLOW_LOOPBACK")
	}
	if ip != "127.0.0.1" && !isTailnetIPStr(t, ip) {
		t.Fatalf("fallback returned %q, want a tailnet ip or 127.0.0.1", ip)
	}
}

func isTailnetIPStr(t *testing.T, s string) bool {
	t.Helper()
	return ValidateListenAddr(s+":9700") == nil
}
