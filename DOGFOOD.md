# Cairn P0 — Dogfood Quickstart (the 30-handoff evaluation)

P0's engineering gates are green (see `PROGRESS.md`). What remains is the
**product** evaluation: ≥30 genuine cross-session handoffs across ≥3 agent
surfaces, measured with the diary protocol below. Product gates: first-query
Success@5 ≥ 70%, median time-to-useful-context < 60 s (or ≥50% below the
copy-paste baseline), manual-workaround rate ≤ 25%.

## 1. Install (target: under 10 minutes)

```sh
git clone https://github.com/ggoosen/cairn && cd cairn
make build                      # builds bin/cairn (needs Go 1.25+ — or 1.21+ with
                                # GOTOOLCHAIN=auto — plus CGO and git)
sudo make install               # -> /usr/local/bin (stops nothing; restart hint if a daemon runs)
# NO ROOT? (e.g. an automated audit agent with no passwordless sudo):
#   make install PREFIX=$HOME/.local   # -> ~/.local/bin  (add it to PATH)
#   export PATH="$HOME/.local/bin:$PATH"

cairn init                      # creates ~/cairn + device identity (FileVault required)
cairn daemon --install          # RECOMMENDED: launchd (macOS) / systemd --user (Linux),
                                # supervised + auto-restart so the daemon is never
                                # hand-babysat and never left running a stale binary.
                                # (`cairn daemon &` still works for a quick look.)
cairn send "hello cairn" && cairn search hello
```

Back up the root key now (`cairn init` printed its path) to offline storage.

## 2. Optional: real-model embeddings

Without this, cairn runs lexical-only (fully functional; `retrieval_mode`
says so). To enable semantic search with the pinned `all-MiniLM-L6-v2`:

```sh
# parity across macOS and Linux (FIX-G5): one script provisions the pinned venv
scripts/cairn-embed-bootstrap.sh ~/cairn
# restart the daemon, then backfill:
cairn reindex --semantic
```

Equivalent manual steps (any platform):

```sh
python3 -m venv ~/cairn/.cairn/embed-venv
~/cairn/.cairn/embed-venv/bin/pip install sentence-transformers
```

(Or point `CAIRN_EMBED_PYTHON` at any python with sentence-transformers.)

If no venv is provisioned, the daemon now says so LOUDLY at every startup
(R45): `embeddings: no embed venv found (semantic search disabled ...)`. A
node running lexical-only is never a silent surprise again. `cairn status`
names the CAUSE — no embedder configured, the degradation ladder shedding the
vector query, or a configured embedder that is failing — because the remedy
differs in each case. With no embedder configured the unembedded backlog is
NOT counted as degradation debt: nothing can work it off, so shedding
summaries and auto-links would buy nothing and never end.

## 3. Wire the three agent surfaces

```sh
cairn setup-agent claude-project-a --interest "topics project A cares about"
cairn setup-agent claude-project-b --interest "topics project B cares about"
cairn setup-agent chat-scratch
```

Per surface:

- **Claude Code project A / B**: add to each project's CLAUDE.md:
  - *"At session start, read `~/cairn/views/<view>/digest.md`. To record
    decisions/findings for other sessions, write a `.md` file into
    `~/cairn/views/<view>/outbox/` (front-matter optional:
    `action/text_class/declared_priority/topic_ids`). To retrieve, run
    `cairn search "<query>" --budget 4000` (or `--budget-tokens`) and `cairn fetch <message-id>
    --view <view>`; fetched bodies land in `views/<view>/fetched/`.
    Before ending a session, publish ONE handoff note — decisions and
    their reasons, unfinished work, surprises — with `cairn send --topic
    <project> --priority 2`. That note is the session's single mandatory
    write; everything else stays signal, not noise."*
- **chat-agent copy/paste view**: run `cairn digest --view chat-scratch
  --budget 4000` and paste the output into the chat; paste the agent's
  conclusions back via `cairn send - --actor chat-scratch < notes.md`.

Regenerate digests any time: `cairn digest --view <name> --budget 4000`.

### 3b. MCP surface (P1 N1): Claude Desktop / Claude Code

With the daemon running, any MCP client gets the twelve tools — the nine
§5.5 tools (`cairn_digest/search/peek/fetch/send/reply/signal/outcome/
why_ranked`), `cairn_thread` for thread expansion, and the two R55
local-tier subscription tools (`cairn_subscribe/cairn_subscriptions`) —
over stdio. Claude Desktop — add to
`~/Library/Application Support/Claude/claude_desktop_config.json` (on Linux:
`$XDG_CONFIG_HOME/Claude/claude_desktop_config.json`, i.e. `~/.config/Claude/`
by default — `cairn mcp-install` finds either):

```json
{
  "mcpServers": {
    "cairn": {
      "command": "/usr/local/bin/cairn",
      "args": ["mcp", "--view", "claude-desktop", "--actor", "claude-desktop"]
    }
  }
}
```

Claude Code: `claude mcp add cairn -- cairn mcp --view claude-code --actor
claude-code`. Use the absolute binary path (GUI apps don't inherit your
shell PATH). One view per client keeps digests and telemetry attributable.

Other harnesses (Hermes agent, OpenClaw-style — anything that launches a stdio
MCP server) are wired the same way; see
[`docs/memory-provider.md`](docs/memory-provider.md) for per-harness config and
the capability profile.

Every content-bearing result arrives in the untrusted-content envelope
(`trust: "untrusted"` + full provenance); budgets default to 1500 chars
(digest) / 2000 (search) and are tunable per call. A caller that thinks in
tokens can pass `budget_tokens` instead of `budget_chars` — exactly one of
the two; both together is refused rather than silently resolved — and every
budgeted response carries a `budget` block naming the mode, the limit, the
**tokenizer** and what the payload cost. Today's token counter is
`cairn-approx-v1`, a deliberate OVER-estimate rather than a BPE tokenizer,
and it flags itself `approximate: true`; size a real context window with
`budget_chars` if you need an exact number. There is no force-class or topic
auto-creation from MCP, by ruling (R20/R21).

**N1 acceptance leg (operator):** in Claude Desktop against the live mesh,
run one full round-trip — digest → search → fetch → send → outcome — and
confirm each result shows the envelope. The protocol-level equivalent is
already automated (`internal/mcp` tests).

### 3c. Capability profiles and the trusted launcher (P1 N2)

The rulings §7.2 tier system is live. Your bare CLI stays **tier-1 full**
(nothing changes for you). Agent surfaces run confined:

```sh
cairn run --profile agent-standard --name claude-code-a -- claude
cairn run --profile read-only --name viewer -- some-tool
cairn session list          # live handles (token prefixes only)
cairn session revoke <tok>  # end one immediately
cairn session prune         # reap expired/dead-process handles now
```

`cairn run` mints a 24h session handle, exports it as `CAIRN_SESSION`, and
revokes it when the command exits (6h idle auto-revoke is the backstop).
Profiles: `full` (operator), `agent-standard` (read + send/reply + signal +
outcome; no retract/topics/pins/admin — the MCP default), `read-only`.
Custom profiles: `profiles.toml` next to the device key (capabilities from
`read, send, signal, outcome, admin`). `cairn mcp` is never tier-1 (R21):
it uses the handle it was launched under or mints one from `--profile`.

**Resource selectors (D3, spec §7.2)** narrow a handle to a *subtree* as
well as a tier — the grant an operator actually wants for a narrow agent:

```sh
cairn run --profile agent-standard --name narrow \
  --topic 'project/x/*' --max-budget-chars 1500 -- claude
```

Grants are **positive only** (there is no "everything except"; mutes are a
separate, still-open question). `*` spans `/`, so `project/x/*` is the whole
subtree — and it does *not* include the bare parent topic `project/x`; pass
both if you mean both. Inside the grant everything works normally; outside
it every op is a **typed refusal** carrying `out_of_scope`, never an empty
result, so an agent can tell "nothing matched" from "you may not ask". That
includes `thread`, which crosses topics by construction: out-of-scope
messages are withheld and counted, and a wholly out-of-scope thread is
refused. Whole-mesh renderings (`cairn map`, `cairn compact`) are refused
outright under a topic grant. `--max-budget-chars` clamps every retrieval and
the clamp is reported in the response — never applied silently. A session
capped in characters may still budget in TOKENS: the cap then applies as a
second hard ceiling beside the token budget (both are reported) rather than
being converted, because there is no honest characters-per-token rate to
convert it by. `cairn
session list` shows each handle's grant, so an audit distinguishes
"read-only" from "read-only inside project/x".

**Isolation honesty (R22):** everything runs as your OS user. Profiles
prevent *accidents* — a confined agent structurally cannot retract or
restructure the mesh — they do not defend against a malicious local
process, which could talk to the daemon socket directly. Treat them as
guardrails, not a security boundary.

**N2 acceptance leg (operator):** wrap one real agent session in
`cairn run --profile read-only` and watch its send get refused with a
capability error while search/digest keep working.

## 4. The 30-handoff diary protocol (human-measured gates)

A **handoff** = one session genuinely needing context another session
produced. For each handoff:

1. Note the wall-clock time you START looking for context.
2. Query cairn (`search`/`digest`). Every response carries an
   `interaction_id`.
3. Record the outcome — this is the whole protocol:
   - `cairn found <interaction-id> --message <id>` — a top result was the
     needed context (note: within top-5?)
   - `cairn not-found <interaction-id>` — cairn had it but retrieval missed
   - `cairn manual-workaround <interaction-id>` — you gave up and re-derived
     or copy-pasted manually
4. Note the time you HAD usable context (time-to-context, for the diary).

**Baseline week**: before (or alongside) the first handoffs, do 5–10
handoffs the OLD way (scrollback copy-paste) and record their
time-to-context in the diary — that's the comparison baseline.

Diary: keep `~/cairn-diary.md` with one line per handoff:
`<date> <surface> <interaction_id> <outcome> <seconds-to-context> <notes>`.

## 5. Weekly review

```sh
cairn gates      # engineering gates recomputed + product gates so far
cairn doctor     # log integrity
cairn doctor conflicts   # unresolved merge debt
```

Autostart the daemon (macOS or Linux — installs and starts the user service;
supersedes the old hand-edited-plist recipe):

```sh
cairn daemon --install
```

(`cairn daemon --stop` / `--restart` manage it afterwards — no launchctl
incantations needed.)

## 6. Backups (portable data only)

```sh
scripts/cairn-backup.sh ~/cairn ~/Backups/cairn        # excludes keys + derived
scripts/cairn-restore-drill.sh ~/Backups/cairn /tmp/cairn-drill  # proves new-origin behavior
```

Device identity is intentionally NOT in backups: a restore is a NEW origin
(`cairn init --adopt`), old history is preserved read-only. The root key is
backed up separately, offline, once (see step 1).

## 7. Importing an existing knowledge base (optional)

To seed cairn from a docs tree or llm-wiki style repo:

```sh
cairn ingest scan ~/notes/team-wiki          # writes cairn-ingest-manifest.json
$EDITOR cairn-ingest-manifest.json           # review the plan (publish/revise/skip)
cairn ingest apply                           # idempotent; provenance via source_ref
```

Re-running scan+apply after edits revises only the changed pages. Imported
messages carry `source_ref` (repo/path/hash) and `relates_to` ([[wiki
links]] resolved to message ids); topics mirror the directory structure.

## 8. Deferred items to run once during the evaluation

- Overnight 1M scorecard: `CAIRN_SCORECARD=1000000 go test -tags sqlite_fts5
  -run TestScorecard -v -timeout 600m ./internal/daemon/` — append the row
  to PROGRESS.md M7.
- After provisioning the embed venv, re-run the golden corpus informally
  (`cairn search` spot checks) and note real-model quality in the diary.

## Exit

After ≥30 handoffs: `cairn gates`. If product gates miss, the P0 ranking
CONFIGURATION is falsified first — one documented tuning pass on held-out
tasks (constants live in `internal/config/constants.go`) before any thesis
conclusions (rulings §10).

## 9. Durable subscriptions (P1 N3)

Standing semantic interests that survive restarts and (post-N6) replicate:

```sh
cairn subscribe "council planning approval" --view roastery --durable
cairn subscription list
cairn subscription disable <id>       # stops delivery; history kept
cairn subscription update <id> --base-revision <n> --query "..."
```

Matches surface in that view's next digest marked `[subscription]`, after
recipients/pins, inside the same budget. Calibration is relative — no
similarity thresholds to tune; caps default to 10 matches/24h and 20/day
(flags: --mode, --top-n, --window-hours, --percentile, --push-cap).
Without `--durable` the command just updates the view's LOCAL view.json
(no events). Requires the real embedder (§2) for genuine semantic
matching — the dev embedder only matches shared words.

**Agent self-service (R25 local tier).** The non-`--durable` form is safe for
**agents to run themselves** — it touches only the caller's own view, mints no
events, and can't escalate capability. This is taught in the agent-facing
instructions (the `cairn` skill and each repo's `CLAUDE.md` Cairn block) as
`cairn subscribe "<what this project works on>" --view <VIEW>`, and over MCP as
`cairn_subscribe` (with `cairn_subscriptions` to inspect it). The `--durable`,
replicated tier stays **operator-only** and is never exposed to MCP.

### 9a. Self-bootstrapping onboarding record (R56)

Stop hand-editing every project's `CLAUDE.md`. Publish ONE operator record per
view; fresh sessions self-configure from it (and self-heal when you update it):

```sh
# operator: publish/update the record for a view
cairn onboarding publish --view cairn \
  --interest "council planning approvals and roastery ops" \
  --topic cairn/affordance --topic roastery/ops \
  --budget 1500 --note "Human prose — not machine-applied."

cairn onboarding show  --view cairn        # fetch + verify; prints the config or the refusal
cairn onboarding apply --view cairn        # what a session runs (idempotent)
```

`publish` writes a canonical message on `cairn/onboarding/<view>` carrying a
fenced ```` ```cairn-onboarding ```` block (`view`, `interest_query`, `topics`,
`digest_budget`). `apply` (run by the agent, taught in the skill) fetches the
**latest** record on that topic, and — **only if it was authored by the operator
principal** — sets the caller's local (R25) interest/topics and rewrites the
delimited `<!-- cairn:onboarding start/end -->` block in `CLAUDE.md`
(`--instructions <path>` to target another file). It is the **one** trusted-config
exception (R56), bounded on three axes: operator authorship, the four-field
schema whitelist (unknown fields + all prose ignored — never a directive, no
command execution), and effect limited to local config + the delimited block.
A non-operator record is refused (`NOT applied`) and stays readable only as
untrusted data. Update the record any time; sessions re-apply on the revision
bump. (Note: the "authoritative" record is the latest operator message on the
topic — Cairn pins are object-durability, not message pins; see RULINGS R56.)

## 10. Attachments, derivatives, and sender summaries (P1 N4)

```sh
cairn send "audit attached" --attach report.pdf --summary "Q3 fire safety audit"
cairn derivative list <message-id>      # extractor provenance per attachment
cairn derivative summary <message-id>   # sender claim + receiver verdict
```

Attachments (PDF/HTML/docx/text, ≤16MiB) become searchable within seconds
via sandboxed deterministic extraction — search hits carry full provenance
back to the source blob. A sender `--summary` is an untrusted claim: the
receiver embeds it against the body, and a divergent claim gets a
`[summary-disputed]` marker in digests plus a locally computed extractive
summary (needs the real embedder, §2). For the F9 retrieval benchmark on
the real model: `cairn bench golden --embedder real`.

## 11. Two-node enrolment ceremony (P1 N5 — operator runbook)

The mesh grows by ROOT-SIGNED enrolment; Tailscale is transport, never
authorization (R27). The root key touches disk only during this ceremony.

```sh
# NEW machine (e.g. the WSL2 box):
cairn device enroll --name windows-wsl --out req.json     # key stays here

# THIS machine (signing): stop the daemon, restore the root key
cairn daemon --stop
cp /Volumes/OfflineUSB/cairn-root.key /tmp/root.key
cairn device approve req.json --root-key /tmp/root.key --grant grant.json
rm -P /tmp/root.key                                        # REMOVE the key
# enable the listener (device-local config next to the device key):
#   sync_listen = "100.x.y.z:9700"     ← your tailnet IP (never 0.0.0.0)
cairn daemon --restart

# NEW machine: install the identity and prove membership
cairn device join grant.json
cairn sync ping 100.x.y.z:9700         # mutual auth; no data flows yet (N6)

# management
cairn device list                       # member / REVOKED
cairn device revoke <device-id> --root-key <restored>   # offline, then restart daemon
```

Requests expire after 1h and are single-use (R28). Refused connections are
logged with the presented identity on the daemon's stderr. A joined node
becomes fully operational (digest/search) once N6 replication lands (below);
`sync ping` remains the zero-data membership probe.

## 12. Peer sync — reconciliation (P1 N6)

Once two machines are enrolled (§11), point them at each other and knowledge
replicates automatically. Peers are LIVE + persisted — no TOML edit, no
daemon restart (a `pair join` registers its counterparty automatically):

```bash
cairn peer add 100.a.b.c:9700   # the other machine's tailnet IP:port
cairn peer list                 # what this node dials
```

(`sync_listen` stays a device-config key — `"auto"` detects the tailnet
interface. The old hand-edited `sync_peers` TOML field is still honored,
but `cairn peer add` is the supported path.) From then on:

- **Automatic**: every send pushes to connected peers immediately, and an
  anti-entropy sweep reconciles every 5 minutes (both config-tunable). A
  freshly-joined machine converges the whole mesh on its first sweep.
- **On demand**:
  ```bash
  cairn sync now                 # reconcile every configured peer now
  cairn sync now 100.a.b.c:9700  # reconcile one peer
  cairn sync status              # per-origin frontiers + configured peers
  ```

What replicates in N6: the event log (all origins) and the searchable text
corpus (canonical + eager message bodies; ephemeral only to a currently
connected peer). Attachments/derivatives replicate in N7. Every replicated
record is hash+signature verified before it is stored or indexed; a node that
falls far behind is caught up by streaming whole segments. Convergence is
crash-safe and idempotent — a re-run of `sync now` after any interruption
simply re-fetches what is missing. Reindex on any node
(`cairn reindex --lexical`) reproduces identical canonical search results.

## 13. Blob durability (P1 N7)

Attachments (sent with `cairn send --attach`) are content-addressed blobs
replicated across the mesh with a durability class:

```bash
cairn send "quarterly numbers" --attach report.pdf --durability normal
```

| class      | replica target                         |
|------------|----------------------------------------|
| ephemeral  | origin only (never replicated)         |
| normal     | ≥ 2 nodes (default)                    |
| important  | all your machines (operator nodes)     |
| pinned     | all your machines (per policy)         |

The send never blocks on replication: it acks `accepted_locally` immediately,
and the receipt/`cairn send` output carries `replication` (e.g. `pending`,
target 2, have 1). Replication is satisfied asynchronously by the sync sweep —
when another node reconciles, it fetches the blob (verifying the hash),
becomes a holder, and both nodes see the target met.

Check live durability any time:
```bash
cairn sync status          # includes each blob's have/target/satisfied
cairn doctor               # (deep) verifies present blobs + reports pending ones
cairn gates                # "blob durability targets (N7)" row
```
A message whose attachment is still below target shows `[replication-pending]`
in the digest. A blob is only ever counted as held by a node when that node
has a complete, hash-verified copy — an interrupted transfer is never counted.

## 14. Fork (equivocation) detection and repair (P1 N8)

If a device's full state (key + log) is ever cloned and both copies then write
different events, that is an equivocation — the same origin/sequence with two
different, validly-signed events. cairn cannot prevent a full-disk clone, but
it DETECTS the fork the moment the two logs meet during sync, and never merges
or deletes either branch.

On detection cairn automatically:
- **freezes** that origin (it stops syncing; every OTHER origin keeps going),
- **quarantines** the divergent branch under `.cairn/quarantine/` (kept forever),
- logs loudly and fails the `cairn gates` "no unresolved forks" row.

Inspect it:
```bash
cairn doctor fork                 # list all forks
cairn doctor fork <origin-device> # common ancestor, both branches, the peer
```

Repair (offline — stop the daemon, restore the root key from offline storage):
```bash
cairn fork resolve <origin-device> --canonical local --root-key /path/to/root.key
#   --canonical local  = keep YOUR branch; the other branch's messages are
#                        reissued under a recovery origin (with provenance:
#                        recovered_from_event_id + fork_resolution_id)
#   --canonical remote = the other branch is authoritative instead
```
This records a root-signed `device.fork.resolve`, reissues the losing branch's
content so nothing is lost, and marks the fork resolved. **Both branches are
preserved** — the losing branch's original frames stay in `.cairn/quarantine`.

Then complete the security follow-up (the cloned certificate must not keep
writing):
```bash
# if the cloned device is a DIFFERENT device you own:
cairn device revoke <origin-device> --root-key /path/to/root.key
# if YOUR OWN device was cloned: run `cairn migrate` first (new identity),
# then revoke the old (cloned) certificate as above.
```
Finally remove the restored root key and restart the daemon.

## 15. Audit rig setup (so the crossed two-node re-audit can actually run)

The N9 re-audit died on `sudo` (an agent shell has no passwordless root) and on a
STALE deployed binary, so the whole two-node fault matrix went unrun. Set the rig
up once, this way, and an auditor agent can drive it end to end.

**Both nodes — install HEAD without root, run supervised, confirm the version:**

```sh
cd ~/projects/cairn && git pull        # BOTH nodes on the SAME commit (git rev-parse HEAD must match)
make install PREFIX=$HOME/.local       # no sudo needed; -> ~/.local/bin
export PATH="$HOME/.local/bin:$PATH"
cairn --version                        # MUST be p1-<sha>, never p0-; warns if a stale daemon is still running
cairn daemon --install                 # launchd (macOS) / systemd --user (Linux) — supervised, no hand-babysitting
```

**NODE-A — REMOVE `sync_listen` and `sync_peers` from the device config.** They
currently MASK the G2 auto-listener gate (R44): with `sync_listen` pinned by
hand, you never exercise the auto-detect path the audit is meant to verify. Delete
both keys and let R44 bind `<tailnet-ip>:9700` on its own; confirm with:

```sh
cairn sync status                      # reports the listener it auto-bound (or the loud reason it did not)
```

(NODE-B may keep an explicit `sync_peers` pointing at NODE-A, or rely on the same
auto path — the point is that at least one node proves the stock listener.)

**SSH-over-tailnet to NODE-B** so the auditor can drive both nodes from one shell
(`ssh nodeb.<tailnet>`). The signing ceremony (root key) stays on the operator's
machine and never touches an agent's hands — the auditor drives sends, syncs,
kills, and doctor, not the root-key ceremony.

**Rig invariants the auditor should assert first (Phase 0):**
- `git rev-parse HEAD` identical on both nodes;
- `cairn --version` is `p1-<sha>` on both, and matches the running daemon (no
  stale-binary warning);
- **checkout ⇄ installed-binary parity:** the sha in `cairn --version` MUST equal
  `git rev-parse --short HEAD` in the checkout you built from;
- NODE-A has no `sync_listen`/`sync_peers` in its device config;
- both daemons are under launchd/systemd (`cairn daemon --install`), not hand-run.

**Deploy-flow provenance (FIX-J4.1 — why the checkout/binary can diverge).** In
the N9 run-2 rig, NODE-B's discovered checkout was `a036060` while its INSTALLED
binary was `ccdf1dc` — because the rig was restored by shipping a **git bundle**
node-to-node (see `docs/cairn-rig-restoration-runbook.md`) and B's working tree
was left a few commits behind the binary that had already been `make install`ed
from a later bundle. It was not causal for any finding, but it makes provenance
un-auditable ("which commit produced this behaviour?"). The version string
derives from VCS build info (R11), so a mismatch is always detectable. Standing
rule for every node:

```sh
cd ~/projects/cairn && git pull                 # bring the CHECKOUT to HEAD
git rev-parse --short HEAD                       # e.g. ccdf1dc
make install PREFIX=$HOME/.local                 # rebuild the BINARY from that checkout
cairn --version | grep -q "$(git rev-parse --short HEAD)" \
  && echo "parity OK" || echo "STALE: binary != checkout — reinstall"
```

Do this on BOTH nodes before any audit phase; never `make install` from one
bundle and then leave the checkout on an older one.
