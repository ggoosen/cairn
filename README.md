<div align="center">

# Cairn 🪨

**A private, crash-safe message and knowledge mesh for AI agents — so your sessions stop needing you as the copy-paste courier.**

[![License: PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/License-PolyForm%20Noncommercial%201.0.0-blue.svg)](https://polyformproject.org/licenses/noncommercial/1.0.0/)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/platform-macOS%20%C2%B7%20Linux-lightgrey.svg)](#quickstart)
[![Status](https://img.shields.io/badge/status-pre--alpha-orange.svg)](#status)
[![Local-first](https://img.shields.io/badge/data-100%25%20local%20%C2%B7%20offline-success.svg)](#how-its-built)
[![PRs](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](#contributing)

*Append-only signed event log · hybrid offline search · crash-safe by construction · no cloud, ever.*

</div>

Cairn gives every AI agent session on your machines a shared, searchable memory. An agent asks for what it needs and receives a **ranked, provenance-preserving answer within a hard context budget** — and can deliberately fetch the original material when it matters. Agents are never handed a raw inbox.

Like the trail-marker stones it's named for: every message is immutable once placed, readable by whoever passes next, needs no coordinator, and the meaning compounds as more are added.

## Contents

- [The problem](#the-problem)
- [What Cairn does](#what-cairn-does)
- [How it's built](#how-its-built)
- [Roadmap](#roadmap)
- [Prior art & inspirations](#prior-art--inspirations)
- [Status](#status)
- [Security posture](#security-posture)
- [Quickstart](#quickstart)
- [License](#license)
- [Contributing](#contributing)

---

## The problem

If you run multiple AI agent sessions — Claude Code here, Codex there, a chat session on another machine — you already know the failure mode: **you are the message bus.** Decisions, research, files, and context move between sessions by manual copy-paste. It's slow, it's lossy, and nothing compounds: the same knowledge gets re-explained to every new session forever.

## What Cairn does

- **`cairn send`** — any agent publishes a message, decision, or artifact into the mesh
- **`cairn digest --budget 1500`** — any agent gets a *ranked* rollup of what's new and relevant, hard-capped to a token/character budget
- **`cairn search "council approval status"`** — hybrid keyword + semantic search across everything, from any session, offline
- **`cairn fetch <id>`** — deliberately pull the full original, with provenance back to the signed source event
- **`cairn thread <id>`** — read a whole conversation; **`cairn topic list`** — browse the taxonomy with live counts
- **`cairn why-ranked <interaction-id> <message-id>`** — see the exact arithmetic behind every ranking. No black boxes.

Agents connect through whichever door they can reach: **plain files** (drop markdown in an outbox, read a generated digest — works with literally anything), the **CLI**, or **MCP** (`cairn mcp`, for Claude Desktop / Claude Code / Codex — every result wrapped in an untrusted-content envelope with full provenance). One daemon per machine; every app and session is just a named consumer.

## How it's built

- **Append-only, signed event log.** Every message is an immutable Ed25519-signed event, hash-chained per origin. Edits are new revisions in a DAG; retractions are events; history always replays. Think personal blockchain, minus the consensus theater you don't need when you trust your own machines.
- **Markdown in, markdown out.** Bodies are content-addressed objects; human-editable exports round-trip through real three-way merge. Your knowledge is never trapped in a database — backup is `zip` a folder.
- **SQLite as a disposable projection.** FTS5 + local vector embeddings (fully offline, no API keys). Delete the index; `cairn reindex` rebuilds it byte-identically from the log.
- **Crash-safe by construction.** Acknowledgement happens only after fsync'd durability. The test suite includes a 15-point crash matrix with simulated power loss — *zero acknowledged-event loss* is a release gate, not an aspiration.
- **Private by architecture.** Nodes sync peer-to-peer over your own tailnet (later: [iroh](https://github.com/n0-computer/iroh)) — membership is device certificates chained to your mesh root, verified per connection, never trusted from the transport. Canonical/eager text replicates to your machines; big files fetch lazily by hash with explicit durability classes. No cloud, no third party in the data path, no public endpoints.
- **Equivocation-aware.** A cloned device that writes divergent history is *detected* when the logs meet, frozen, quarantined (never silently deleted), and repaired through an operator ceremony that preserves both branches.
- **Ranking that stays dumb on purpose.** Relevance + freshness + bounded priority, inspectable arithmetic, constants in one config file. Calibrated from real usage logs — never a learned black box.

## Roadmap

| Phase | Scope | Status |
|---|---|---|
| **P0** | Single-machine daemon: event log, search, ranked digests, outbox, exports, crash safety | ✅ complete |
| **P1** | Multi-machine Tailscale mesh: signed membership, event + text + blob replication with durability classes, live fork detection, MCP server, capability enforcement, durable semantic subscriptions, deterministic attachment derivatives | ✅ complete (hardened through three live two-node audit rounds) |
| **P2** | Retrieval quality: behavioural salience, calibrated ranking, **agent-shaped relevance** (self-subscribe + a self-configuring onboarding record), local **structural** navigation maps (topic/thread rollups) | 🔨 built (opt-in) |
| **P3** | Frictionless onboarding: **one-command `cairn setup`** (mesh + resident daemon service + MCP client wiring), iroh transport, one-time pairing invites, thin nodes for mobile | 🔨 single-machine deploy shipped · mesh offline scope built (live two-node checkout of pairing/thin-role is hardware-gated; iroh live wire deferred) |
| **CAPTURE** | Zero-effort capture: session-transcript ingest as a low-trust searchable substrate (opt-in, redacted, never in digests), trigram substring search, end-of-session handoff convention, memory-provider packaging for agent harnesses | 📋 planned ([work order](build/CAPTURE-PLAN.md)) |
| **P4** | Self-organising knowledge: automated filing, **embedding-clustered self-folding topic maps** (the semantic map — needs P2 usage/salience data to be good), salience propagation | planned |

**On P1:** the full multi-machine mesh — replication of events, canonical text, and lazy blobs with explicit durability classes; capability enforcement; the MCP server and its untrusted-content envelope; live fork detection; and durable subscriptions — is built, passing its acceptance suites, and hardened through three live two-node audit rounds (each round's blockers fixed and re-verified). It's ready for the first tagged release.

**On P3:** single-machine onboarding is real today — `cairn setup` (via `install.sh` or `make deploy`) creates the mesh, installs the daemon as a user service, and wires your MCP clients, all in one idempotent command. The multi-machine layer is built and safety-audited on real hardware to its offline-testable scope: one-time pairing invitations (`cairn pair invite`/`join`), thin-node roles for mobile/metered devices, and `cairn net` diagnostics. The live **iroh** peer-to-peer transport — the wire itself, relay self-hosting, and automatic metered/battery sensing — is deliberately deferred behind those interfaces (it needs a mature iroh Go binding and real relays) and drops in with no caller changes.

Each phase gates the next on measured results — P0's engineering gates (zero acknowledged-event loss, 100% provenance, 100% budget compliance, P95 lexical-visibility < 200 ms) are green, and Cairn must demonstrably beat copy-paste (Success@5 ≥ 70% across 30 real cross-session handoffs) before P2 is declared shipped. The event-sourced core means every later phase is a new projection over the same log: no migrations, ever.

## Prior art & inspirations

Kin and inspirations: [Secure Scuttlebutt](https://scuttlebutt.nz)'s per-origin signed append-only logs, [Karpathy's llm-wiki](https://gist.github.com/karpathy) pattern of flat-markdown knowledge bases navigated by index, and [iroh](https://iroh.computer)'s dial-by-public-key networking.

The design and its full decision trail — specification (v0.3), the binding build rulings, and the historical design briefs — live in [`docs/`](docs/) and [`RULINGS.md`](RULINGS.md).

## Status

**Pre-alpha.** P0 and P1 are both complete and audited — the single-machine daemon and the multi-machine Tailscale mesh (replication, blob durability, capability enforcement, MCP, live fork detection) are built, passing their acceptance suites, and hardened through the N9 punchlist plus three live two-node audit rounds (each round's blockers fixed and re-verified). What remains before cutting the first tag is the operator's own **30-handoff product evaluation** (the real-world "does it beat copy-paste?" gate in [`DOGFOOD.md`](DOGFOOD.md)). Star/watch for the release. Built in Go; macOS (Apple Silicon) first, Linux best-effort.

**Known limitation — degradation ladder.** Under disk/memory pressure Cairn sheds load in stages (defer auto-linking → summaries → embeddings → force lexical-only search → defer proactive blob replication). The two most aggressive stages — *rejecting* new low-priority blobs or small text outright — are currently computed and reported (visible in `cairn status`, every transition logged) but not yet *enforced*: safely rejecting a send needs reserved-capacity accounting that's deferred to its own change. Until then they fail open — the send proceeds — never silently.

## Security posture

- **Device keys are device-local, never in the portable mesh directory.** Each
  node's private key and the mesh root key live in device-local state
  (`~/Library/Application Support/cairn/…` on macOS, `$XDG_DATA_HOME/cairn/…` on
  Linux), `0600`, and never travel with a portable backup. A portable-only
  restore creates a *new* origin; it can never impersonate the old device.
- **Encrypted storage is expected; unencrypted is opt-in and loud.** `cairn init`
  refuses to start on an unencrypted volume unless you pass `--allow-unencrypted`,
  which persists the override device-local and **warns on every start**. This is
  the right escape hatch for a throwaway test node, but it means the device key
  sits on unencrypted storage. For any real node, keep the cairn directory — and
  therefore the device key — on encrypted storage (FileVault / LUKS / dm-crypt):
  a device key on an unencrypted volume is a compromise-at-rest risk, and a stolen
  device key stays a valid mesh writer until you `cairn device revoke` it. (On
  WSL2, use a native Linux filesystem inside the distro, not a Windows-mounted
  `drvfs` path — it can't be encrypted by cairn and is pathologically slow for the
  `synchronous=FULL` write path.)
- **Same-OS-user isolation prevents accidents, not malice:** the daemon cannot
  distinguish same-user local processes except by the handle they present. A
  stronger boundary (socket peer-cred binding, per-principal OS users) is a future
  consideration.

## Quickstart

**Prerequisite: Go 1.25+.** The build floor is set by dependencies (`golang.org/x/net`
processes untrusted mesh HTML, `ledongthuc/pdf` untrusted attachments — both kept current
for security), not by Cairn's own code (which compiles at 1.23). `go.mod` pins the build
toolchain to `go1.26.3` for reproducibility; a Go 1.21+ toolchain with `GOTOOLCHAIN=auto`
(the default) fetches it automatically. macOS arm64 is primary; Linux is best-effort.

**One command (recommended):**

```bash
curl -fsSL https://raw.githubusercontent.com/ggoosen/cairn/master/install.sh | sh
```

This builds Cairn, installs it to `~/.local/bin` (no sudo), and runs `cairn setup` —
which creates your mesh, installs the resident daemon as a user service
(launchd/systemd), and wires `cairn mcp` into every detected MCP client (Claude
Desktop / Claude Code / Codex). Idempotent: re-run any time to upgrade. From a
checkout, `make deploy` does the same. *(Requires Git + Go 1.23+ today — a prebuilt
signed binary + Homebrew tap is the planned zero-dependency path.)*

Then restart Claude Desktop / Claude Code so they load the tools, and:

```bash
cairn digest --view operator --budget 1500
```

**From a checkout** — build, then run the wizard (one command):

```bash
git clone https://github.com/ggoosen/cairn && cd cairn
make build                       # bin/cairn — always build via make (or the
                                 # sqlite_fts5 build tag); a plain `go build`
                                 # fails at compile time by design
export PATH="$PWD/bin:$PATH"

cairn setup                      # mesh + daemon service + MCP clients, idempotent
```

**Or the individual steps `cairn setup` runs** (only if you want to do it by hand — you do **not** need these *and* `setup`):

```bash
cairn init                       # create your mesh + device identity + genesis
cairn daemon --install           # resident single writer under launchd/systemd
cairn mcp-install --all          # wire cairn into detected MCP clients (one view per app)
```

Shell completion is built in: `cairn completion zsh|bash|fish --help` shows
the one-liner for your shell. A second machine pairs with
`cairn pair invite` + `cairn pair join` (the join registers the sync peer;
`cairn peer add <host:port>` manages peers live afterwards).

Then, whichever way you set up, use it (`setup-agent` mints a named consumer view — that's a different command from `setup`):

```bash
cairn setup-agent claude-code    # mint a named view for a consumer
cairn send --topic project/x "Decided: we're using approach B because …"
cairn digest --view claude-code --budget 1500
```

Wiring an agent is one line in its instructions file (`CLAUDE.md`, `AGENTS.md`, …):

> *At session start, run `cairn digest --view <name> --budget 1500`. Share decisions and findings via `cairn send`. To tune what your digest surfaces, declare a local standing interest with `cairn subscribe "<what you work on>" --view <name>` (your own view only; no `--durable`).*

Then read [`DOGFOOD.md`](DOGFOOD.md) for the full setup: the three agent surfaces (files, CLI, MCP), the multi-machine enrolment ceremony, blob durability, fork repair, and the 30-handoff evaluation protocol.

## License

Cairn is **source-available** under the [PolyForm Noncommercial License 1.0.0](https://polyformproject.org/licenses/noncommercial/1.0.0/).

**Plain English:**
- ✅ **Free for personal use** — run it, modify it, self-host it, tinker, learn, share patches
- ✅ Free for noncommercial research, education, and nonprofit use
- ❌ **Commercial use requires a separate license.** If you want to build a product, service, or business on Cairn — including offering it as a hosted service — contact me to negotiate commercial terms.

This isn't OSI-certified open source (noncommercial restrictions disqualify it, and I'd rather be upfront than "open-washed"). It's a deliberate trade: maximally free for individuals, while keeping commercialisation a conversation.

**Commercial licensing:** open an issue titled `[commercial]` or reach me via the contact details on my GitHub profile.

## Contributing

Issues and PRs welcome. Contributions are accepted under the project license; substantial contributors will be asked to sign a lightweight CLA (needed to keep dual/commercial licensing possible). The bar for merging into the event log and durability code is high — read [`build/TESTING.md`](build/TESTING.md) first; if your change touches the write path, it ships with crash-matrix coverage or it doesn't ship.

---

*Cairn: leave a stone, mark the trail, let the next traveller find the way.*
