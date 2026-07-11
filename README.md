# Agent Mesh — P0 Build Pack

Everything Claude Code needs to build Agent Mesh P0: a local-first,
crash-safe message + knowledge daemon for AI agent sessions. The design
survived five rounds of adversarial LLM review; this pack is the frozen
output of that process.

## How to use

```bash
mkdir agent-mesh && cd agent-mesh
unzip ~/Downloads/agent-mesh-buildpack.zip -d .
git init
claude
```

Then start with:

> Read CLAUDE.md and follow its read order. Then begin Milestone M0 from
> build/BUILD-PLAN.md. Work one milestone at a time. Maintain PROGRESS.md.

## What's in the pack

```
CLAUDE.md                          # Claude Code's standing instructions (read first)
README.md                          # this file
docs/
├── spec-v0.3.md                   # full specification (P0 scope = §12)
├── rulings-v0.3.1.md              # BINDING build rulings — highest authority
└── design-brief-v0.2-HISTORICAL.md# background only; never implement from this
build/
├── ARCHITECTURE.md                # condensed implementation architecture
├── BUILD-PLAN.md                  # milestones M0–M8, tasks, acceptance criteria
├── TESTING.md                     # crash/fault matrix (the zero-loss gate)
├── schemas/p0-events.schema.json  # normative event payload schemas
└── sql/projection.sql             # SQLite projection DDL
```

Document precedence when anything conflicts:
**rulings-v0.3.1 > spec-v0.3 > CLAUDE.md > build/ files.**

## Session tips

- One milestone per session works well; M1 (event log + crash tests) may
  take two. Never let a session skip acceptance criteria.
- If Claude Code flags `RULING-NEEDED` items in PROGRESS.md, resolve them
  yourself or bring them back to the design conversation before they
  compound.
- The definition of done is `mesh gates` green on the engineering gates
  (zero acknowledged-event loss, 100% provenance, 100% budget compliance,
  P95 lexical visibility < 200 ms) — then the 30-handoff human evaluation
  (M8 / DOGFOOD.md) decides whether P1 gets built.

## What P0 is (and is not)

Single machine, macOS-first, no networking. Event log + object store +
SQLite/FTS/vector projection + ranked budget-capped digest/search + outbox
+ exports with 3-way merge + telemetry + crash safety. No MCP, no
subscriptions, no knowledge maps, no maintenance economy — those are
P1–P4, and they ride on the same event log without migration.
