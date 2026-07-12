// Package peer implements the N5 sync transport skeleton: a TCP listener
// bound to the TAILNET address only, and a mutual application-layer
// handshake (RULINGS.md R27) — device certs chained to the mesh root are
// the authorization; the transport (Tailscale) is never trusted for
// membership. Endpoint identity ≠ mesh authorization.
//
// The handshake authenticates BOTH directions before a single protocol
// byte flows; N6 layers reconciliation on top of an authenticated
// connection. Refusals log the presented identity loudly.
package peer

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// Trust is the membership view the handshake validates against —
// identity.Trust satisfies it, as does the join-grant bootstrap trust.
type Trust interface {
	Member(deviceID string) bool
	Revoked(deviceID string) bool
	DevicePub(deviceID string) (ed25519.PublicKey, bool)
}

// Identity is this node's sync identity.
type Identity struct {
	CairnID  string
	DeviceID string
	Priv     ed25519.PrivateKey
}

type hello struct {
	V        int    `json:"v"`
	CairnID  string `json:"cairn_id"`
	DeviceID string `json:"device_id"`
	Nonce    string `json:"nonce"`
	Sig      string `json:"sig,omitempty"` // over the transcript (responder/final)
	OK       bool   `json:"ok,omitempty"`
}

// transcript binds the signature to both nonces, the direction, and the
// signer's claimed identity under a fixed domain separator.
func transcript(cairnID, signerDevice, nonceMine, nonceTheirs string) []byte {
	return []byte(config.SyncHelloDomain + "|" + cairnID + "|" + signerDevice + "|" + nonceMine + "|" + nonceTheirs)
}

func newNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func writeMsg(w io.Writer, m hello) error {
	blob, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(blob, '\n'))
	return err
}

func readMsg(r *bufio.Reader) (*hello, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var m hello
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// verifyPeer enforces R27 on one presented identity: mesh membership by
// root-chained cert, not endpoint identity; revoked devices refused.
func verifyPeer(trust Trust, cairnID string, m *hello, signed []byte) error {
	if m.V != 1 {
		return fmt.Errorf("unsupported sync protocol v%d", m.V)
	}
	if m.CairnID != cairnID {
		return fmt.Errorf("peer belongs to cairn %s, not %s", m.CairnID, cairnID)
	}
	if trust.Revoked(m.DeviceID) {
		return fmt.Errorf("device %s is REVOKED", m.DeviceID)
	}
	if !trust.Member(m.DeviceID) {
		return fmt.Errorf("device %s is not enrolled in this mesh", m.DeviceID)
	}
	pub, ok := trust.DevicePub(m.DeviceID)
	if !ok {
		return fmt.Errorf("device %s has no registered key", m.DeviceID)
	}
	sig, err := base64.StdEncoding.DecodeString(m.Sig)
	if err != nil || !ed25519.Verify(pub, signed, sig) {
		return fmt.Errorf("device %s presented an invalid handshake signature", m.DeviceID)
	}
	return nil
}

// ValidateListenAddr enforces tailnet-only binding: a specific Tailscale
// CGNAT address (100.64.0.0/10) or its IPv6 range — NEVER 0.0.0.0/::.
// Loopback is allowed ONLY under CAIRN_SYNC_ALLOW_LOOPBACK=1 (dev/test).
func ValidateListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("sync listen address must be host:port: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("sync listen host must be a literal IP (the tailnet address), got %q", host)
	}
	if ip.IsUnspecified() {
		return errors.New("sync listener must bind the tailnet address, NEVER 0.0.0.0/:: (buildpack N5)")
	}
	if ip.IsLoopback() {
		if os.Getenv("CAIRN_SYNC_ALLOW_LOOPBACK") == "1" {
			return nil
		}
		return errors.New("loopback sync binding is a dev/test mode; set CAIRN_SYNC_ALLOW_LOOPBACK=1 to acknowledge")
	}
	ts4 := netip.MustParsePrefix("100.64.0.0/10")       // Tailscale CGNAT range
	ts6 := netip.MustParsePrefix("fd7a:115c:a1e0::/48") // Tailscale IPv6 range
	if ts4.Contains(ip) || ts6.Contains(ip) {
		return nil
	}
	return fmt.Errorf("%s is not a tailnet address (100.64.0.0/10 or fd7a:115c:a1e0::/48) — the sync listener binds the tailnet ONLY", ip)
}

// Server accepts authenticated peer connections.
type Server struct {
	ident Identity
	trust Trust
	warn  io.Writer
	ln    net.Listener
	// OnPeer runs after a successful mutual handshake (N6 attaches the
	// reconciliation protocol here). nil = N5 skeleton: acknowledge + close.
	OnPeer func(conn net.Conn, peerDevice string)
}

// NewServer validates the address and starts listening.
func NewServer(addr string, ident Identity, trust Trust, warn io.Writer) (*Server, error) {
	if warn == nil {
		warn = io.Discard
	}
	if err := ValidateListenAddr(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Server{ident: ident, trust: trust, warn: warn, ln: ln}, nil
}

// Addr returns the bound address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops accepting.
func (s *Server) Close() error { return s.ln.Close() }

// Serve accepts until the listener closes.
func (s *Server) Serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(config.SyncHelloTimeout))
	r := bufio.NewReader(conn)

	// 1. dialer's hello (unsigned; its proof comes in step 3)
	their, err := readMsg(r)
	if err != nil {
		return
	}
	nonceMine, err := newNonce()
	if err != nil {
		return
	}
	// 2. our signed hello: proves US to the dialer
	sig := ed25519.Sign(s.ident.Priv, transcript(s.ident.CairnID, s.ident.DeviceID, nonceMine, their.Nonce))
	if err := writeMsg(conn, hello{
		V: 1, CairnID: s.ident.CairnID, DeviceID: s.ident.DeviceID,
		Nonce: nonceMine, Sig: base64.StdEncoding.EncodeToString(sig),
	}); err != nil {
		return
	}
	// 3. dialer's signature over the full transcript: proves THEM to us
	proof, err := readMsg(r)
	if err != nil {
		fmt.Fprintf(s.warn, "SYNC REFUSED: %s (device %q, cairn %q): connection dropped before proof\n",
			conn.RemoteAddr(), their.DeviceID, their.CairnID)
		return
	}
	check := hello{V: their.V, CairnID: their.CairnID, DeviceID: their.DeviceID, Sig: proof.Sig}
	if err := verifyPeer(s.trust, s.ident.CairnID, &check,
		transcript(s.ident.CairnID, their.DeviceID, their.Nonce, nonceMine)); err != nil {
		// R27: refusals LOG the presented identity
		fmt.Fprintf(s.warn, "SYNC REFUSED: %s presented device %q (cairn %q): %v\n",
			conn.RemoteAddr(), their.DeviceID, their.CairnID, err)
		writeMsg(conn, hello{V: 1, OK: false})
		return
	}
	if err := writeMsg(conn, hello{V: 1, OK: true}); err != nil {
		return
	}
	fmt.Fprintf(s.warn, "sync: authenticated peer %s (device %s)\n", conn.RemoteAddr(), their.DeviceID)
	if s.OnPeer != nil {
		conn.SetDeadline(time.Time{})
		s.OnPeer(conn, their.DeviceID)
	}
}

// Ping dials a peer, completes the mutual handshake, and returns the
// authenticated responder device id (the N5 membership proof; N6 keeps the
// connection for reconciliation).
func Ping(addr string, ident Identity, trust Trust) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, config.SyncHelloTimeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(config.SyncHelloTimeout))
	r := bufio.NewReader(conn)

	nonceMine, err := newNonce()
	if err != nil {
		return "", err
	}
	// 1. our hello
	if err := writeMsg(conn, hello{V: 1, CairnID: ident.CairnID, DeviceID: ident.DeviceID, Nonce: nonceMine}); err != nil {
		return "", err
	}
	// 2. responder's signed hello — verify THEM first
	their, err := readMsg(r)
	if err != nil {
		return "", fmt.Errorf("peer closed during handshake (are we enrolled?): %w", err)
	}
	if err := verifyPeer(trust, ident.CairnID, their,
		transcript(ident.CairnID, their.DeviceID, their.Nonce, nonceMine)); err != nil {
		return "", fmt.Errorf("responder failed authentication: %w", err)
	}
	// 3. our proof
	sig := ed25519.Sign(ident.Priv, transcript(ident.CairnID, ident.DeviceID, nonceMine, their.Nonce))
	if err := writeMsg(conn, hello{V: 1, CairnID: ident.CairnID, DeviceID: ident.DeviceID, Sig: base64.StdEncoding.EncodeToString(sig)}); err != nil {
		return "", err
	}
	verdict, err := readMsg(r)
	if err != nil {
		return "", fmt.Errorf("peer dropped the connection after our proof (refused?): %w", err)
	}
	if !verdict.OK {
		return "", errors.New("peer REFUSED this device (not enrolled, or revoked)")
	}
	return their.DeviceID, nil
}
