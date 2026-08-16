package peer

// P3-1 — the transport seam.
//
// A Transport moves bytes between mesh endpoints and nothing more. It is NEVER
// trusted for membership: the application-layer mutual cert handshake (R27,
// peer.go) is the ONLY authorization, and it runs identically over any
// transport. A transport supplies two things — connectivity (Listen/Dial) and
// an address scheme (ValidateAddr/LocalAddr). Endpoint identity ≠ mesh
// authorization, so a compromised or substituted transport cannot admit a
// non-member; it can only fail to deliver bytes.
//
// This is the seam iroh (P3-4) drops into: it will be another Transport, and
// the handshake + N6 reconciliation above it are untouched. The Tailscale/TCP
// transport (P1) is the default.

import (
	"fmt"
	"net"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// Transport is the byte-moving substrate the authenticated handshake runs over.
// Implementations MUST NOT make any trust decision — that is the handshake's job.
type Transport interface {
	// Name identifies the transport in diagnostics and logs (e.g. "tcp-tailnet").
	Name() string
	// ValidateAddr reports whether addr is a well-formed listen address for this
	// transport (the tailnet transport rejects 0.0.0.0/:: and non-tailnet IPs).
	ValidateAddr(addr string) error
	// Listen binds an inbound listener at addr (after ValidateAddr).
	Listen(addr string) (net.Listener, error)
	// Dial opens an outbound connection to a peer address, bounded by timeout.
	Dial(addr string, timeout time.Duration) (net.Conn, error)
	// LocalAddr auto-detects this node's own bindable address for AUTO listen
	// resolution; ("", false) when the transport has no auto address.
	LocalAddr() (string, bool)
}

// DefaultTransport is the P1 Tailscale/TCP transport: a TCP socket bound to the
// tailnet CGNAT address only. The bare NewServer/Dial/Ping/ValidateListenAddr/
// DetectTailnetIP entry points use it, so no existing caller changes; iroh and
// tests pass an explicit Transport to the *WithTransport variants.
var DefaultTransport Transport = tcpTransport{}

// TransportByName resolves a configured transport name (P3-4). The empty name
// and TransportTCPTailnet select the P1 default. TransportIroh is the P3 target
// and still refuses INSTRUCTIVELY here rather than pretending — the P2-7
// deferral pattern. The P3-1 Transport interface is the seam it drops into with
// no caller change once a binding is CHOSEN.
//
// RESOLVED by R58 (2026-08-16): DEFERRED, not blocked. No Go iroh binding is
// adopted. github.com/tmc/go-iroh maps onto this interface almost verbatim and
// was demonstrated working (two endpoints, round trip by public key), but it is
// v0.0.0, days old, single-author, unaffiliated with n0, and vendors a quic-go
// fork plus a patched crypto/tls into the process holding the device signing
// key — and it moves the Go floor to 1.26, an R52 decision. The n0 routes
// (iroh-ffi via uniffi-bindgen-go, or iroh-c-ffi) each add cgo, a Rust
// toolchain and a per-platform static library to the release path.
//
// The decisive point is need: iroh buys NAT traversal for nodes WITHOUT a
// tailnet, which is a distribution problem Cairn does not yet have. Waiting
// costs nothing because this seam exists and a second transport is already
// exercised in the tests. Revisit when the binding has tags and adopters, or
// when n0 ships Go bindings.

// Deciding that is a dependency-and-spec call for the author, not an
// implementation detail, so this stays refused until it is ruled on. The
// conservative reading is the one in force: refuse, and keep the mesh on the
// audited tailnet transport.
func TransportByName(name string) (Transport, error) {
	switch name {
	case "", config.TransportTCPTailnet:
		return DefaultTransport, nil
	case config.TransportIroh:
		return nil, fmt.Errorf("transport %q is not available in this build: iroh is a Rust project with no official Go binding, and choosing between a cgo/Rust FFI build, an unaffiliated pure-Go reimplementation, or dropping iroh is an open author ruling (PROGRESS.md P3-4c). Use %q (the default) for the tailnet mesh",
			config.TransportIroh, config.TransportTCPTailnet)
	default:
		return nil, fmt.Errorf("unknown transport %q (want %q or %q)", name, config.TransportTCPTailnet, config.TransportIroh)
	}
}

// tcpTransport is the tailnet-bound TCP transport. Its addressing rules live in
// ValidateListenAddr / DetectTailnetIP (peer.go) — the methods delegate there so
// the P1 semantics have exactly one definition.
type tcpTransport struct{}

func (tcpTransport) Name() string { return "tcp-tailnet" }

func (tcpTransport) ValidateAddr(addr string) error { return ValidateListenAddr(addr) }

func (t tcpTransport) Listen(addr string) (net.Listener, error) {
	if err := t.ValidateAddr(addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", addr)
}

func (tcpTransport) Dial(addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, timeout)
}

func (tcpTransport) LocalAddr() (string, bool) { return DetectTailnetIP() }
