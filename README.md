<div align="center">

# Cairn 🪨

**A private, crash-safe message and knowledge mesh for AI agents — so your sessions stop needing you as the copy-paste courier.**

[![License: PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/License-PolyForm%20Noncommercial%201.0.0-blue.svg)](https://polyformproject.org/licenses/noncommercial/1.0.0/)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/platform-macOS%20%C2%B7%20Linux-lightgrey.svg)](#quickstart)
[![Status](https://img.shields.io/badge/status-pre--alpha-orange.svg)](#status)
[![Local-first](https://img.shields.io/badge/data-100%25%20local%20%C2%B7%20offline-success.svg)](#how-its-built)
[![PRs](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](#contributing)

*Append-only signed event log · offline hybrid search · crash-safe by construction · no cloud, ever.*

</div>

Cairn gives every AI agent session on your machines a shared, searchable memory. An agent asks for what it needs and receives a **ranked, provenance-preserving answer within a hard context budget** — and can deliberately fetch the original material when it matters. Agents are never handed a raw inbox.

Like the trail-marker stones it's named for: every message is immutable once placed, readable by whoever passes next, needs no coordinator, and the meaning compounds as more are added.

## Contents

- [The problem](#the-problem)
- [What Cairn does](#what-cairn-does)
- [The rest of the surface](#the-rest-of-the-surface)
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
- **`cairn digest --budget 1500`** — any agent gets a *ranked* rollup of what's new and relevant, hard-capped to a character budget (oversized items are dropped whole, never truncated mid-item). Budget in tokens instead with `--budget-tokens`; pass exactly one of the two, and the response names the tokenizer that counted it — today an over-estimating approximation called `cairn-approx-v1`, not a BPE tokenizer
- **`cairn search "council approval status"`** — hybrid keyword + semantic search across everything, from any session, offline
- **`cairn fetch <id>`** — deliberately pull the full original, with provenance back to the signed source event
- **`cairn thread <id>`** — read a whole conversation; **`cairn topic list`** — browse the taxonomy with live counts
- **`cairn why-ranked <interaction-id> <message-id>`** — see the exact arithmetic behind a ranking, as it was recorded at the time. No black boxes.

Agents connect through whichever door they can reach: **plain files** (drop markdown in an outbox, read a generated digest — works with literally anything), the **CLI**, or **MCP** (`cairn mcp` serves twelve tools to Claude Desktop / Claude Code / Codex — every result that carries mesh content is wrapped in an untrusted-content envelope with full provenance; the rest are daemon-authored receipts). One daemon per machine; every app and session is just a named consumer.

## The rest of the surface

The five commands above are the daily loop. The rest of what's built:

**Bring your existing knowledge in**
- `cairn ingest scan ~/notes/wiki && cairn ingest apply` — import a docs tree or llm-wiki repo: a reviewable manifest first, then an idempotent apply that carries `source_ref` provenance, resolves `[[wiki links]]` into real message links, and mirrors your directory structure as topics. Re-run after edits and only changed pages become new revisions.
- `cairn export <id>` renders `exports/<id>.md` with read-only front-matter — edit the body in any editor, then `cairn export ingest`: an unchanged base becomes a clean revision, a diverged one is machine-merged with diff3, and a genuine conflict lands in `conflicts/<id>/` for you to settle with `cairn resolve`.

**Confine your agents**
- `cairn run --profile read-only -- <your agent>` — run any agent inside a capability-confined session: it gets a short-lived handle (`CAIRN_SESSION`), auto-revoked on exit or idle, that cannot retract, restructure topics, or touch admin. Profiles are `full` / `agent-standard` / `read-only`, or your own in `profiles.toml`. `cairn session list`, `cairn session prune` and `cairn session revoke` are the kill switch.
- `cairn run --profile agent-standard --topic 'project/x/*' --max-budget-chars 1500 -- <your agent>` — confine it to a *subtree* as well as a tier. The grant is positive-only, `*` spans `/` (so `project/x/*` is the whole subtree), and anything outside it is a **typed refusal**, never a quietly empty result — including via `thread`, which crosses topics. A budget cap is clamped and the clamp is reported in the response.

**Shape what each agent receives**
- `cairn subscribe "<what this view works on>" --view <name>` — declare a standing interest so digests surface it. Local by default (no events); `--durable` replicates it across the mesh.
- `cairn onboarding publish --view <name> --interest "…"` — publish one operator-signed onboarding record per view and every fresh session self-configures from it with `cairn onboarding apply`. Only the operator's record is honoured, only four whitelisted fields are applied, prose is never a directive, and it writes nothing outside a delimited block.
- `cairn found <interaction-id>` / `not-found` / `manual-workaround` — every search and digest returns an `interaction_id`; tell Cairn whether it worked. `cairn rank-stats` shows per-term contribution distributions, and `--calibrate` replays those episodes on held-out tasks to *recommend* new weights. You apply them; nothing learns behind your back.
- `cairn saved add council "council approvals"` / `cairn saved run council` — name the queries you run repeatedly.

**Navigate instead of searching blind**
- `scripts/cairn-adopt-standalone.sh` merges an ad-hoc **standalone mesh** into your primary one. A separate mesh has its own genesis and root, so its events cannot be merged (R34) — the procedure re-publishes the knowledge with provenance back to the original message id, verifies `cairn doctor` on both, and retires the old mesh without deleting it. It states plainly what it does not preserve. See DOGFOOD.md §16.
- `cairn map` writes `views/<view>/map.md`, a topic/thread/pin rollup. `cairn compact` writes `compaction.md`, the log folded down to current-state entities — what things *are* now, not how they got there. `cairn peek <id>` returns sender, revision, hash, class and size without spending budget on a body.

**Attachments**
- `cairn send "notes" --attach report.pdf --durability important --summary "Q3 audit"` — attachments become searchable through sandboxed deterministic extraction (size-capped, page-capped, hard-timeout, panics contained), with provenance back to the source blob via `cairn derivative list`. A sender's `--summary` is an untrusted claim, checked against the body; a divergent one is marked `[summary-disputed]` in digests.

**Check its work**
- `cairn doctor` — walk the whole log and verify every frame checksum, hash chain, signature, seal header, object, and cross-origin trust link. It reports; it never repairs. `cairn doctor fork` shows detected equivocation, `cairn doctor conflicts` your unresolved merge debt.
- `cairn gates` — print the release gates (acknowledged-event loss, provenance, budget compliance, send-to-visible P95, blob durability, unresolved forks) computed live against your own mesh. It reports INCONCLUSIVE rather than faking a PASS on thin data.
- `cairn status` — daemon health, log head, projection state, peers, degradation level. `cairn net` — transport, role, listener and peer configuration at a glance.

**Multi-machine**
- `cairn pair invite` / `cairn pair join` — one-time pairing invitations. `cairn device enroll|approve|list|revoke`, `cairn sync now|ping|status`, `cairn fork resolve` for the equivocation repair ceremony, `cairn migrate` to move to a new device identity.

## How it's built

- **Append-only, signed event log.** Every message is an immutable Ed25519-signed event, hash-chained per origin. Edits are new revisions in a DAG; retractions are events; history always replays. Every signature and chain link is re-verified on *every* daemon start, for every origin — the projection is never trusted for integrity. Think personal blockchain, minus the consensus theater you don't need when you trust your own machines.
- **Markdown in, markdown out.** Bodies are content-addressed objects; human-editable exports round-trip through real three-way merge (`git merge-file` semantics — `git` must be on PATH). Your knowledge is never trapped in a database — backup is `zip` a folder.
- **SQLite as a disposable projection.** FTS5 out of the box, plus optional local vector embeddings ([one bootstrap script](#optional-semantic-search) — fully offline, no API keys); without them retrieval degrades cleanly to lexical-only. Delete the index; `cairn reindex --lexical` rebuilds it from the log, deterministically, down to identical query results.
- **Crash-safe by construction.** Acknowledgement happens only after fsync'd durability. The test suite drives a 15-point crash matrix: the write path (object store, frame append, fdatasync, seal) is crashed before *every* mutating filesystem call under a fault-injecting FS that models true power loss — unsynced writes vanish, directory entries survive only after a dir-fsync — and the remaining points (projection, view swap, receipts, merge, migrate) are exercised with injected ENOSPC/EIO at every op. The same discipline extends to replication: kill-9 mid-sync on sender *and* receiver, deleted segments, offline catch-up. *Zero acknowledged-event loss* is a release gate, not an aspiration.
- **Private by architecture.** Nodes sync peer-to-peer over your own tailnet (later: [iroh](https://github.com/n0-computer/iroh)) — membership is device certificates chained to your mesh root, verified per connection, never trusted from the transport. Canonical/eager text replicates to your machines; attachments replicate out-of-band by content hash after events converge, with explicit durability classes (ephemeral stays put; normal targets two nodes; important/pinned target every node). Ephemeral text has a delivery-time window enforced on *both* the sending and receiving side, so a nonconforming peer cannot backfill it later. No cloud, no third party in the data path, no public endpoints.
- **Equivocation-aware.** A cloned device that writes divergent history is *detected* when the logs meet, frozen, quarantined (never silently deleted), and repaired through an operator ceremony that preserves both branches.
- **Ranking that stays dumb on purpose.** Relevance + freshness + bounded priority, inspectable arithmetic, constants in one config file. Never a learned black box: the weights are hand-set constants, and the offline calibration harness (`cairn rank-stats --calibrate`) replays your real usage logs only to *recommend* changes for a human to adopt. Explanations are stored snapshots keyed to the interaction that produced them, so a past ranking can never be silently rewritten by a later one.

## Roadmap

This table is the coarse phase-level view. Everything still to be built —
release blockers, capture, evaluation, deferred debt, unbuilt spec surfaces,
later phases and the open design rulings — lives in one file:
**[build/BUILD-PLAN.md](build/BUILD-PLAN.md)**, which also carries the
execution order and says what each blocked item is blocked *on*.

Worth knowing when reading the table: some outstanding work maps to **no
phase row at all** — deferred scaling debt (macOS codesigning for the prebuilt
binaries, which needs an Apple Developer ID) and surfaces
the spec describes but no milestone built (mutes, an `explore` ranking
profile). A green phase row is not a claim that nothing is owed underneath
it.

Vector search runs on a `sqlite-vec` index, feature-probed at startup; where
the extension will not load, the brute-force cosine scan answers instead and
`cairn status` names which path is live. Both return the same top-K — that
equivalence is a test, not an assumption.

| Phase | Scope | Status |
|---|---|---|
| **P0** | Single-machine daemon: event log, search, ranked digests, outbox, exports, crash safety | ✅ complete — engineering gates green; field evaluation pending |
| **P1** | Multi-machine Tailscale mesh: signed membership, event + text + blob replication with durability classes, live fork detection, MCP server, capability-scoped sessions, durable subscriptions, deterministic attachment derivatives | ✅ code-complete · passed a live two-node audit in July 2026 |
| **P2** | Retrieval quality: behavioural salience, an additive ranking profile + calibration harness, **agent-shaped relevance** (self-subscribe + a self-configuring onboarding record), local **structural** navigation maps (topic/thread rollups), saved searches, compaction views, opt-in OCR derivatives | 🔨 built (opt-in, not yet calibrated) |
| **P3** | Frictionless onboarding: **one-command `cairn setup`** (mesh + resident daemon service + MCP client wiring), iroh transport, one-time pairing invites, thin nodes for mobile | 🔨 single-machine deploy shipped · mesh scope built + audited single-host (two-machine pass and iroh live wire both deferred) |
| **CAPTURE** | Zero-effort capture: session-transcript ingest as a low-trust searchable substrate (opt-in, redacted, never in digests), trigram substring search, end-of-session handoff convention, memory-provider packaging for agent harnesses | 🔨 partial — trigram search, handoff convention and [memory-provider packaging](docs/memory-provider.md) shipped; transcript ingest designed but unbuilt, pending a crossed review of its privacy model ([build plan](build/BUILD-PLAN.md) §2 · [design note](build/CAPTURE-C3-DESIGN.md)) |
| **EVAL** | Proving the claims rather than making them: an independent black-box harness, corpora with **mined human relevance labels** (not author-written), component ablations, baselines including grep-over-transcripts, an agent-in-the-loop task battery, adversarial injection testing, and long-horizon/mesh recall — with **falsification criteria registered before measurement** | 🔨 apparatus built, **nothing measured yet** — harness ([`eval/`](eval/), its own module so the compiler enforces black-box access), corpus format/normalizers/loader and the time-control hook all ship and gate in CI; corpora are unacquired and the 21 kill criteria are unsigned, and measuring before either would defeat the point ([build plan](build/BUILD-PLAN.md) §3 · [claims register](eval/claims.yaml)) |
| **P4** | Self-organising knowledge: automated filing, **embedding-clustered self-folding topic maps** (the semantic map — needs P2 usage/salience data to be good), salience propagation | planned |

**On P1:** the full multi-machine mesh — replication of events, canonical text and attachment blobs with explicit durability classes; capability-scoped sessions; the MCP server and its untrusted-content envelope; live fork detection; and durable subscriptions — is built and passing its acceptance suites. In July 2026 it went through six rounds of live two-node audit on real hardware over a real tailnet (macOS + Linux), by two independent AI auditors working from separate adversarial briefs, ending with zero blockers — one of which found a genuine ephemeral-backfill leak that was fixed and re-verified live. Two caveats worth stating plainly: those auditors were AI agents, not human security reviewers; and the audit certifies a July commit — the pairing, trust and sync code has been extended since and has not been re-audited live. Treat it as thoroughly exercised, not independently certified.

**On P3:** single-machine onboarding is real today — `cairn setup` (via `install.sh` or `make deploy`) creates the mesh, installs the daemon as a user service, and wires your MCP clients in one idempotent command. The multi-machine layer — one-time pairing invitations (`cairn pair invite`/`join`), thin-node roles for mobile/metered devices (now with automatic metered/battery sensing, which may only ever ADD caution — a sensed "not metered" never overrides your config), and `cairn net` diagnostics — is built, and its safety invariants (single-use admission durable across restart, server-side root re-verification, membership enforcement, revocation, thin-node durability accounting) were adversarially audited on a real Linux box, including raw wire-protocol attacks below the CLI. That audit ran **over loopback on one host**; the companion two-machine pass was blocked before it started and has not been rerun, so P3's mesh path has not yet been exercised between two machines. The live **iroh** transport — the wire itself and relay self-hosting — is deliberately deferred behind a transport interface that the handshake and reconciliation already run over unchanged (a second transport implementation is exercised in the test suite), and drops in with no caller changes.

Each phase gates the next on measured results. P0's engineering gates are green on the dev machine — zero acknowledged-event loss (deep doctor clean across the crash matrix), 100% provenance on fetched results, 100% hard-budget compliance, and send-ack → lexical-digest-visible P95 of ~16–25 ms against a 200 ms gate over 347 real sends. Run `cairn gates` to reproduce them against your own corpus; note that the provenance and blob-durability rows pass trivially until you have traffic. The product gate is still open: Cairn must demonstrably beat copy-paste (Success@5 ≥ 70% across 30 real cross-session handoffs) before P2 is declared shipped, and that evaluation has not begun. The event-sourced core means every later phase is a new projection over the same log: no migrations, ever.

## Prior art & inspirations

Kin and inspirations: [Secure Scuttlebutt](https://scuttlebutt.nz)'s per-origin signed append-only logs, [Karpathy's llm-wiki](https://gist.github.com/karpathy) pattern of flat-markdown knowledge bases navigated by index (`cairn ingest` will import one), and [iroh](https://iroh.computer)'s dial-by-public-key networking.

The design and its full decision trail — specification (v0.3), the binding build rulings, and the historical design briefs — live in [`docs/`](docs/) and [`RULINGS.md`](RULINGS.md).

## Status

**Pre-alpha.** P0 and P1 are code-complete: the single-machine daemon and the multi-machine Tailscale mesh (replication, blob durability, capability-scoped sessions, MCP, live fork detection) are built, passing their acceptance suites, and past six rounds of live two-node hardening audit with zero blockers at the audited commit. P2 and P3 are built but opt-in, and their newest pieces — the self-subscribe/onboarding affordances and `cairn setup` — are unit-tested and code-reviewed but have had no live audit. What remains before cutting the first tag is the operator's own **30-handoff product evaluation** (the real-world "does it beat copy-paste?" gate in [`DOGFOOD.md`](DOGFOOD.md)), which has not started. Star/watch for the release. Built in Go; macOS (Apple Silicon) is the primary target, and Linux runs in CI on equal footing (verify, race, lint and fuzz smoke all gate) plus by hand on a WSL2 Ubuntu node.

**Known limitation — degradation ladder.** Under disk/memory pressure Cairn sheds load in stages (defer auto-linking → summaries → embeddings → force lexical-only search → defer proactive blob replication). The two most aggressive stages — *rejecting* new low-priority blobs or small text outright — are currently computed and reported (visible in `cairn status`, every transition logged) but not yet *enforced*: safely rejecting a send needs reserved-capacity accounting that's deferred to its own change. Until then they fail open — the send proceeds — never silently.

## Security posture

- **Device keys are device-local, never in the portable mesh directory.** Each
  node's private key and the mesh root key live in device-local state
  (`~/Library/Application Support/cairn/…` on macOS, `$XDG_DATA_HOME/cairn/…` on
  Linux), written `0600`, and never travel with a portable backup. A
  portable-only restore creates a *new* origin; it can never impersonate the old
  device. (Note the mode is enforced when the key is written, not re-checked on
  read — if you loosen it yourself, Cairn won't stop you.)
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
- **Capability profiles confine agents, not attackers.** `cairn run --profile`
  and `cairn mcp` confine themselves to a profile the daemon enforces per
  request, before acknowledgement — that is what stops an agent retracting
  messages or touching admin operations. It is *not* a boundary against a
  hostile local process: a caller that presents no session handle is treated as
  the operator, so the profile system prevents accidents and scope creep, not
  malice.
- **Same-OS-user isolation prevents accidents, not malice:** the daemon cannot
  distinguish same-user local processes except by the handle they present. A
  stronger boundary (socket peer-cred binding, per-principal OS users) is a future
  consideration.
- **Untrusted input is treated as untrusted.** Attachment extraction (HTML, PDF,
  DOCX) runs size-capped, page-capped, under a hard timeout with panics
  contained, and off the acknowledgement path. Everything an agent reads back
  out of the mesh is wrapped in an untrusted-content envelope. Heavy/OCR
  extractors are opt-in and their isolation is by construction, not
  OS-enforced — don't register a network-capable extractor without adding a jail.

## Quickstart

**Homebrew — prebuilt binary, no toolchain** *(from the first tagged release; see
the note below)*:

```bash
brew tap ggoosen/cairn
brew install cairn
cairn setup
```

Artifacts are macOS arm64 and Linux x86_64/arm64. Each one is built on a native
runner of its own architecture and then *executed there* — a real mesh, a real
daemon, a send and a search — before it is allowed onto a release page, so a
binary whose SQLite lacks FTS5 cannot ship
([`.github/workflows/release.yml`](.github/workflows/release.yml)). Checksums are
published with every release. Two honest caveats: the macOS binary is **not
notarized** (this project has no Apple Developer ID, so `brew install` works but
a *browser*-downloaded tarball is quarantined by Gatekeeper), and the Linux
binaries link glibc 2.35 (Ubuntu 22.04 and newer). `git` must be on PATH at
runtime either way. **This path goes live with the first tagged release** — the
pipeline is built and exercised in CI, but until a tag is pushed the tap has
nothing in it, so use the source install below.

**Prerequisites for the source install.** **Go 1.25+** — or any Go 1.21+ with
`GOTOOLCHAIN=auto` (the default) and network access, which fetches the pinned
toolchain for you. You also
need **`git`** (at build time *and* at runtime, for the three-way merge in
`cairn export ingest`) and a **C toolchain** — Cairn uses cgo for SQLite/FTS5, so
on macOS run `xcode-select --install` if you haven't. macOS arm64 is primary;
Linux is best-effort.

**One command (recommended until the tap is live):**

```bash
curl -fsSL https://raw.githubusercontent.com/ggoosen/cairn/master/install.sh | sh
```

This builds Cairn, installs it to `~/.local/bin` (no sudo), and runs `cairn setup` —
which creates your mesh, installs the resident daemon as a user service
(launchd/systemd), and wires `cairn mcp` into every detected MCP client (Claude
Desktop / Claude Code / Codex). Idempotent: re-run any time to upgrade. From a
checkout, `make deploy` does the same.

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

### Optional: semantic search

Out of the box, retrieval is **lexical-only** (SQLite FTS5) — good, and it needs
nothing but the binary. The semantic half of hybrid search needs a local
embedding model, which is a separate one-time step because it pulls a Python
runtime and a ~90 MB model:

```bash
scripts/cairn-embed-bootstrap.sh ~/cairn   # provisions the pinned all-MiniLM-L6-v2 venv
cairn reindex --semantic                   # embed the existing corpus
```

It stays fully offline and key-free. Without it the daemon says so on every
start and degrades cleanly to lexical-only; durable *semantic* subscriptions
also need it to match anything. See [`DOGFOOD.md`](DOGFOOD.md) §2.

### Wiring an agent

One line in its instructions file (`CLAUDE.md`, `AGENTS.md`, …):

> *At session start, run `cairn digest --view <name> --budget 1500`. Share decisions and findings via `cairn send`. Before ending the session, publish one handoff note — decisions and why, unfinished work, surprises — with `cairn send --topic <project> --priority 2`. To tune what your digest surfaces, declare a local standing interest with `cairn subscribe "<what you work on>" --view <name>` (your own view only; no `--durable`).*

Or publish it once and let sessions configure themselves: `cairn onboarding publish --view <name> --interest "…"`.

Wiring a harness that treats memory as a pluggable provider (Hermes agent, OpenClaw-style, anything that launches a stdio MCP server)? See [`docs/memory-provider.md`](docs/memory-provider.md) — config snippets, what the harness gets, and the capability profile it runs under.

Then read [`DOGFOOD.md`](DOGFOOD.md) for the full setup: the three agent surfaces (files, CLI, MCP), the multi-machine enrolment ceremony, blob durability, fork repair, and the 30-handoff evaluation protocol.

## License

Cairn is **source-available** under the [PolyForm Noncommercial License 1.0.0](https://polyformproject.org/licenses/noncommercial/1.0.0/).

**Plain English:**
- ✅ **Free for personal use** — run it, modify it, self-host it, tinker, learn, share patches
- ✅ Free for noncommercial research, education, and nonprofit use
- ❌ **Commercial use requires a separate license.** Read that broadly: it covers building a product, service, or business on Cairn *and* simply using it as a tool in the course of for-profit work. If Cairn would be useful at your job, that's a commercial licence — contact me and we'll sort out terms.

This isn't OSI-certified open source (noncommercial restrictions disqualify it, and I'd rather be upfront than "open-washed"). It's a deliberate trade: maximally free for individuals, while keeping commercialisation a conversation.

**Commercial licensing:** open an issue titled `[commercial]` or reach me via the contact details on my GitHub profile.

## Contributing

Issues and PRs welcome — start with [`CONTRIBUTING.md`](CONTRIBUTING.md). Contributions are accepted under the project license; substantial contributors will be asked to sign the [CLA](CLA.md) (needed to keep dual/commercial licensing possible — you keep ownership of your work). The bar for merging into the event log and durability code is high — read [`build/TESTING.md`](build/TESTING.md) first; if your change touches the write path, it ships with crash-matrix coverage or it doesn't ship.

Found a security problem? Don't open a public issue — see [`SECURITY.md`](SECURITY.md), which also spells out precisely what "audited" does and doesn't mean here. Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).

---

*Cairn: leave a stone, mark the trail, let the next traveller find the way.*
