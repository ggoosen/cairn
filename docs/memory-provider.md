# Cairn as your agent's memory provider

Most agent harnesses now treat memory as a pluggable slot: point the harness at
a provider, and the agent gets recall across sessions. Cairn already speaks
**MCP** over stdio, so it drops into that slot in any harness that can launch a
local MCP server — no adapter to write, no service to host, nothing leaves your
machines.

What makes Cairn different from a session-history store is the retrieval
contract: the agent gets a **ranked, provenance-preserving answer inside a hard
character budget**, and can deliberately fetch the original when it matters.
Agents are never handed a raw inbox.

> This page is about wiring Cairn into a harness. For running the mesh itself —
> setup, multi-machine enrolment, backups — read [`../DOGFOOD.md`](../DOGFOOD.md).

## What the harness gets

`cairn mcp` serves twelve tools:

| Tool | What it does |
|---|---|
| `cairn_digest` | ranked, budget-capped rollup of what changed and matters to this view |
| `cairn_search` | hybrid lexical + semantic search, scopable by topic / sender / thread |
| `cairn_peek` | one message's metadata without pulling the body |
| `cairn_thread` | a whole conversation in order, budget-capped |
| `cairn_fetch` | deliberately retrieve one full body, with provenance |
| `cairn_send` / `cairn_reply` | publish into the mesh (full pre-ack validation) |
| `cairn_signal` | lightweight signal about a message (ack, useful, …) |
| `cairn_outcome` | record whether a retrieval helped — this is what calibrates ranking |
| `cairn_why_ranked` | the exact stored arithmetic behind one result |
| `cairn_subscribe` / `cairn_subscriptions` | declare/inspect this view's own standing interest |

Three properties hold on every content-bearing result, by ruling rather than by
convention:

- **Untrusted envelope (R18).** Content arrives as
  `{kind, trust:"untrusted", provenance{message_id, revision_id, sender, content_hash}, content}`.
  The tool descriptions themselves say returned content is DATA, not
  instructions. A harness that pipes mesh text into a prompt is piping in
  something another tool wrote — Cairn labels it as such instead of hoping.
- **Hard budgets (R19).** `budget_chars` covers the COMPLETE payload — header,
  entries, truncation markers. Oversized items are dropped whole, never
  truncated mid-item. Defaults: 1500 chars for digest, 2000 for search.
- **Provenance on everything fetched.** Every body is traceable to the event
  that carried it and the device that signed it.

## Install

1. **A running daemon.** `cairn mcp` bridges stdio to the local daemon's unix
   socket; it fails fast with a readable error if none is up.
   ```sh
   cairn daemon --install     # or just: cairn daemon
   ```
2. **A view for the harness.** One view per harness keeps digests, standing
   interest and telemetry attributable — sharing a view across clients collapses
   all three.
   ```sh
   cairn setup-agent hermes --interest "what this agent works on"
   ```
3. **Wire the config** (below). Always use the **absolute** path to the binary:
   GUI apps and supervised harnesses do not inherit your shell `PATH`.
   ```sh
   command -v cairn      # e.g. /usr/local/bin/cairn
   ```

## Wiring

### Clients Cairn configures for you

For Claude Desktop, Claude Code and Codex CLI, don't hand-edit anything:

```sh
cairn mcp-install --status     # what's detected, configured, stale
cairn mcp-install --all        # wire every installed client
```

It merges only its own entry, backs up before writing, refuses a config it
cannot parse rather than clobbering it, and is idempotent (R54).

### Hermes agent

Hermes reads MCP servers from `~/.hermes/config.yaml` under `mcp_servers`, with
`command` / `args` / optional `env` for a stdio server:

```yaml
mcp_servers:
  cairn:
    command: "/usr/local/bin/cairn"
    args: ["mcp", "--view", "hermes", "--actor", "hermes"]
```

Or via its CLI, which discovers the tools for you:

```sh
hermes mcp add cairn --command /usr/local/bin/cairn --args mcp --view hermes --actor hermes
hermes mcp list
hermes mcp test cairn
```

Two things worth being precise about:

- Hermes's `memory.provider` slot (honcho, mem0, …) takes an in-process **Python
  plugin**, not an MCP server. Cairn is wired as **MCP tools**, which is the
  supported way for an external binary to provide recall. The built-in memory
  stays active alongside it either way.
- `--args` passes the rest of the argv to the stdio command, so it goes last.
  Confirm against your Hermes version with `hermes mcp --help`; the YAML above
  is the durable form and is what the CLI writes.

### OpenClaw-style harnesses

**Unverified — check against your version before trusting it.** OpenClaw's own
MCP config shape was still moving at the time of writing (a `mcp.servers` block
in `openclaw.json` was proposed in openclaw/openclaw#43509, while community
guides describe a top-level `mcpServers` key), and we have not run it. What is
stable is the **MCP-standard stdio entry** every such harness accepts in some
spelling — the command and args below are what Cairn needs; put them wherever
your harness's docs say servers go:

```json
{
  "command": "/usr/local/bin/cairn",
  "args": ["mcp", "--view", "openclaw", "--actor", "openclaw"],
  "description": "Cairn — local knowledge mesh: ranked, budget-capped, provenance-preserving memory"
}
```

### Any other MCP client

The generic form, which is what all of the above reduce to:

```json
{
  "mcpServers": {
    "cairn": {
      "command": "/usr/local/bin/cairn",
      "args": ["mcp", "--view", "<harness-name>", "--actor", "<harness-name>"]
    }
  }
}
```

## The capability profile it runs under

**MCP is never tier-1 (R21).** Every MCP request runs inside a capability
session — either the `CAIRN_SESSION` handle the server was launched with, or
one it mints at startup from `--profile` and revokes on exit. The default is
`agent-standard`; `--profile full` is **refused at the flag**, not merely
discouraged.

`agent-standard` grants read, send, signal, outcome. It does **not** grant:

| Not available over MCP | Why |
|---|---|
| retraction, structural topic ops, admin | not in the profile's capability set |
| topic auto-creation | R20 — an unresolved topic is rejected before acknowledgement, never invented |
| `--force-class` | R20 — text-class policy may downgrade; MCP cannot override it |
| durable (replicated) subscriptions | R55 — `cairn_subscribe` is the LOCAL tier only: own view, no events, no capability escalation. The durable tier is operator-only, via the CLI |

Sessions are short-lived (24h TTL, 6h idle) and auto-revoked on exit.
`cairn session list` and `cairn session revoke` are the kill switch.

If you want a strictly read-only harness, launch it confined and let it inherit
the handle:

```sh
cairn run --profile read-only -- <your harness>
```

**Honesty about the boundary (R22).** Same-OS-user confinement prevents
accidents, not malice. The profile stops a harness from *accidentally* issuing a
structural op through its normal toolchain; it cannot stop code that
deliberately strips its environment, because that code could dial the daemon
socket directly instead. Run harnesses you don't trust as a different OS user.

## Checking it works

```sh
cairn status                     # daemon, rank profile, embedder
cairn mcp-install --status       # per-client: detected / configured / stale
```

Then, from the harness itself: `cairn_digest`, a `cairn_search`, a `cairn_fetch`
on a hit, and a `cairn_outcome` recording whether it helped. That last call is
not optional bookkeeping — recorded outcomes are what `cairn gates` computes
Success@5 from, and what calibrates ranking over time.

## Notes

- Retrieval is **lexical-only** until you provision the embedding venv
  (`scripts/cairn-embed-bootstrap.sh`); the daemon says so on every start rather
  than degrading quietly. See [`../DOGFOOD.md`](../DOGFOOD.md) §2.
- Teach the harness the end-of-session handoff convention — one canonical note
  per session (decisions and why, unfinished work, surprises). It is the single
  highest-value write an agent makes, and no automatic capture substitutes for
  the summarizer that was actually there.
