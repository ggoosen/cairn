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
// but its live wire is hardware-gated (no mature Go binding; needs real relays),
// so it refuses INSTRUCTIVELY here rather than pretending — the P2-7 deferral
// pattern. The P3-1 Transport interface is the seam it drops into with no caller
// change once the binding lands.
func TransportByName(name string) (Transport, error) {
	switch name {
	case "", config.TransportTCPTailnet:
		return DefaultTransport, nil
	case config.TransportIroh:
		return nil, fmt.Errorf("transport %q is not available in this build: the iroh 1.x wire is deferred (hardware-gated — needs real relays/NAT traversal and a Go binding; see P3-PLAN.md). Use %q (the default) for the tailnet mesh",
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
