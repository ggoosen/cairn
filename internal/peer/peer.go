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
	// Mode selects the connection protocol: "" (or "sync") runs the N5 mutual
	// membership handshake; "pair" runs the P3-2c pairing handshake (the dialer
	// is NOT yet a member — it presents a root-signed invite credential instead).
	Mode string `json:"mode,omitempty"`
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
	if isTailnetIP(ip) {
		return nil
	}
	return fmt.Errorf("%s is not a tailnet address (100.64.0.0/10 or fd7a:115c:a1e0::/48) — the sync listener binds the tailnet ONLY", ip)
}

// tailnet CGNAT ranges (Tailscale): the sync listener binds these ONLY.
var (
	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// isTailnetIP reports whether ip is in a Tailscale CGNAT range.
func isTailnetIP(ip netip.Addr) bool {
	return tailnetV4.Contains(ip) || tailnetV6.Contains(ip)
}

// DetectTailnetIP scans local interfaces for a Tailscale CGNAT address
// (100.64.0.0/10 or fd7a:115c:a1e0::/48) and returns the first found — the
// address the AUTO sync listener binds (R44), IPv4 preferred. When no tailnet
// interface exists it returns ("", false); under CAIRN_SYNC_ALLOW_LOOPBACK=1
// (dev/test) it falls back to 127.0.0.1 so the auto path is exercisable
// without a real tailnet. It NEVER returns an unspecified/0.0.0.0 address.
func DetectTailnetIP() (string, bool) {
	if addrs, err := net.InterfaceAddrs(); err == nil {
		var v6 string
		for _, a := range addrs {
			var raw net.IP
			switch v := a.(type) {
			case *net.IPNet:
				raw = v.IP
			case *net.IPAddr:
				raw = v.IP
			}
			ip, ok := netip.AddrFromSlice(raw)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			if !isTailnetIP(ip) {
				continue
			}
			if ip.Is4() {
				return ip.String(), true // prefer IPv4
			}
			if v6 == "" {
				v6 = ip.String()
			}
		}
		if v6 != "" {
			return v6, true
		}
	}
	if os.Getenv("CAIRN_SYNC_ALLOW_LOOPBACK") == "1" {
		return "127.0.0.1", true
	}
	return "", false
}

// Server accepts authenticated peer connections.
type Server struct {
	ident Identity
	trust Trust
	warn  io.Writer
	ln    net.Listener
	// OnPeer runs after a successful mutual handshake (N6 attaches the
	// reconciliation protocol here). The reader carries any bytes the
	// handshake buffered past the last message, so reconciliation MUST read
	// through it. nil = N5 skeleton: acknowledge + close.
	OnPeer func(conn net.Conn, r *bufio.Reader, peerDevice string)
	// OnPair validates an opaque pairing-invite payload (P3-2c) and, if valid,
	// returns the device public key to challenge for private-key possession plus
	// an admit closure to run AFTER the challenge passes (the daemon appends the
	// device.add). The peer layer stays identity-agnostic — the payload is opaque
	// and all cert/root verification lives in this callback. nil = pairing
	// disabled (the node refuses pairing dialers).
	OnPair func(payload []byte) (devicePub ed25519.PublicKey, admit func() (string, error), err error)
}

// NewServer validates the address and starts listening over DefaultTransport
// (the P1 tailnet TCP transport).
func NewServer(addr string, ident Identity, trust Trust, warn io.Writer) (*Server, error) {
	return NewServerWithTransport(DefaultTransport, addr, ident, trust, warn)
}

// NewServerWithTransport binds over an explicit transport (P3-1 seam: iroh and
// tests supply their own). The address is validated by the transport itself.
func NewServerWithTransport(tr Transport, addr string, ident Identity, trust Trust, warn io.Writer) (*Server, error) {
	if warn == nil {
		warn = io.Discard
	}
	ln, err := tr.Listen(addr)
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
	// P3-2c: a pairing dialer is not a member yet — it authenticates by a
	// root-signed invite credential, not by membership. Branch before the N5
	// mutual handshake (which would refuse a non-member).
	if their.Mode == "pair" {
		s.handlePair(conn, r, their)
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
		s.OnPeer(conn, r, their.DeviceID)
	}
}

// Ping dials a peer, completes the mutual handshake, and returns the
// authenticated responder device id (the N5 membership proof). It closes the
// connection; N6's Dial keeps it open for reconciliation.
func Ping(addr string, ident Identity, trust Trust) (string, error) {
	return PingWithTransport(DefaultTransport, addr, ident, trust)
}

// PingWithTransport is Ping over an explicit transport (P3-1 seam).
func PingWithTransport(tr Transport, addr string, ident Identity, trust Trust) (string, error) {
	conn, peerDevice, r, err := dial(tr, addr, ident, trust)
	if err != nil {
		return "", err
	}
	conn.Close()
	_ = r
	return peerDevice, nil
}

// Conn is an authenticated peer connection: the live net.Conn plus the
// buffered reader the handshake left mid-stream (N6 reconciliation reads
// through it so no bytes are lost).
type Conn struct {
	net.Conn
	PeerDevice string
	R          *bufio.Reader
}

// Dial completes the mutual handshake (identical to Ping) but returns the
// LIVE authenticated connection for N6 reconciliation. The caller owns the
// connection and must Close it. The deadline set during the handshake is
// cleared before returning.
func Dial(addr string, ident Identity, trust Trust) (*Conn, error) {
	return DialWithTransport(DefaultTransport, addr, ident, trust)
}

// DialWithTransport is Dial over an explicit transport (P3-1 seam).
func DialWithTransport(tr Transport, addr string, ident Identity, trust Trust) (*Conn, error) {
	conn, peerDevice, r, err := dial(tr, addr, ident, trust)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return &Conn{Conn: conn, PeerDevice: peerDevice, R: r}, nil
}

// dial performs the three-message mutual handshake and hands back the still-
// open connection and its reader. On any failure it closes the connection.
func dial(tr Transport, addr string, ident Identity, trust Trust) (net.Conn, string, *bufio.Reader, error) {
	conn, err := tr.Dial(addr, config.SyncHelloTimeout)
	if err != nil {
		return nil, "", nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	conn.SetDeadline(time.Now().Add(config.SyncHelloTimeout))
	r := bufio.NewReader(conn)

	nonceMine, err := newNonce()
	if err != nil {
		return nil, "", nil, err
	}
	// 1. our hello
	if err := writeMsg(conn, hello{V: 1, CairnID: ident.CairnID, DeviceID: ident.DeviceID, Nonce: nonceMine}); err != nil {
		return nil, "", nil, err
	}
	// 2. responder's signed hello — verify THEM first
	their, err := readMsg(r)
	if err != nil {
		return nil, "", nil, fmt.Errorf("peer closed during handshake (are we enrolled?): %w", err)
	}
	if err := verifyPeer(trust, ident.CairnID, their,
		transcript(ident.CairnID, their.DeviceID, their.Nonce, nonceMine)); err != nil {
		return nil, "", nil, fmt.Errorf("responder failed authentication: %w", err)
	}
	// 3. our proof
	sig := ed25519.Sign(ident.Priv, transcript(ident.CairnID, ident.DeviceID, nonceMine, their.Nonce))
	if err := writeMsg(conn, hello{V: 1, CairnID: ident.CairnID, DeviceID: ident.DeviceID, Sig: base64.StdEncoding.EncodeToString(sig)}); err != nil {
		return nil, "", nil, err
	}
	verdict, err := readMsg(r)
	if err != nil {
		return nil, "", nil, fmt.Errorf("peer dropped the connection after our proof (refused?): %w", err)
	}
	if !verdict.OK {
		return nil, "", nil, errors.New("peer REFUSED this device (not enrolled, or revoked)")
	}
	ok = true
	return conn, their.DeviceID, r, nil
}
