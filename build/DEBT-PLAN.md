# DEBT-PLAN — the work with no phase to call home

Every other outstanding item belongs to a phase (P2/P3/P4) or a work order
(CAPTURE, EVAL). This file owns the rest: the scaling and distribution debt
that P0–P3 knowingly deferred, and the surfaces the spec describes but no
milestone ever built. They are collected here so that "what is left" has a
single answer per item, rather than living only as a row in ROADMAP.md.

Read `RULINGS.md` first; where anything here conflicts with it, the ruling
wins. Sizes are S/M/L on the same scale the other work orders use. Each
milestone states its acceptance criteria, because the next agent to pick
this up will not have the conversation that produced it.

---

## D1 — sqlite-vec integration (L)

**Why now.** `internal/projection/schema.sql:163` stores vectors in a plain
table and `HeadVectors` (rankq.go:347) loads **every** head vector into
process memory for an in-process cosine scan. `config.BruteForceMaxCandidates
= 5000` names the cliff. This is fine at today's corpus size and becomes the
binding constraint the moment CAPTURE C3 lands — transcript ingest is
precisely the feature that multiplies revision count by an order of
magnitude. CLAUDE.md pinned `asg017/sqlite-vec-go-bindings` as the intended
path; the brute-force scan was always the fallback, not the destination.

**What.**
- Feature-probe the extension at `projection.Open`. Load failure is NOT an
  error: log once, set a capability flag, keep the plain table + brute force.
  The daemon must start and serve on a machine where the extension will not
  load, exactly as it does today.
- When it loads, mirror vectors into a `vec0` virtual table and route
  `VectorTopK` through it. One writer, same transaction as the projection
  write — the checkpoint invariant in CLAUDE.md is not negotiable.
- Bump `config.ProjectionSchemaVersion` (currently 7). The auto-rebuild path
  at `daemon.go:372` already handles drift by replaying the log, so no
  migration is needed — but the rebuild must be exercised, not assumed.

**Acceptance.**
- Equivalence test: on a seeded corpus, vec0 and brute force return the
  **identical top-K** for a fixed query set. Brute force stays in the tree
  as the oracle, not as dead code.
- Extension-absent test: `projection.Open` succeeds, `cairn status` reports
  the vector path in use, retrieval results are unchanged.
- A corpus above `BruteForceMaxCandidates` answers a vector query without
  loading every vector into memory (assert via allocation or timing bound).
- `make verify` and `make test-race` green; the schema bump replays cleanly
  from a log written by the previous version.

## D2 — Origin-liveness beacon (M)

**Why.** Spec §13.2 and rulings §2 deferred this in P0 for a stated reason —
"requires peers" — and P1 has peers. The failure it catches is real and
silent: a device restored from portable data only mints a new origin, and a
device restored from a stale backup **regresses** its (generation, sequence).
Today `detectFrontierForkFromPeer` (reconcile.go:1284) catches divergence at
the same sequence; nothing catches a peer whose frontier moved *backwards*.

**What.** Persist the highest (generation, sequence) ever observed per origin
device. On each reconcile, compare the peer's advertised frontier against
that watermark; a regression raises a durable alarm surfaced by `cairn net`
and `cairn doctor`. Alarm, do not auto-quarantine: a regression is a strong
signal of operator error (restored backup), not of equivocation, and the
existing fork machinery already owns the equivocation case.

**Acceptance.** Two-daemon test where B is restarted from a stale copy of its
own state: A raises the alarm, names the origin and both watermarks, and
`cairn doctor` reports it. No false positive across an ordinary restart, an
ordinary catch-up, or a thin node that legitimately holds less.

## D3 — Capability `resource_selectors` (M)

**Why.** Spec §7.2 describes per-capability resource scoping —
`topic="project/x/*"`, per-session budget caps. P0/P1 shipped the coarse
action tiers only (`capRead`/`capSend`/`capSignal`/`capOutcome`/`capAdmin`,
session.go:34-48). A session can today be denied *writing*, but not confined
to a *subtree*, which is the grant an operator actually wants when handing a
capability to a narrow agent.

**What.** Extend the capability record with optional selectors; enforce at
the IPC dispatch boundary where the action tier is already checked, so the
enforcement point stays singular. Topic globs resolve through the existing
topic resolver — no second resolver. Per-session budget caps clamp the
`budget_chars` a session may request, and the clamp must be reported in the
response rather than applied silently.

**Acceptance.** A session granted `topic="a/*"` can search, digest and fetch
within `a/*` and is refused outside it — including via `thread`, which
crosses topics by construction. Refusals are typed, not empty results:
an agent must be able to tell "nothing matched" from "you may not ask".
Budget clamp appears in the response envelope. Grants remain positive-only
(no negative selectors) unless the mutes ruling below says otherwise.

## D4 — `budget_tokens` (M)

**Why.** Rulings §7 ruled `budget_chars` only for P0, with tokenizer budgets
post-P0; `retrieve.go:531` still refuses `budget_tokens` explicitly. Callers
are LLM sessions whose real constraint is tokens, and the char↔token ratio
varies enough by content (code, JSON, prose) that the current advice —
divide by four — is a guess the caller has to make.

**What.** Accept `budget_tokens` alongside `budget_chars` (exactly one, never
both). Count with a vendored tokenizer whose identity is recorded in the
response, because a budget is only meaningful against a named tokenizer.
The hard-budget property is unchanged: oversized items are dropped whole,
never truncated mid-item.

**Acceptance.** The existing budget property test runs in both modes over
every renderer. The response names the tokenizer and the mode used. A
request carrying both budgets is refused, not silently resolved.

## D5 — `cairn adopt-standalone` (S)

**Why.** R34 permits either a command or a documented script for merging an
ad-hoc standalone mesh into the primary; neither exists (PROGRESS N9). The
operator-facing failure is that a mesh started casually on a second machine
is currently a dead end.

**What.** Given R34's latitude, prefer the documented procedure first — it is
a rare, high-consequence operation and a script the operator reads before
running is safer than a verb that hides the steps. Promote to a command only
if the procedure proves mechanical. Either way it must state plainly what is
preserved (events, origins) and what is not.

**Acceptance.** A test or a rehearsed transcript that merges a standalone
mesh into a primary and ends with `cairn doctor` clean on both origins, with
no event loss from either side.

## D6 — Prebuilt signed binary + Homebrew tap (M; part [operator])

**Why.** README's Quickstart promises a "planned zero-dependency path" and
today's only path is build-from-source with a C toolchain. This is the
single largest adoption barrier for a tool whose pitch is "one command".

**What.** Release workflow producing macOS arm64 + Linux x86_64/arm64
artifacts with checksums, and a tap formula pointing at them. Codesigning
and notarization need an Apple Developer ID — that half is **operator**, and
the workflow should degrade to unsigned artifacts with an honest note rather
than block on it.

**Acceptance.** A tagged release produces downloadable artifacts whose
checksums verify; `brew install` on a clean machine yields a working `cairn`
with no Xcode toolchain present. The FIX-F4 guard still holds — a release
artifact must be a `sqlite_fts5` build, and the workflow must assert that.

## D7 — Mutes ([ruling] then S/M)

**Blocked, deliberately.** Spec §7.1/§4.5 list `mute(...)`; §7.2's stance is
positive grants only. No event, op or verb exists, and `grep '"mute"'`
returns nothing. These cannot both be right, and the resolution changes the
capability model rather than adding to it — so this needs an author ruling
before any code. Record the answer in RULINGS.md, then build or delete the
spec text. Do not implement a mute as a negative selector under D3 without
that ruling.

## D8 — Third ranking profile for `explore()` ([ruling] then M)

**Blocked, deliberately.** Spec §13.4 raises it as an open question and no
`explore` surface exists to profile. Deciding the weights before the surface
exists is backwards. If an exploration surface is wanted, it needs its own
design pass; until then this stays a question, not a task.

---

## Non-goals

- **Reranking with an LLM.** Breaks R47/R51 — the why-ranked record must
  reconcile exactly against an external recomputation, and a model's opinion
  does not reconcile. Same reasoning as CAPTURE-PLAN's non-goals.
- **Replacing brute-force cosine.** D1 keeps it as the test oracle. A vector
  path with no independent check is a silent-corruption surface.
- **Widening capabilities to negative grants** as a side effect of D3. That
  is D7's ruling to make.

## Sequencing

D1 first if CAPTURE C3 is coming, because C3 without it walks straight off
the brute-force cliff; D1 is otherwise the least urgent of the three code
items. D2 and D3 are independent of everything else and can run in parallel.
D4 and D5 are small and can fill gaps. D6 is worth doing before any public
push, and is the only item here whose value is entirely external. D7 and D8
are ruling-gated and should not be started as code.
