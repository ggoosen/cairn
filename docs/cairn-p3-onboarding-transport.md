# P3 — onboarding & transport (operator reference)

What P3 delivers and how to use it. Scope: spec §12 P3 — *iroh 1.x transport,
one-time pairing invitations, relay selection/self-host diagnostics + patching
story, thin-node role*. The live iroh wire is **deferred (hardware-gated)**; the
rest is built and tested. Build notes live in `PROGRESS.md` (the P3-PLAN work-order file was removed from the tree after completion).

## 1. One-time pairing invitations

Frictionless onboarding replaces the three-file enrolment ceremony (`device
enroll` → `device approve` → `device join`, which restores the root key at approve
time) with a **single one-time token**.

**On an existing node (offline root ceremony — once):**

```
# restore the root key from offline storage first, then:
cairn pair invite --name laptop --root-key /path/to/restored-root.key
# writes pair-invite.token (0600). REMOVE the restored root key afterward.
```

The mint generates the new device's keypair, root-signs its certificate, and
packages `{cert + key + verifiable chain from genesis}` into one token
(`cairn-pair-v1.<base64url>`). It appends **nothing** to the log.

**On the new node:**

```
cairn pair join pair-invite.token <inviting-node-tailnet-addr:9700>
cairn daemon          # start syncing (join registered the inviting node as a sync peer)
```

`pair join` verifies the token from genesis, installs the identity, then completes
a network **pairing handshake**: it proves possession of the device key, and the
inviting node appends the `device.add` that admits it (**append-on-arrival**).

Security properties:

- **One-time / expiring:** the token expires 15 min after mint (anchored in the
  root-signed cert's `issued_at`, so it cannot be forged). Treat it as a SECRET —
  it carries a device credential.
- **Hard single-use:** a given invitation admits at most one `device.add` per
  node; replays are refused.
- **Root stays offline:** the cert is root-signed at mint; no root key ever lives
  on a running node. No delegated live-signing authority is introduced (that
  remains P4).
- **Immediately syncable:** the inviting node refreshes its live trust on
  admission, so the new device syncs without a daemon restart.

Tradeoff (accepted, operator ruling 2026-07-16): the device private key travels
inside the one-time token. Mitigated by expiry + single-use + treating the token
as a secret.

## 2. Thin nodes (mobile / metered)

```
cairn init --thin
```

A thin node (spec §7) holds only a recent window + selected objects. Consequences,
enforced today:

- **Not counted toward durability.** Full nodes exclude thin nodes from the
  `important`/`pinned` replica target (a thin node that *does* hold a blob still
  counts via actual holdership — no double-count, no unreachable target).
- **Never advertised as a normal node.** A node advertises its true role at the
  sync frontier exchange; a thin node never claims to be full.
- **Partial universal search.** A thin node's `cairn_search` / `cairn_digest`
  results carry `partial: true` + a reason, so the agent knows older material may
  live on full nodes and was not searched locally.

Deferred (hardware-gated): the live remote-query dependency (a thin node asking a
full node to search on its behalf) and battery/metered awareness.

## 3. Transport

The sync transport sits behind a stable interface (the P3-1 seam). Select it in
device config (`transport = "..."`):

- **`tcp-tailnet`** (default) — a TCP socket bound to the tailnet CGNAT address
  only (never `0.0.0.0`). This is the P1 mesh transport; membership is always the
  app-layer device-cert handshake (R27), never the transport.
- **`iroh`** — the P3 target (iroh 1.x endpoint auth + application-layer
  membership). **Deferred:** selecting it disables sync with an instructive
  message (there is no mature Go binding and it needs real relays/NAT traversal).
  The daemon still serves local reads/writes.

Diagnostics:

```
cairn net            # transport, role, listener, peers, relay status
cairn net --json     # raw
```

## 4. iroh / relays / self-host / patching (the deferred story)

When the iroh wire lands, it drops into the P3-1 `Transport` interface with **no
caller changes** — the handshake and reconciliation above it are transport-
agnostic (proven by the in-memory-transport test). The operational story to
implement alongside it:

- **Relay selection / self-host diagnostics.** iroh's public relays are
  rate-limited, so fully infra-free onboarding carries a soft dependency unless a
  relay is self-hosted. `cairn net` is the diagnostic surface; live relay health
  checks need real relays and are deferred.
- **Patching duty.** A self-hosted relay is a patching responsibility (iroh
  shipped a relay DoS fix in 1.0.2). The update/patching mechanism is part of the
  P3 transport rollout.
- **Endpoint auth ≠ membership.** iroh authenticates endpoints; the daemon still
  verifies mesh membership at the application layer independently. A substituted
  transport can never admit a non-member — it can only fail to deliver bytes.
