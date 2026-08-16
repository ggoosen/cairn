# BUILD-PLAN — the single source of truth for what is left to build

Everything outstanding, in one file: release blockers, capture, evaluation,
deferred debt, unbuilt spec surfaces, later phases, and the open author
rulings that block some of them. If a piece of work is not in this file, it
is not planned.

**Precedence:** `RULINGS.md` > `docs/rulings-v0.3.1.md` > `docs/spec-v0.3.md`
> this file > your judgment. Where a genuine contradiction or gap appears,
stop, record it under "Author rulings needed" in PROGRESS.md, mark the code
`// RULING-NEEDED:`, take the most conservative reading, and continue.

**Companions, none of which are plans:** `PROGRESS.md` (what shipped, per
milestone), `build/P0-BUILD-PLAN-HISTORICAL.md` (the completed P0 M0–M8
plan, kept for its acceptance criteria and decision trail),
`build/CAPTURE-C3-DESIGN.md` (design note for C3), `build/TESTING.md` (the
crash/fault matrix), `build/ARCHITECTURE.md`, `eval/claims.yaml` (the
machine-readable claims register).

**Maintenance rule:** new planned work gets a section here. Shipped work
moves to PROGRESS.md and its section is **deleted** from this file. Do not
start a new plan document; extend this one.

Legend: **[code]** buildable now · **[ruling]** needs an author ruling first
· **[operator]** human activity, not code · **[hardware]** needs the
two-machine rig · **[data]** needs real usage data first.

---

# Part I — Execution order

The rest of this file is the specification. This part is the order to work
it, and the honest reason each blocked item is blocked. **Nothing in the
first group needs anything from the operator**, so an agent can start there
immediately.

**Buildable now, in this order:**

0. **D9 session reaping** and **D10 ladder vs missing embedder** — live
   defects, not enhancements, so they go first. D9: `sessions.json` grows
   without bound (2,673 records observed on the dev node), the mint path is
   O(n) per session, and `cairn session list` has stopped being a usable kill
   switch. D10: the degradation ladder reads "no embedder" as "behind", so an
   idle laptop sheds summaries forever and the reported level misleads. Both
   were found by inspection on a live node, both carry a policy question that
   is recorded rather than blocking.
1. **D2 origin-liveness beacon** and **D3 capability `resource_selectors`** —
   independent of everything else, each closes a spec gap that exists today,
   each has a two-daemon or dispatch-boundary test that proves it. Best
   starting point: self-contained, no gate, real user-visible value. D3
   touches the capability model (R21/R22/R23), so it belongs in its own
   reviewable commit rather than batched with unrelated changes.
2. **E4** (intrinsic quality: nDCG/MRR/Recall, ablations, baselines) — the
   apparatus can be built and exercised against the sample corpus now; it
   stays *dark* (no reported numbers) until corpora land and the kill
   criteria are signed. Building it early is safe; reporting from it is not.
3. **E6** (adversarial/safety: prompt-injection compliance through
   digest/search/fetch) — needs no external corpus, only adversarial inputs
   the harness can author, so it is unblocked in a way E4/E5 are not.
4. **D4 `budget_tokens`** and **D5 `adopt-standalone`** — small, well-scoped.
5. **D1 sqlite-vec** — least urgent alone, but a hard prerequisite if C3 is
   coming, because transcript ingest walks straight off the brute-force cliff.

**Blocked, and by what:**

| Blocked on | Items |
|---|---|
| **Operator sign-off** | E1's 21 kill criteria — commitments about what would count as disproof; an agent must not set them |
| **Operator activity** | corpus acquisition (E3 residual → gates E4/E5/E9), the three release blockers in §1, C4 directory listings |
| **An author ruling** | ladder rungs 6–7 (§8.2 reserved-slice vs send-never-blocks), D7 mutes, D8 explore profile |
| **A crossed review** | C3 — the privacy/redaction model is the expensive thing to get wrong, so the design note lands before the code |
| **Real hardware** | P3 two-machine pass, live re-audit of code changed since the July commit |
| **Usage data** | P2 weight calibration — calibrating on synthetic episodes would fit noise |

**A standing rule for anything in EVAL:** apparatus may be built ahead of
sign-off, but no measurement is reported before its kill criterion is signed.
An unfalsifiable number is worse than no number, because it looks like
evidence.

---

# Part II — The work

## §1. Release blockers (before the first tag)

| Item | Kind | Source |
|---|---|---|
| **30-handoff product evaluation** — the "does it beat copy-paste?" gate. Success@5 / workaround-rate are computed by `cairn gates` from recorded outcomes; the handoffs themselves are the missing input. **As designed this is n=1, self-reported and uncontrolled — see E7, which strengthens it with pre-registration and randomized withholding rather than replacing it** | [operator] | DOGFOOD.md §4, spec §11 |
| Embed venv provisioning on each node (`scripts/cairn-embed-bootstrap.sh`) so the evaluation runs semantic, not lexical-only | [operator] | DOGFOOD.md §2 |
| Overnight 1M-event synthetic scorecard (`CAIRN_SCORECARD=1`, TESTING.md §5 — includes reindex/backup/restore/RSS numbers never recorded) | [operator] | build/TESTING.md, PROGRESS M7 |

## §2. CAPTURE — zero-effort capture

Provenance: competitive review of Hermes agent's session-memory design. The
conclusion was that Cairn is AHEAD on retrieval (hybrid RRF + budgets +
explainable ranking already shipped) but BEHIND on capture — Cairn only
learns what an agent deliberately `cairn send`s, so knowledge nobody
summarized dies in the session transcript.

**Shipped:** C1 handoff convention, C2 trigram FTS companion index, C4
memory-provider packaging (`docs/memory-provider.md`). See PROGRESS.md.

### C3 — Session-transcript ingest: the low-trust capture substrate (M/L) [code, after review]

A new **transcript source** on the existing M9 ingest path (`cairn ingest
scan/apply`) that ingests agent session transcripts (first adapter: Claude
Code JSONL; adapter interface open for others) so unsummarized knowledge is
FINDABLE without ever polluting the curated surface. Design note:
`build/CAPTURE-C3-DESIGN.md`.

Non-negotiable design constraints (these ARE the feature):

1. **Text class:** transcript chunks land as `eager-searchable` (or
   `ephemeral` when the operator prefers TTL); NEVER `canonical`. Existing
   downgrade policy and ceilings apply unchanged.
2. **Digest exclusion:** transcript-sourced messages are excluded from digest
   candidates by default. Note the constraint discovered during planning:
   `DigestCandidates` with an empty topic filter returns every non-retracted
   message, so a topic-namespace-only mechanism is insufficient — an explicit
   source flag in the projection is required. They are PULL-ONLY via search;
   "agents are never handed a raw inbox" stays true.
3. **Provenance:** `source_ref` records source path + session id + chunk
   position, so a search hit says "raw transcript, session X" — never dressed
   up as a curated note. Untrusted-content quoting applies (R18/R53).
4. **Privacy is opt-in per source directory.** Transcripts contain pasted
   secrets and tool output. `cairn ingest` must be pointed at a directory
   explicitly; a redaction pass masks recognizable token/key patterns at
   ingest, **before the object store** — a stored secret is durable and
   replicated — and the residual risk is documented plainly.
5. **Idempotent re-scan:** the same session file re-ingested produces no
   duplicate messages, so chunk boundaries must be deterministic.

Chunking: by conversational turn-group with a size cap (constants in config),
not fixed windows — turns are the natural provenance unit.

**Acceptance.** Ingest a real Claude Code session dir; a fact mentioned only
mid-session is found by `cairn search` with transcript provenance; the digest
for every view is byte-unchanged before vs after ingest; re-running ingest is
a no-op; a seeded fake API key does not appear in the object store; the fault
matrix stays green (ingest rides the ordinary publish path, so durability
ordering is inherited).

**Drags forward:** D1 sqlite-vec (transcript volume reaches the brute-force
cliff far sooner than curated traffic) and the duplicate/saturation penalties
in §5 (transcripts are highly repetitive; without penalties a scoped search
drowns in near-identical chunks).

### C4 residual [operator]

Submit provider-directory listings (awesome-hermes-agent, Hermes Atlas) now
that `docs/memory-provider.md` exists. External acceptance is out of our
control — record the submissions in PROGRESS.

### CAPTURE non-goals

- **LLM reranking / LLM summarization in the daemon:** breaks the R47/R51
  explainability contract — every ranking must reconcile to exact printed
  arithmetic. The agent-side handoff note (C1) is the sanctioned place for
  LLM distillation.
- **Query expansion models:** the shipped RRF hybrid already covers the
  different-terminology failure. Revisit only on measured recall failures.
- **Infinite history as an attention surface:** transcripts become a
  searchable substrate, never digest content. Do not weaken the digest/budget
  stance to make capture look richer.

## §3. EVAL — proving the thesis

Cairn makes strong claims. Almost none of them are currently *evidence*.
This part builds the mechanism that could falsify them.

The distinction that drives everything: Cairn measures whether **retrieval
returns the right document** (intrinsic). It has never measured whether **an
agent does better work** (extrinsic). The product thesis — "knowledge
compounds instead of being re-explained" — is extrinsic, so no amount of
intrinsic measurement can establish it.

**Shipped:** E2 harness skeleton (`eval/`, its own Go module), E3 corpus
format/normalizers/loader, E9.2 time-control hook. See PROGRESS.md.

### 3.1 The claims, and what actually backs them today

| Claim | Current evidence | Honest status |
|---|---|---|
| Zero acknowledged-event loss | Crash matrix in CI, deep doctor, fault injection | **Proven** (for the modelled fault set) |
| 100% budget compliance | Property test over budget sweeps; gate | **Proven** |
| 100% provenance on fetched | Automated gate | **Proven** |
| Send→visible P95 < 200 ms | Measured, 1.5 ms @ 100k | **Proven** (one machine) |
| Ranking is explainable | R47/R51 external-recompute reconciliation | **Proven** |
| Retrieval finds the right thing (Success@5 ≥ 70%) | Golden corpus — **synthetic, authored by the project author, queries and relevance judgments by the same person** | **Circular.** Validates configuration, not capability |
| Untrusted-content envelope protects agents | Structural — content is wrapped and quoted | **Assumed, never tested.** No adversarial input has ever been run at an agent through it |
| Budget-capped digest preserves what matters | Budget compliance is proven; *usefulness under the cap* is not | **Untested** |
| Cross-model knowledge transfer works | — | **Untested** (and it is the headline pitch) |
| Knowledge compounds / beats copy-paste | 30-handoff evaluation, not yet run | **Unstarted**, and as designed: n=1, self-reported, no control |
| Beats the alternatives (flat files, DB, transcript search) | — | **Untested.** The central competitive claim has never been measured against a single baseline |

### 3.2 The three methodological problems

**No control condition.** Everything measured is intrinsic. To support
"materially improves agent memory" you need the same tasks run *with* and
*without* Cairn, and against the alternatives people actually use. Without a
counterfactual there is no claim — only a description.

**Self-authorship at every layer.** The corpus, the queries, the relevance
judgments, the ranking weights, the operator, and the person interpreting the
results are all the same judgment. That is a structural property that makes
the numbers unusable as evidence for anyone else. Breaking this circularity
is the single highest-value change in this plan.

**No falsification criteria.** An evaluation that cannot fail is marketing.
§3.6 fixes this by pre-registering kill criteria BEFORE any measurement runs.

### 3.3 Architecture: same repo, separate module, black box only

`eval/` is its own Go module, not a package of the main one. Three
consequences, all deliberate:

1. **Black-box access is compiler-enforced, not conventional.** A separate
   module physically cannot import `github.com/ggoosen/cairn/internal/...` —
   Go's `internal/` visibility rule does the enforcement. The harness can
   only reach Cairn the way a real agent does: CLI and MCP. It cannot
   accidentally measure implementation details, and cannot be tuned against
   internals.
2. **Dependency isolation.** The harness needs LLM API clients and statistics
   libraries. The daemon's dependency tree is deliberately small and offline;
   those deps must never enter it.
3. **Property isolation.** The main suite is offline, deterministic, free, and
   gates every commit. Agent-in-the-loop evaluation is none of those. Mixing
   them would destroy the properties that make the main suite trustworthy.

It stays in-repo so it cannot rot out of sync with the surface it tests.

**Tiers:** **T0** offline/deterministic, no LLM, free, every commit · **T1**
intrinsic quality over independent corpora, LLM only as a relevance judge
where ground truth is thin, cents, nightly/pre-release · **T2** extrinsic
agent-in-the-loop, multi-model, multi-trial, real money, per release and
pre-registered.

The existing golden corpus stays, demoted to what it honestly is: a **T0
regression gate** that catches configuration drift.

### 3.4 Milestones

**E1 — claims register [operator sign-off].** `eval/claims.yaml` holds every
public claim, its measurement, its threshold, and its **kill criterion**.
Drafted: 21 claims. Residual: operator sign-off on the criteria. Nothing is
measured until its criterion is signed.

**E3 residual — acquire the corpora [operator].** Format, normalizers, loader
and sample are built; acquisition commands are in
`eval/corpora/ACQUISITION.md`. The cheap trick that makes independence
tractable: **mine free human-authored ground truth** — GitHub duplicate-issue
links (a maintainer marking #B a duplicate of #A *is* a relevance judgment by
someone with no stake in Cairn), Stack Overflow duplicate markers,
documentation cross-references, and real anonymized session transcripts.
Corpora are versioned and checksummed. Gates E4/E5/E9.

**E4 — intrinsic quality: ablations + baselines (M) [code].** Proper IR
metrics (nDCG@k, MRR, Recall@k), not just binary Success@5.
*Ablations* (does each component earn its complexity?): lexical-only ·
vector-only · RRF fusion · ±freshness · ±priority decay · ±mandatory
inclusion · P0 vs P2 profile. A component whose removal doesn't hurt should
be deleted; that is a *result*, not a failure.
*Baselines*: B0 no memory · **B1 grep over raw transcripts — the zero-effort
baseline and the one to beat** · B2 flat append-only markdown · B3 naive
vector-DB RAG · B4 full-context stuffing where the corpus fits · B5 Cairn.

**E5 — extrinsic task battery (L) [code, large].** THE THESIS. Handoff tasks
where session B needs something session A learned, with **objectively
checkable** success. Every condition from E4, multiple trials, paired where
possible, effect sizes and confidence intervals — not point estimates.
Primary metrics: task success rate · **rediscovery rate** (the thesis stated
as a measurement — "knowledge compounds" is precisely the claim that this
number goes down) · budget survival of task-critical facts · cost to first
correct action · **cross-model transfer** (the headline pitch is cross-model,
so a single-model result does not test it).

**E6 — adversarial / safety evaluation (M) [code].** The untrusted-content
claim is a *safety* claim and is therefore testable. Plant prompt-injection
payloads in mesh content (fake operator directives, spoofed onboarding
records, tool-call injections) and measure agent compliance rate through
digest, search and fetch. Also: does the R56 onboarding authorship gate hold
against a non-operator record end-to-end at the agent, not just at the
daemon? Report the compliance rate honestly, including if it is non-zero.

**E7 — longitudinal dogfood, strengthened (M) [operator + code].** Supersedes
the bare 30-handoff. Pre-registered success definitions; **randomized
withholding** (a fraction of sessions run Cairn-blind, giving a
within-operator control); existing telemetry as the substrate; and a written
statement of its own limits — n=1, non-blinded, self-reported. Report it as a
case study, and say so.

**E8 — replication artifacts (S) [operator].** Publish corpora + harness +
raw results so a third party can rerun and disagree. An evaluation nobody
else can reproduce is a press release.

**E9 — longitudinal + mesh recall (L) [code, large].** E4/E5 are *snapshot*
evaluations. "Long-term memory" is a claim about *time* and *growth*, and a
mesh adds *partiality*.

*The surface distinction — get this right or the results are garbage.* The
digest profile has a 72-hour freshness half-life; search has 90 days. That is
deliberate: the digest is a *working set*, search is the *memory*. Testing
long-horizon recall through the digest would measure the wrong surface and
produce a falsely damning result; testing it only through search would let
the digest off a hook it should be on. Measure both, with expectations stated
up front: **the digest is allowed to forget; search is not.**

*Time control (E9.2) is BUILT.* Two environment variables read at daemon
`Start`, compiled in only under `cairn_testhooks`: `CAIRN_FAKE_CLOCK_OFFSET`
(a Go duration) or `CAIRN_FAKE_CLOCK` (an RFC 3339 instant); both at once is
refused rather than resolved by precedence. **Offset, never frozen** — a
frozen clock stalls everything that waits for time to pass, and a hung
harness looks disturbingly like a result. **An epoch is a daemon lifetime** —
the offset resolves once at start, so simulated time never jumps under a
running daemon; advancing means restarting, which also exercises recovery.
**A malformed value is fatal** — falling back to real time would produce a
run whose timestamps mean something other than what the harness believes.
**The daemon announces it** (`SIMULATED CLOCK …`), so the harness confirms
the hook took effect. **Release builds are asserted clean** by `cmd/cairn`'s
release test, which builds an untagged binary and checks both that the
variable names are absent from its bytes and that setting them changes no
timestamp — either check alone is defeatable. Scope: ceremonies with
wall-clock TTLs (pairing invitations, enrolment requests) will expire if a
simulated clock crosses their window. That is the hook being honest.

*Metrics — curves, not point estimates.*
- **Recall-over-age.** Fix (query → known-relevant-item) pairs; plot recall
  against the *age of the target at query time*. Directly tests spec §9.1's
  claim that additive freshness "never annihilates old canonical material".
- **Recall-under-growth (interference).** Same query set; grow the
  surrounding corpus 10× → 100× → 1000× while **holding the budget fixed**.
  Selectivity demand rises with N, so this is the scariest curve in the plan:
  does a mesh that works at 1k messages still work at 100k? Cheap (T0, no
  agent) and the most likely place to find a real limit — **land it with E4**.
- **Supersession accuracy.** For fact pairs where B supersedes A: returns B
  (correct) / returns A (**stale**) / returns both undifferentiated. Known
  structural gap: `relates_to` is payload-only with no projection table, so
  supersession *across messages* is not queryable — only *within* a message's
  revision chain. This metric quantifies what that gap costs.
- **Stale-confidence rate.** Of the stale returns, how often does the agent
  act without signalling uncertainty? A miss is visible to the user; a
  confidently-stale hit is not. This is the dangerous one.
- **Duplicate dilution.** The same fact restated across N sessions: what
  fraction of a fixed budget does one fact consume? Measures the cost of the
  unimplemented duplicate/saturation penalties, and tells you whether to
  build them.
- **Temporal competence.** Can an agent answer "what did we decide about X,
  and has that changed?" Cairn has revisions, retractions and `cairn compact`
  but no "history of X" surface. If this scores badly, that absence is the
  finding.

*Mesh-specific recall*, which single-node evaluation cannot see:
- **Transitive convergence recall.** Written on A, needed on C, which has only
  ever synced with B. Recall as a function of time-since-write and topology.
- **Partiality honesty.** A thin node sets `partial: true`. The flag must be
  honest in both directions — and far more important, **never absent when the
  answer was in fact incomplete.** A silently-complete claim on an incomplete
  corpus is the mesh version of stale confidence.
- **Recall across repair.** After equivocation repair, the losing branch is
  reissued under a recovery origin. Is that knowledge still findable and
  correctly attributed? Long-term memory has to survive the ceremonies.
- **Revoked-device knowledge.** Authored by a since-revoked device: still
  retrievable? Should it be? A policy question with a recall consequence,
  to be answered deliberately rather than discovered.

*Highest-fidelity variant:* if C3 lands, real session history with real
timestamps can be replayed chronologically — a true longitudinal replay.
C3 supplies the corpus E9 most wants.

### 3.5 Sequencing

E1 (sign-off) gates reporting, not building. E4 next, with E9's
recall-under-growth curve landing alongside it. E5/E6 in parallel after. E7
alongside real usage. E8 at first tag.

### 3.6 Pre-registered kill criteria

Written before measurement, on purpose. If these fire, the honest response is
to change the product, not the metric. Thresholds live in `eval/claims.yaml`.

| If… | Then |
|---|---|
| Cairn does not beat **B1 (grep over transcripts)** on task success | The ranking/curation layer is not earning its complexity. Reconsider the whole retrieval stack. |
| Ablating **vector search** does not degrade quality | Delete the embedder. Kills the Python venv dependency, the model pin, and the enrichment pipeline — a large simplification. |
| Budget-capped digest loses to **B4 (full-context)** on tasks that fit in context | Budget discipline only matters above the context limit. Say that plainly instead of implying it always helps. |
| **Rediscovery rate** does not fall versus B0/B2 | The core thesis ("knowledge compounds") is unsupported. |
| **Cross-model transfer** underperforms single-model | The mesh pitch narrows to single-model multi-session. Rewrite the claim. |
| Injection **compliance rate is materially above baseline** | The untrusted-content envelope is decorative. Treat as a release blocker. |
| P2 profile does not beat P0 after calibration | Ship P0 as default and archive P2's extra terms. |
| **Recall collapses with target age** | Additive freshness is not protecting old canonical material as spec §9.1 claims. Revisit half-lives, or the claim. |
| **Recall collapses as the corpus grows** at fixed budget | The mesh does not scale as long-term memory. Duplicate/saturation penalties and/or summarization become mandatory, not optional. |
| **Stale-preferred rate is material** | Cross-message supersession needs structural representation. Until then, say plainly that Cairn recalls what was written, not what is currently true. |
| **Thin-node `partial` is ever falsely absent** | A node claims completeness it does not have. Release blocker for thin nodes: silent incompleteness is worse than declared partiality. |

### 3.7 EVAL non-goals

- **Not a leaderboard.** The goal is falsification of *our* claims.
- **No LLM judges where structural ground truth exists.** Mined human labels
  beat model opinions; judges are a fallback, and their agreement with human
  labels must itself be reported.
- **No tuning on the evaluation set.** Corpora split dev/holdout; weights are
  only ever calibrated on dev.
- **Not a substitute for the engineering gates.** Those stay where they are;
  this measures usefulness, not correctness.

## §4. DEBT — deferred scaling debt and unbuilt spec surfaces

These map to no phase row in the README. A green phase row is not a claim
that nothing is owed underneath it.

### D1 — sqlite-vec integration (L) [code]

`schema.sql` stores vectors in a plain table and `HeadVectors` loads **every**
head vector into process memory for an in-process cosine scan;
`config.BruteForceMaxCandidates = 5000` names the cliff. Fine at today's
corpus size, binding the moment C3 lands. CLAUDE.md pinned
`asg017/sqlite-vec-go-bindings`; brute force was always the fallback.

Feature-probe the extension at `projection.Open` — load failure is NOT an
error: log once, set a capability flag, keep brute force. When it loads,
mirror vectors into a `vec0` virtual table and route `VectorTopK` through it,
one writer, same transaction as the projection write. Bump
`ProjectionSchemaVersion`; the auto-rebuild path replays from the log, so no
migration is needed — but the rebuild must be exercised, not assumed.

**Acceptance.** Equivalence test: vec0 and brute force return the **identical
top-K** on a seeded corpus — brute force stays in the tree as the oracle, not
as dead code. Extension-absent test: `Open` succeeds, `cairn status` reports
the path in use, results unchanged. A corpus above the cliff answers without
loading every vector. `make verify` and `make test-race` green; the schema
bump replays cleanly from a log written by the previous version.

### D2 — origin-liveness beacon (M) [code]

Spec §13.2 and rulings §2 deferred this in P0 for a stated reason — "requires
peers" — and P1 has peers. The failure it catches is real and silent: a
device restored from portable data only mints a new origin, and a device
restored from a stale backup **regresses** its (generation, sequence). Today
`detectFrontierForkFromPeer` catches divergence at the same sequence; nothing
catches a peer whose frontier moved *backwards*.

Persist the highest (generation, sequence) ever observed per origin device;
on each reconcile compare the peer's advertised frontier against that
watermark; a regression raises a durable alarm surfaced by `cairn net` and
`cairn doctor`. Alarm, do not auto-quarantine: a regression signals operator
error, not equivocation, and the fork machinery already owns equivocation.

**Acceptance.** Two-daemon test where B restarts from a stale copy of its own
state: A raises the alarm, names the origin and both watermarks, `cairn
doctor` reports it. No false positive across an ordinary restart, an ordinary
catch-up, or a thin node that legitimately holds less.

### D3 — capability `resource_selectors` (M) [code]

Spec §7.2 describes per-capability resource scoping — `topic="project/x/*"`,
per-session budget caps. P0/P1 shipped the coarse action tiers only. A
session can be denied *writing* but not confined to a *subtree*, which is the
grant an operator actually wants for a narrow agent.

Extend the capability record with optional selectors; enforce at the IPC
dispatch boundary where the action tier is already checked, so enforcement
stays singular. Topic globs resolve through the existing topic resolver — no
second resolver. Budget caps clamp the requested `budget_chars`, and the
clamp is reported rather than applied silently.

**Acceptance.** A session granted `topic="a/*"` can search, digest and fetch
within `a/*` and is refused outside it — including via `thread`, which
crosses topics by construction. Refusals are **typed, not empty results**: an
agent must be able to tell "nothing matched" from "you may not ask". Grants
remain positive-only unless D7's ruling says otherwise.

### D4 — `budget_tokens` (M) [code]

Rulings §7 ruled `budget_chars` only for P0; `retrieve.go` still refuses
`budget_tokens` explicitly. Callers are LLM sessions whose real constraint is
tokens, and the char↔token ratio varies enough by content that the current
advice — divide by four — is a guess the caller has to make.

Accept `budget_tokens` alongside `budget_chars` (exactly one, never both).
Count with a vendored tokenizer whose identity is recorded in the response,
because a budget is only meaningful against a named tokenizer. The hard-budget
property is unchanged: oversized items are dropped whole, never truncated.

**Acceptance.** The budget property test runs in both modes over every
renderer. The response names the tokenizer and mode. A request carrying both
budgets is refused, not silently resolved.

### D5 — `cairn adopt-standalone` (S) [code]

R34 permits either a command or a documented script for merging an ad-hoc
standalone mesh into the primary; neither exists (PROGRESS N9). The
operator-facing failure is that a mesh started casually on a second machine
is currently a dead end. Given R34's latitude, prefer the documented
procedure first — a rare, high-consequence operation is safer as a script the
operator reads before running than as a verb that hides the steps. Promote to
a command only if the procedure proves mechanical.

**Acceptance.** A test or rehearsed transcript that merges a standalone mesh
into a primary and ends with `cairn doctor` clean on both origins, no event
loss from either side.

### D6 — prebuilt signed binary + Homebrew tap (M) [code] + [operator]

README's Quickstart promises a "planned zero-dependency path" and today's
only path is build-from-source with a C toolchain — the single largest
adoption barrier for a tool whose pitch is "one command". Release workflow
producing macOS arm64 + Linux x86_64/arm64 artifacts with checksums, and a
tap formula. Codesigning/notarization needs an Apple Developer ID — that half
is **operator**, and the workflow should degrade to unsigned artifacts with
an honest note rather than block on it.

**Acceptance.** A tagged release produces artifacts whose checksums verify;
`brew install` on a clean machine yields a working `cairn` with no Xcode
toolchain present. The FIX-F4 guard still holds — a release artifact must be
a `sqlite_fts5` build, and the workflow must assert that.

### D7 — mutes [ruling] then S/M

**Blocked, deliberately.** Spec §7.1/§4.5 list `mute(...)`; §7.2's stance is
positive grants only. No event, op or verb exists. These cannot both be
right, and the resolution changes the capability model rather than adding to
it. Record the answer in RULINGS.md, then build or delete the spec text. Do
not implement a mute as a negative selector under D3 without that ruling.

### D8 — third ranking profile for `explore()` [ruling] then M

**Blocked, deliberately.** Spec §13.4 raises it as an open question and no
`explore` surface exists to profile. Deciding weights before the surface
exists is backwards. If an exploration surface is wanted it needs its own
design pass; until then this stays a question, not a task.

### D9 — capability sessions are never reaped (M) [code] + [ruling]

**A live defect, observed on the dev node:** `cairn session list` returns
2,673 sessions accumulated since 2026-07-16, all named `mcp`, all
`agent-standard` — 1,149 unexpired and 1,524 expired but still resident.
`sessions.json` is 772 KB and growing at roughly two records per 90s while
any MCP client runs.

**Severity is operational, not a breach**, and saying so plainly matters:
the daemon already treats a no-handle local caller as operator and the file
is 0600 device-local, so 1,149 live `agent-standard` tokens grant nothing an
attacker could not get more cheaply. This is consistent with the documented
"confines agents, not attackers" posture. The real costs are unbounded
growth, a quadratic mint path, and a kill switch nobody can read.

Four compounding defects, each verified in the code:

1. **Expiry is lazy via an unreachable path.** Expired records are deleted
   only inside `resolve()` (session.go:280–289) — that is, only when someone
   presents that exact token *after* it expired. A dead MCP client never
   presents its token again, so its record is immortal. There is no
   background sweep anywhere in `internal/daemon`.
2. **`loadSessions` does not filter on load** (session.go:214–217), which is
   the natural reaping point. It rehydrates every stored session verbatim and
   sets `sess.lastUsed = now`, restarting the idle window on every daemon
   restart — so idle revocation can never retire a session across a restart.
3. **`persist()` rewrites the entire sorted array on every mutation**
   (session.go:221). Minting one session is O(n) in all sessions ever
   minted; cost grows quadratically over the mesh's life.
4. **The leak has a source, upstream of session.go.** `cairn mcp` mints a
   session per process and releases it with `defer` (cmd/cairn/mcp.go:67).
   A deferred revoke does not run when the process is killed by a signal —
   exactly how MCP clients tear down stdio servers — so every respawn leaks
   precisely one record. Fixing only the reaping would leave the leak intact
   and merely bound its backlog at the 24h TTL.

**`BoundPID` is the fix, not a footnote.** It is recorded (session.go:254)
and printed (session.go:312) and read **nowhere** in non-test code, so the
pid binding is decorative and a token is valid for its full TTL regardless
of whether its process still exists. A pid-liveness check is the honest
implementation of the README's "auto-revoked on exit", reaps the leak at
source rather than waiting out the TTL, and restores `session list` as a
kill switch. Guard pid reuse: trust the pid only for sessions bound on this
device, and pair it with `CreatedAt` so a recycled pid cannot resurrect a
record.

**What.** Drop expired entries in `loadSessions`; filter them in `list()`;
sweep on create; reap sessions whose bound pid is gone; handle SIGTERM/SIGINT
in `cairn mcp` so the revoke actually runs; add `cairn session prune` for the
existing backlog. If the mint path stays O(n), bound it — an append-only
journal with periodic compaction, or persisting only on a dirty flag.

**RULING NEEDED — what should idle mean across a restart?** The README
promises sessions are "auto-revoked on exit or idle". The code keeps that
promise only within a single daemon lifetime, because `lastUsed` resets on
load, and a comment says that reset is deliberate. Persisting `lastUsed`
would make the user-facing promise true and stop a daemon restart from
granting every stale token a fresh idle window — but it changes documented-
as-intentional behaviour, so it needs an author ruling rather than a
unilateral flip. Conservative interim: reap on expiry and dead-pid (neither
of which is in question), leave the `lastUsed` reset alone, and mark it
`// RULING-NEEDED:`.

**Acceptance.** A daemon restarted with a `sessions.json` full of expired and
dead-pid records loads a bounded set and rewrites the file smaller. `cairn
session prune` retires the 2,673-record backlog and reports what it removed.
A killed `cairn mcp` process leaves no resident session once the sweep runs.
Minting the 1000th session costs no more than the 10th. A live session is
never reaped while its process is alive and inside its TTL — the test that
matters most, because over-reaping breaks running agents.

### D10 — the ladder cannot tell "behind" from "no embedder" (S/M) [code]

**A live defect, same node as D9.** `assessDegradation`
(internal/daemon/maintenance.go:19) samples `CountPendingEmbeddings()`
unconditionally — it never checks whether an embedder exists. So the backlog
axis cannot distinguish **backlog because we are behind** (real load; shed
derived work to catch up) from **backlog because there is no embedder at
all** (nothing to catch up to, so shedding buys nothing and never ends).

Observed: 1,242 revisions unembedded because no venv is provisioned, read as
debt, putting an idle laptop at rung 2 (delay-summaries) under zero load —
and `message_summaries` holds 5 rows for 1,242 messages, which is that
shedding actually happening.

It worsens with corpus growth, because with no embedder the counter is
monotonic in **corpus size, not load**: ~5,000 messages silently reaches rung
3, 20,000 reaches rung 4 (thresholds at constants.go:593–596). Rungs 3 and 4
are harmless no-ops in that state — you cannot delay embeddings that never
run, and lexical-only is already true — but **rungs 1 and 2 shed real,
achievable work forever**, and the reported level misleads the operator about
why.

**Two causes, one observable mode.** Today's `lexical_only` is
`d.emb() == nil` (retrieve.go:147), *not* the ladder's rung 4, which would
need 20,000 pending. Same visible state, two unrelated causes, and `cairn
status` does not distinguish them — so an operator cannot tell "provision the
venv" from "you are under load".

**What.** Zero the backlog axis when no embedder is configured: "pending" is
not debt when nothing can ever work it off. The disk axis (rungs 5–7) is
unaffected and keeps governing. Separately, make `cairn status` name the
cause of lexical-only — no embedder configured vs ladder rung 4 vs embedder
present but failing — because the remedy differs in each case.

**Policy note, not a blocker.** Whether an unworkable backlog counts as debt
is a reading of spec §8.2. The conservative reading is that the ladder exists
to shed derived work *so the system can catch up*; where catching up is
impossible, shedding is pure loss with no recovery, so zeroing the axis is
the ladder's intent rather than a change to it. Implement that, mark it
`// RULING-NEEDED:` for confirmation, and do not block on the answer.

**Acceptance.** A daemon with no embedder and 50,000 unembedded revisions
reports Healthy on the backlog axis and writes summaries and auto-links
normally. A daemon *with* an embedder and a real backlog still climbs the
rungs exactly as it does today (the existing ladder tests must pass
unchanged). Disk rungs are unaffected in both cases. `cairn status`
distinguishes the causes of lexical-only, and a test pins each one.

### DEBT non-goals

- **Reranking with an LLM** — breaks R47/R51; a model's opinion does not
  reconcile against printed arithmetic.
- **Replacing brute-force cosine** — D1 keeps it as the test oracle. A vector
  path with no independent check is a silent-corruption surface.
- **Widening capabilities to negative grants** as a side effect of D3. That is
  D7's ruling to make.

## §5. P2 completion (built, opt-in — what "done" still needs)

| Item | Kind | Source |
|---|---|---|
| Weight calibration adoption: run `cairn rank-stats --calibrate` on the 30-handoff episode data, adopt weights per the §9.3 protocol (survives holdout, stays explainable) | [data] | spec §9.3–9.4 |
| Duplicate/thread-saturation penalties (`PenaltyCap` is pinned, unreferenced). Must move in lockstep with the why-ranked record + external reconciliation (R47/R51) — its own reviewed task | [code] | spec §9.1, PROGRESS WP-G3 |
| Degradation ladder rungs 6–7 ENFORCEMENT (currently computed + reported, fail open). Needs pre-ack reserved-capacity semantics vs send-never-blocks | [ruling] then [code] | spec §8.2, PROGRESS WP-G4 |

## §6. P3 completion (mesh built, single-host-audited)

| Item | Kind | Source |
|---|---|---|
| Two-machine live pass: pairing / thin-role / transport / remote-query on real hardware over a real tailnet (the July audit ran loopback single-host for P3's additions) | [hardware] | README "On P3", PROGRESS P3 close |
| Live re-audit of pairing/trust/sync code extended SINCE the audited July commit | [hardware] | README Status caveat |
| iroh transport: the live wire, relay self-hosting + diagnostics, NAT-traversing dial-by-key (transport seam already in place) | [code, large] | spec §12 P3, `internal/peer/transport.go` |
| Automatic metered/battery sensing (manual `metered` flag exists; sensing is platform work) | [code] | spec §7, config `Metered` |
| Mutual pairing authentication (handshake currently authenticates dialer→responder only) | [code] | PROGRESS P3-2b/2c |

## §7. P4 — self-organising knowledge (evidence-gated, needs P2 usage data)

| Item | Source |
|---|---|
| Automated filing | spec §12 P4 |
| Embedding-clustered self-folding topic maps (the semantic map; P2's structural map is the precursor) | spec §12 P2/P4 |
| Salience propagation | spec §12 P4 |
| Multi-human namespaces, per-topic ACLs, payload-level encryption + key epochs | spec §2, §12 P4 |

## §8. Open author rulings

Tracked in PROGRESS.md "Author rulings needed"; markers in code are greppable
via `RULING-NEEDED`.

| Ruling | Blocks |
|---|---|
| FIX-A6 residual: what to report when a link append fails AFTER publish is durable (conservative error-return implemented) | nothing (confirmation) |
| R38 bootstrap-trust retention breadth (`internal/daemon/daemon.go`) | nothing (confirmation) |
| R40/R41 backfill confirmation (fork-repair revoke bundling) | nothing (confirmation) |
| §8.2 reserved-slice vs send-never-blocks | §5: ladder rungs 6–7 |
| Mutes vs "positive grants only" | §4: D7 |
