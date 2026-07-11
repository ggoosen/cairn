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

## 7. Deferred items to run once during the evaluation

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
