# Cairn P0 — Dogfood Quickstart (the 30-handoff evaluation)

P0's engineering gates are green (see `PROGRESS.md`). What remains is the
**product** evaluation: ≥30 genuine cross-session handoffs across ≥3 agent
surfaces, measured with the diary protocol below. Product gates: first-query
Success@5 ≥ 70%, median time-to-useful-context < 60 s (or ≥50% below the
copy-paste baseline), manual-workaround rate ≤ 25%.

## 1. Install (target: under 10 minutes)

```sh
git clone https://github.com/ggoosen/cairn && cd cairn
make build                      # builds bin/cairn (needs Go 1.23+, CGO, git)
sudo cp bin/cairn /usr/local/bin/ 2>/dev/null || export PATH="$PWD/bin:$PATH"

cairn init                      # creates ~/cairn + device identity (FileVault required)
cairn daemon &                  # or install autostart (step 5)
cairn send "hello cairn" && cairn search hello
```

Back up the root key now (`cairn init` printed its path) to offline storage.

## 2. Optional: real-model embeddings

Without this, cairn runs lexical-only (fully functional; `retrieval_mode`
says so). To enable semantic search with the pinned `all-MiniLM-L6-v2`:

```sh
python3 -m venv ~/cairn/.cairn/embed-venv
~/cairn/.cairn/embed-venv/bin/pip install sentence-transformers
# restart the daemon, then backfill:
cairn reindex --semantic
```

(Or point `CAIRN_EMBED_PYTHON` at any python with sentence-transformers.)

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
    `cairn search "<query>" --budget 4000` and `cairn fetch <message-id>
    --view <view>`; fetched bodies land in `views/<view>/fetched/`."*
- **chat-agent copy/paste view**: run `cairn digest --view chat-scratch
  --budget 4000` and paste the output into the chat; paste the agent's
  conclusions back via `cairn send - --actor chat-scratch < notes.md`.

Regenerate digests any time: `cairn digest --view <name> --budget 4000`.

### 3b. MCP surface (P1 N1): Claude Desktop / Claude Code

With the daemon running, any MCP client gets the nine §5.5 tools
(`cairn_digest/search/peek/fetch/send/reply/signal/outcome/why_ranked`)
over stdio. Claude Desktop — add to
`~/Library/Application Support/Claude/claude_desktop_config.json`:

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

Every content-bearing result arrives in the untrusted-content envelope
(`trust: "untrusted"` + full provenance); budgets default to 1500 chars
(digest) / 2000 (search) and are tunable per call. There is no
force-class or topic auto-creation from MCP, by ruling (R20/R21).

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
```

`cairn run` mints a 24h session handle, exports it as `CAIRN_SESSION`, and
revokes it when the command exits (6h idle auto-revoke is the backstop).
Profiles: `full` (operator), `agent-standard` (read + send/reply + signal +
outcome; no retract/topics/pins/admin — the MCP default), `read-only`.
Custom profiles: `profiles.toml` next to the device key (capabilities from
`read, send, signal, outcome, admin`). `cairn mcp` is never tier-1 (R21):
it uses the handle it was launched under or mints one from `--profile`.

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

Autostart the daemon (macOS):

```sh
sed "s|CAIRN_BIN|$(command -v cairn)|; s|CAIRN_DIR_PATH|$HOME/cairn|; s|HOME_PATH|$HOME|" \
  scripts/com.cairn.daemon.plist > ~/Library/LaunchAgents/com.cairn.daemon.plist
launchctl load ~/Library/LaunchAgents/com.cairn.daemon.plist
```

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
launchctl unload ~/Library/LaunchAgents/com.cairn.daemon.plist  # or Ctrl-C
cp /Volumes/OfflineUSB/cairn-root.key /tmp/root.key
cairn device approve req.json --root-key /tmp/root.key --grant grant.json
rm -P /tmp/root.key                                        # REMOVE the key
# enable the listener (device-local config next to the device key):
#   sync_listen = "100.x.y.z:9700"     ← your tailnet IP (never 0.0.0.0)
launchctl load ~/Library/LaunchAgents/com.cairn.daemon.plist

# NEW machine: install the identity and prove membership
cairn device join grant.json
cairn sync ping 100.x.y.z:9700         # mutual auth; no data flows yet (N6)

# management
cairn device list                       # member / REVOKED
cairn device revoke <device-id> --root-key <restored>   # offline, then restart daemon
```

Requests expire after 1h and are single-use (R28). Refused connections are
logged with the presented identity on the daemon's stderr. A joined node
becomes fully operational (digest/search) when N6 replication lands; until
then `sync ping` is the membership proof.
