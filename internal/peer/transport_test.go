package peer

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"github.com/ggoosen/cairn/internal/config"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// memTransport is an in-memory transport backed by net.Pipe — no sockets, no
// addressing rules. It exists to prove the P3-1 contract: the authenticated
// handshake (and everything above it) runs IDENTICALLY over any Transport, and
// a transport that imposes no addressing and trusts every endpoint STILL cannot
// admit a non-member — membership is the handshake's job, never the transport's.
// This is the exact contract iroh (P3-4) will satisfy.
type memAddr string

func (memAddr) Network() string  { return "mem" }
func (a memAddr) String() string { return string(a) }

type memListener struct {
	addr   string
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, errors.New("mem listener closed")
	}
}
func (l *memListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (l *memListener) Addr() net.Addr { return memAddr(l.addr) }

type memTransport struct {
	mu        sync.Mutex
	listeners map[string]*memListener
}

func newMemTransport() *memTransport {
	return &memTransport{listeners: map[string]*memListener{}}
}

func (t *memTransport) Name() string              { return "mem" }
func (t *memTransport) ValidateAddr(string) error { return nil } // no addressing rules at all
func (t *memTransport) LocalAddr() (string, bool) { return "", false }

func (t *memTransport) Listen(addr string) (net.Listener, error) {
	l := &memListener{addr: addr, ch: make(chan net.Conn), closed: make(chan struct{})}
	t.mu.Lock()
	t.listeners[addr] = l
	t.mu.Unlock()
	return l, nil
}

func (t *memTransport) Dial(addr string, timeout time.Duration) (net.Conn, error) {
	t.mu.Lock()
	l, ok := t.listeners[addr]
	t.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no mem listener at %s", addr)
	}
	client, server := net.Pipe()
	select {
	case l.ch <- server:
		return client, nil
	case <-l.closed:
		client.Close()
		return nil, errors.New("mem listener closed")
	case <-time.After(timeout):
		client.Close()
		return nil, errors.New("mem dial timeout")
	}
}

// P3-1: the full mutual cert handshake succeeds over a transport that has no
// tailnet, no sockets, and no addressing — the seam is honest.
func TestP31HandshakeOverArbitraryTransport(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	a := newIdent(t, "mesh-1", "device-a", trust, true)
	b := newIdent(t, "mesh-1", "device-b", trust, true)

	warn := &lockedBuf{}
	srv, err := NewServerWithTransport(tr, "node-a", a, trust, warn)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })

	got, err := PingWithTransport(tr, "node-a", b, trust)
	if err != nil {
		t.Fatalf("handshake over mem transport failed: %v", err)
	}
	if got != "device-a" {
		t.Fatalf("responder identity: %q", got)
	}
}

// P3-1: a transport that trusts every endpoint (mem) still cannot grant
// membership — an un-enrolled peer is refused by the app-layer handshake, and
// the refusal logs the presented identity (R27). Endpoint identity ≠ mesh
// authorization holds regardless of transport.
func TestP31TransportDoesNotGrantMembership(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	a := newIdent(t, "mesh-1", "device-a", trust, true)
	warn := &lockedBuf{}
	srv, err := NewServerWithTransport(tr, "node-a", a, trust, warn)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })

	stranger := newIdent(t, "mesh-1", "device-stranger", trust, false)
	if _, err := PingWithTransport(tr, "node-a", stranger, trust); err == nil {
		t.Fatal("un-enrolled peer accepted over a trusting transport")
	}
	if !strings.Contains(warn.String(), "device-stranger") || !strings.Contains(warn.String(), "not enrolled") {
		t.Fatalf("refusal did not log the presented identity:\n%s", warn.String())
	}
}

// P3-1: DefaultTransport preserves the P1 tailnet addressing semantics — the
// address guard moved behind the interface but did not change.
func TestP31DefaultTransportPreservesTailnetSemantics(t *testing.T) {
	if DefaultTransport.Name() != "tcp-tailnet" {
		t.Fatalf("default transport name: %q", DefaultTransport.Name())
	}
	if err := DefaultTransport.ValidateAddr("0.0.0.0:9700"); err == nil {
		t.Fatal("default transport accepted 0.0.0.0 (must be tailnet-only)")
	}
	if err := DefaultTransport.ValidateAddr("100.64.0.1:9700"); err != nil {
		t.Fatalf("default transport refused a tailnet address: %v", err)
	}
}

// P3-2c: a node with pairing disabled (OnPair == nil) refuses pairing dialers
// cleanly rather than hanging or accepting them.
func TestP32cPairingDisabledRefused(t *testing.T) {
	tr := newMemTransport()
	trust := &fakeTrust{devices: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	a := newIdent(t, "mesh-1", "device-a", trust, true)
	warn := &lockedBuf{}
	srv, err := NewServerWithTransport(tr, "node-a", a, trust, warn)
	if err != nil {
		t.Fatal(err)
	}
	// OnPair deliberately left nil
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })

	_, priv, _ := ed25519.GenerateKey(nil)
	if _, err := PairDialWithTransport(tr, "node-a", "mesh-1", []byte(`{"invite_id":"x"}`), priv); err == nil {
		t.Fatal("pairing accepted on a node with pairing disabled")
	}
}

// P3-4: transport selection resolves the default and refuses iroh instructively.
func TestP34TransportByName(t *testing.T) {
	for _, name := range []string{"", config.TransportTCPTailnet} {
		tr, err := TransportByName(name)
		if err != nil || tr == nil || tr.Name() != "tcp-tailnet" {
			t.Fatalf("TransportByName(%q) = (%v, %v)", name, tr, err)
		}
	}
	_, err := TransportByName(config.TransportIroh)
	if err == nil || !strings.Contains(err.Error(), "iroh") || !strings.Contains(err.Error(), "deferred") {
		t.Fatalf("iroh not refused instructively: %v", err)
	}
	if _, err := TransportByName("bogus"); err == nil {
		t.Fatal("unknown transport accepted")
	}
}
