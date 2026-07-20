# Cairn 🪨

**A private, crash-safe message and knowledge mesh for AI agents — so your sessions stop needing you as the copy-paste courier.**

Cairn gives every AI agent session on your machines a shared, searchable memory. An agent asks for what it needs and receives a **ranked, provenance-preserving answer within a hard context budget** — and can deliberately fetch the original material when it matters. Agents are never handed a raw inbox.

Like the trail-marker stones it's named for: every message is immutable once placed, readable by whoever passes next, needs no coordinator, and the meaning compounds as more are added.

---

## The problem

If you run multiple AI agent sessions — Claude Code here, Codex there, a chat session on another machine — you already know the failure mode: **you are the message bus.** Decisions, research, files, and context move between sessions by manual copy-paste. It's slow, it's lossy, and nothing compounds: the same knowledge gets re-explained to every new session forever.

## What Cairn does

- **`cairn send`** — any agent publishes a message, decision, or artifact into the mesh
- **`cairn digest --budget 1500`** — any agent gets a *ranked* rollup of what's new and relevant, hard-capped to a token/character budget
- **`cairn search "council approval status"`** — hybrid keyword + semantic search across everything, from any session, offline
- **`cairn fetch <id>`** — deliberately pull the full original, with provenance back to the signed source event
- **`cairn why-ranked <id>`** — see the exact arithmetic behind every ranking. No black boxes.

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
| **P1** | Multi-machine Tailscale mesh: signed membership, event + text + blob replication with durability classes, live fork detection, MCP server, capability enforcement, durable semantic subscriptions, deterministic attachment derivatives | 🔨 nearly complete |
| **P2** | Retrieval quality: behavioural salience, calibrated ranking, local **structural** navigation maps (topic/thread rollups), entity/typed-edge graph projection | 🔨 built (opt-in) |
| **P3** | Frictionless onboarding: iroh transport, one-time pairing invites, thin nodes for mobile | 🔨 offline scope built + safety surface live-audited (iroh live wire deferred) |
| **P4** | Self-organising knowledge: automated filing, **embedding-clustered self-folding topic maps** (the semantic map — needs P2 usage/salience data to be good), salience propagation | planned |

**P3 progress:** the onboarding/transport layer is built to its offline-testable
scope — a transport abstraction seam (P3-1); one-time pairing invitations end to
end (`cairn pair invite`/`join`: offline mint → paste-able token → new-node
install → live pairing handshake → durable hard-single-use admission →
immediately syncable) (P3-2); thin-node role with durability exclusion, role
advertisement, partial universal search, remote-query (a thin node consults a
full peer when partial), and a metered policy (P3-3); and operator-selectable
transport + `cairn net` diagnostics (P3-4), with a concrete iroh integration
plan. A crossed two-auditor **live** pass (2026-07-19) verified the P3 safety
surface on real hardware — pairing hard-single-use (survives restart), membership
not bypassed, thin durability accounting, and the remote-query contract
(query-verb only, provenance preserved, graceful degrade, and `metered=true`
confirmed **zero bytes on the wire**) — verdict SAFE-TO-CLOSE, zero blockers; the
broad functional live pass is pending a rig binary redeploy. The **live iroh 1.x
wire, relay diagnostics/self-host, and automatic metered/battery sensing remain
deferred (hardware-gated)** — they sit behind the built interfaces and drop in
with no caller changes. See `docs/cairn-p3-onboarding-transport.md`.

**P1 progress:** the mesh is built and passing its acceptance suites — MCP server + untrusted-content envelope (N1), capability enforcement + trusted launcher (N2), durable semantic subscriptions (N3), sandboxed attachment derivatives + receiver summary checks (N4), Tailscale transport + enrolment ceremony (N5), reconciliation + text replication (N6), blob replication + durability acknowledgement (N7), and live fork detection + network doctor (N8). The remaining gate is **N9: hardening + a crossed two-auditor network audit** before the first tagged release.

Each phase gates the next on measured results — P0's engineering gates (zero acknowledged-event loss, 100% provenance, 100% budget compliance, P95 lexical-visibility < 200 ms) are green, and it must demonstrably beat copy-paste (Success@5 ≥ 70% across 30 real cross-session handoffs) before P2 ships. The event-sourced core means every later phase is a new projection over the same log: no migrations, ever.

## Design pedigree

The architecture was developed through five rounds of structured adversarial review across multiple frontier LLMs, with every accepted and rejected recommendation recorded in a decisions changelog. The full paper trail lives in [`docs/`](docs/) and [`RULINGS.md`](RULINGS.md): the specification (v0.3), the binding build rulings (`RULINGS.md` supersedes rulings-v0.3.1), and the historical design briefs.

Kin and inspirations: [Secure Scuttlebutt](https://scuttlebutt.nz)'s per-origin signed append-only logs, [Karpathy's llm-wiki](https://gist.github.com/karpathy) pattern of flat-markdown knowledge bases navigated by index, and [iroh](https://iroh.computer)'s dial-by-public-key networking.

## Status

**Pre-alpha.** P0 is complete and P1 is nearly complete — the single-machine daemon and the multi-machine Tailscale mesh (replication, blob durability, capability enforcement, MCP, live fork detection) are built and passing their acceptance suites. What remains before the first tagged release is **N9: hardening under a full network fault matrix and a crossed two-auditor security audit**. Star/watch for the release. Built in Go; macOS (Apple Silicon) first, Linux best-effort.

**Known limitation — degradation ladder (spec §8.2):** the load-shedding ladder enforces rungs 1–5 (delay auto-links → summaries → embeddings → force lexical-only search → delay proactive blob replication). The two disk/quota **reject** rungs (6 reject low-priority blobs, 7 reject small text) are computed and reported (the level shows in `cairn status` and every transition is logged) but not yet *enforced* — safely rejecting a send needs pre-ack reserved-capacity semantics that are deferred to a dedicated change. Until then they fail open (the send proceeds), never silently.

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
  sits on unencrypted storage.
  - **Standing finding (FIX-J4.2):** the N9 two-node test rig ran NODE-B on a WSL2
    box on an **unencrypted** volume with a persisted `--allow-unencrypted`
    override — acceptable for a disposable test node, **not** acceptable for any
    real second node. For a production node, put the cairn dir (and therefore the
    device-local key path) on encrypted storage (FileVault / LUKS / dm-crypt); a
    device key on unencrypted storage is a device-compromise-at-rest risk, and a
    stolen device key is a valid mesh writer until you `cairn device revoke` it.
  - WSL2 note: a Windows-mounted (`drvfs`) path is both unencryptable-by-cairn and
    pathologically slow for `synchronous=FULL` I/O (it is what wedged the run-2
    reindex — see DOGFOOD §15 / PROGRESS J3). Keep the cairn dir on a **native
    Linux** filesystem inside the WSL2 distro, on encrypted storage.
- **Same-OS-user isolation prevents accidents, not malice** (capability profiles,
  R22/R35): the daemon cannot distinguish same-user local processes except by the
  handle they present. A stronger boundary (socket peer-cred binding, per-principal
  OS users) is a future consideration.

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
cairn mcp-install --all          # wire cairn into detected MCP clients
```

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

Issues and PRs welcome once P1 lands. Contributions are accepted under the project license; substantial contributors will be asked to sign a lightweight CLA (needed to keep dual/commercial licensing possible). The bar for merging into the event log and durability code is high — read [`build/TESTING.md`](build/TESTING.md) first; if your change touches the write path, it ships with crash-matrix coverage or it doesn't ship.

---

*Cairn: leave a stone, mark the trail, let the next traveller find the way.*
