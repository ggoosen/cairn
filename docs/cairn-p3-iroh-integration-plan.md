# P3-4 — iroh transport: integration plan (for the hardware phase)

The iroh 1.x wire is the one P3 item that cannot be honestly built offline: iroh
is a Rust QUIC stack with no mature production Go binding, and its value (NAT
traversal, relays) is only exercisable on real network hardware. Rather than
commit non-functional skeleton code that can't be tested (against the repo's
"green + tested" discipline), this is the **concrete plan** to build it when a
binding + rig exist. Everything it plugs into is already built and tested.

## The seam it drops into (already built)

`internal/peer.Transport` (P3-1) — iroh is just another implementation:

```go
type Transport interface {
    Name() string
    ValidateAddr(addr string) error
    Listen(addr string) (net.Listener, error)
    Dial(addr string, timeout time.Duration) (net.Conn, error)
    LocalAddr() (string, bool)
}
```

- `peer.TransportByName("iroh")` currently returns an instructive "deferred"
  error (P3-4a). The wire replaces that error with a real `irohTransport{}`.
- **Nothing above the transport changes.** The mutual cert handshake (R27) and N6
  reconciliation operate on `net.Conn` and are transport-agnostic — proven today
  by the in-memory-transport test (`peer.TestP31HandshakeOverArbitraryTransport`).
  Membership stays the app-layer handshake; iroh authenticates *endpoints*, the
  daemon independently verifies *mesh membership*. A substituted transport can
  never admit a non-member — only fail to deliver bytes.

## Binding decision (make first)

Two viable ways to reach iroh from Go; pick per the rig:

| Option | How | Pros | Cons |
|---|---|---|---|
| **A. cgo FFI** | build iroh (or `iroh-ffi`) as a C-ABI staticlib; cgo wrapper exposing endpoint/connect/accept | in-process; lowest latency; one binary | cgo + Rust toolchain in the build; cross-compile pain; macОS arm64 codesigning (mind the AMFI remove-before-copy rule) |
| **B. sidecar** | a small Rust `iroh` process; Go talks to it over a local unix socket; `irohTransport.Dial/Listen` proxy to it | clean process isolation; no cgo; the sidecar can be updated/patched independently | second process to supervise; per-message IPC overhead; lifecycle/restart handling |

Recommendation: **start with B (sidecar)** — it keeps cgo out of the main build,
isolates the Rust dependency, and the IPC cost is negligible next to network RTT.
Revisit A only if in-process latency proves to matter. Either way, keep the
`Transport` interface identical so the choice is invisible to callers.

## Method mapping (iroh 1.x)

- **`Name()`** → `"iroh"`.
- **`ValidateAddr(addr)`** → parse/verify an iroh `NodeAddr` (NodeId + optional
  relay URL + direct addrs). Unlike tcp-tailnet there is no `0.0.0.0` guard; the
  address is a node identity, not a socket bind.
- **`Listen(addr)`** → create an iroh `Endpoint` (with our ALPN, e.g.
  `cairn/sync/1`), spawn its accept loop, and adapt each accepted bi-stream to
  `net.Conn` (so `peer.Server.handle` runs unchanged). `net.Listener.Addr()`
  returns our NodeAddr string.
- **`Dial(addr, timeout)`** → `Endpoint.connect(NodeAddr, ALPN)`, open a
  bi-directional stream, adapt to `net.Conn`, honor `timeout` via context.
- **`LocalAddr()`** → this node's NodeAddr (for AUTO listen resolution / `cairn
  net`). Replaces `DetectTailnetIP` for the iroh path.

The `net.Conn` adapter over an iroh bi-stream is the main piece of glue: map
Read/Write/Close/SetDeadline onto the stream. The handshake sets deadlines, so
`SetDeadline` must work (QUIC streams support it).

## Relays, self-host, patching (the operational story)

- **Relay selection.** iroh's public relays are rate-limited → fully infra-free
  onboarding carries a soft dependency. Config: an optional `relay_url` (device-
  local, next to `transport`). Default to iroh's public relays; allow pinning a
  self-hosted one.
- **Self-host diagnostics.** Extend `cairn net`: show the active relay, whether a
  direct path (hole-punched) is established vs. relay-fallback, and RTT. These are
  the live checks that need real relays — build them against the sidecar/endpoint
  status API.
- **Patching duty.** A self-hosted relay is a patching responsibility (iroh
  shipped a relay DoS fix in 1.0.2). Ship an update/patch mechanism + a `cairn
  net` staleness warning for the relay binary version. Part of the transport
  rollout, not an afterthought.

## Test plan (needs the rig)

1. **Two nodes on separate NATs** (not one LAN/tailnet) — prove hole-punching and
   relay-fallback both work; `cairn net` reports which path is live.
2. **Pairing over iroh** — `cairn pair invite`/`join` end to end over the iroh
   transport (the pairing handshake is transport-agnostic; this just swaps the
   substrate). Confirm the same durable, single-use admission.
3. **Membership still enforced** — a non-member iroh endpoint that completes the
   iroh handshake is still refused by the app-layer cert handshake (R27). This is
   the load-bearing security invariant; test it explicitly.
4. **Relay self-host + patching** — stand up a self-hosted relay, point a node at
   it, verify selection + the staleness warning.

## Definition of done

`peer.TransportByName("iroh")` returns a working `irohTransport`; `transport =
"iroh"` in device config brings up sync over iroh with no other code change; the
four rig tests pass; `cairn net` reports relay/path/RTT truthfully. Until then the
instructive-refusal (P3-4a) stands so nobody selects a half-built transport.
