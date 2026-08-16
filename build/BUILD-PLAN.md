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
it, as sprints, plus the honest reason each blocked item is blocked.
The sprints are exhaustive: **S1–S14 cover every item in Part II**, so
finishing them is finishing the backlog. **S8 needs nothing from anyone**, so
an agent can start there and run without waiting. (S1, defect clearance — D9
session reaping and D10 ladder-vs-missing-embedder; S2, mesh integrity and
scoped capabilities — D2 origin-liveness beacon and D3 capability
`resource_selectors`; S3, small surfaces — D4 `budget_tokens` and D5
standalone-mesh adoption; S5, scale — D1 sqlite-vec; S4, the dark evaluation
apparatus; and S15, retrieval correctness — D11 disjunctive lexical matching —
all shipped on 2026-08-16; see PROGRESS.md.)

## Sprints

Sprints are **scope-boxed, not time-boxed**: a sprint is done when its exit
criterion holds, and a sprint contains only work that is unblocked at the
time it starts. That is deliberate — most of what is left has a dependency on
the operator, on hardware, or on a ruling, and a time-boxed sprint containing
blocked work just stalls and lies about why.

Run them in order. Each ships as its own commit(s) with PROGRESS.md updated,
and `make verify` + `make test-race` green before moving on.

**The sprint set is exhaustive.** Every item in Part II belongs to exactly
one sprint — see Coverage at the end of this part — so finishing S1–S14 is
finishing the backlog, with no separate track running alongside. S8 needs
nothing from anyone; the later sprints name their gate in the heading.

### S6 — Capture [gated: crossed review of the privacy model]

- **C3** session-transcript ingest (§2)

**Exit:** the §2 acceptance criteria, of which the load-bearing one is that a
seeded fake API key never reaches the object store.

### S16 — CI truth and release identity [ready — RUN THIS NEXT]

Both found by reading CI rather than a local terminal, after five sprints
reported "verify green" on Linux while macOS — the **primary** platform — had
been failing for hours.

- **D12** macOS `make verify` fails on the unix-socket path limit (§4)
- **D13** a release binary cannot name its own version (§4)

**Exit:** CI is green on macOS and ubuntu; a built artifact reports the tag it
was released under.

**Watch:** D12 is the more important of the two, and not only for the fix — a
green Linux run has been standing in for a green build all day. Whatever the
repair, the sprint is not done until the macOS job actually passes on a
runner, observed, not inferred.

### S8 — Ranking completeness [ready]

- **P2 duplicate/thread-saturation penalties** (§5) — `PenaltyCap` is pinned
  and unreferenced; spec §9.1 describes the penalties nothing applies

**Exit:** penalties apply under the P2 profile and appear as why-ranked
components, with R47/R51 external recomputation reconciling **exactly** —
that lockstep is the whole difficulty, not the penalty arithmetic.

**Watch:** this is the item C3 drags forward. If S6 lands first, S8 stops
being optional — transcripts are highly repetitive and a scoped search
without penalties drowns in near-identical chunks.

### S9 — iroh transport [gated: an author ruling on the binding]

Mutual pairing authentication and metered/battery sensing **shipped**
(2026-08-16); only the wire itself is left, and it is now blocked on a
dependency decision rather than on effort.

- **iroh transport** (§6) — the live wire, relay self-hosting + diagnostics,
  NAT-traversing dial-by-key

**The ruling needed.** `n0-computer/iroh-ffi` ships no Go bindings.
`github.com/tmc/go-iroh` is a pure-Go clean-room port that maps onto the
existing transport seam almost verbatim and was demonstrated working
(two endpoints, round trip by public key) — but it is v0.0.0, untagged,
single-author, unaffiliated with n0, days old, vendors a quic-go fork and a
patched `crypto/tls`, and raises Cairn's Go floor to 1.26 (an R52 decision).
The alternatives are cgo against `iroh-c-ffi`, or uniffi-bindgen-go against
`iroh-ffi` — both add a Rust toolchain and a per-platform static library to
S7's packaging. Marker in `internal/peer/transport.go`; options in PROGRESS.

**Exit:** two nodes pair and reconcile over iroh with no Tailscale
dependency, on a binding the author has sanctioned.

### S10 — Hardware validation [gated: two physical machines]

- **P3 two-machine live pass** (§6) — pairing / thin-role / transport /
  remote-query on real hardware over a real tailnet
- **Live re-audit** of pairing/trust/sync code extended since the audited
  July commit (§6)

**Exit:** the P3 mesh path is exercised between two machines, not loopback,
and the audit certifies the current commit rather than a July one.

**Why it cannot be simulated:** the July audit ran single-host, which is
exactly the caveat the README states. Loopback cannot falsify NAT traversal,
clock skew between hosts, or a partition.

### S11 — Measurement [gated: operator sign-off, then corpora]

The point of the whole EVAL section. Nothing here may report a number before
its kill criterion is signed.

- **E1 sign-off** on the 21 kill criteria (§3.4) — [operator]
- **E3 corpus acquisition** (§3.4) — [operator]
- **Embed venv provisioning** (§1) — so measurement runs semantic, not
  lexical-only; a lexical-only result would measure a different product
- **E4 + E9 run for real** — the apparatus built dark in S4, now reporting
- **E5** extrinsic agent-in-the-loop battery (§3.4) — **the thesis**; task
  success, rediscovery rate, budget survival, cross-model transfer
- **E7** longitudinal dogfood with pre-registration and randomized
  withholding (§3.4), which subsumes the bare 30-handoff evaluation in §1
- **E8** replication artifacts (§3.4)
- **P2 weight calibration adoption** (§5) — needs E7's episode data; the
  §9.3 protocol requires surviving holdout

**Exit:** every claim in `eval/claims.yaml` has a measured result or a stated
reason it could not be measured, and any kill criterion that fired has been
acted on — by changing the product or the claim, not the metric.

**Watch:** this is the sprint that can go badly, by design. If E5 shows Cairn
losing to B1 (grep over transcripts), that is a real result and the plan says
what to do about it. Budget real money and real time.

### S12 — Ruling-gated surfaces [gated: author rulings]

- **D7 mutes** (§4) — spec §7.1/§4.5 versus §7.2's positive-grants-only stance
- **D8 explore ranking profile** (§4) — needs an exploration surface first
- **P2 ladder rungs 6–7 enforcement** (§5) — §8.2 reserved-slice versus
  send-never-blocks

**Exit:** each of the three is either built to a recorded ruling or deleted
from the spec, with the ruling written into RULINGS.md. "Still open" is not
an exit — an unanswered question that blocks work is itself the deliverable.

### S13 — Release [gated: S1–S12 green]

- **Overnight 1M-event synthetic scorecard** (§1) — includes the
  reindex/backup/restore/RSS numbers never recorded
- **C4 provider-directory listings** (§2) — [operator]
- Cut the first tag

**Exit:** `cairn gates` passes on a real corpus, the scorecard is recorded in
PROGRESS, and a tagged release exists whose artifacts verify.

### S14 — P4: self-organising knowledge [gated: P2 usage data]

- Automated filing · embedding-clustered self-folding topic maps · salience
  propagation · multi-human namespaces, per-topic ACLs, payload-level
  encryption + key epochs (§7)

**Exit:** per-item, once S11 has produced the usage and salience data that
makes any of it more than guesswork.

**Watch:** deliberately last and deliberately vague. P4 is evidence-gated;
specifying it in detail before S11 reports would be inventing requirements.

### Coverage

**Every item in Part II belongs to exactly one sprint.** That is the property
that makes "finish the sprints" equal "finish the backlog", and it is worth
checking when adding work: if a new item does not fit a sprint, it needs one,
or the sprint set is wrong.

| Sprint | Part II items | Owner |
|---|---|---|
| S1 ✅ shipped | D9, D10 | agent |
| S2 ✅ shipped | D2, D3 | agent |
| S3 ✅ shipped | D4, D5 | agent |
| S4 ✅ shipped | E4 + E9 growth curve (apparatus), E6 | agent |
| S15 ✅ shipped | D11 | agent |
| S5 ✅ shipped | D1 | agent |
| S6 | C3 | agent, after review |
| S7 ✅ shipped | D6 | agent + operator (signing) |
| S16 | D12, D13 | agent |
| S8 | P2 penalties | agent |
| S9 (partial) | iroh — **blocked on a ruling**; metered sensing and mutual pairing auth ✅ shipped | author, then agent |
| S10 | P3 two-machine pass, live re-audit | operator + hardware |
| S11 | E1, E3, venv, E4/E9 reported, E5, E7, E8, P2 calibration | operator + agent |
| S12 | D7, D8, ladder rungs 6–7 | author, then agent |
| S13 | 1M scorecard, C4 listings, the tag | operator |
| S14 | P4 (4 items) | agent, evidence-gated |

The five open rulings in §8 are not sprint items: three are confirmations
that block nothing, and two (mutes, §8.2 reserved-slice) are S12's input.

**What "all sprints done" means:** every claim measured or explicitly
unmeasurable, the mesh exercised on real hardware, a tagged release, and P4
started on evidence rather than intuition. There is no hidden remainder.

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

**Drags forward:** the duplicate/saturation penalties in §5 (transcripts are
highly repetitive; without penalties a scoped search drowns in near-identical
chunks). The brute-force cliff transcript volume would have walked off is
already gone — S5 shipped the sqlite-vec index.

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

### D12 — macOS `make verify` fails on the unix-socket path limit (S) [code]

CI on this branch has been red on `verify (macos-latest)` since at least D1.
Every `internal/` package passes; `cmd/cairn` fails, deterministically,
macOS-only:

```
--- FAIL: TestD5AdoptStandaloneScript
    error: listen unix /var/folders/df/djsxfhc17x95674wsm_g8s980000gn/T/
           cd53430871316/cairn/01a00b15-…-….sock: bind: invalid argument
```

macOS caps `sun_path` at ~104 bytes. Its `TMPDIR` alone is ~50 characters of
`/var/folders/…`; add the test's own temp dir, the per-user socket directory,
a 36-character UUID and `.sock`, and `bind` fails with `invalid argument`.
Linux allows 108 bytes and has a short `/tmp`, so the same suite is green
here. The socket-directory design (FIX-A7) explicitly claimed to respect
"the ~104-byte path cap the code documents" — so either that reasoning was
wrong or this path defeats it. Establish which before changing anything.

**Why it matters beyond the fix.** Five sprints in a row reported "make
verify green" truthfully, on Linux, while the primary platform was broken.
The lesson is procedural: a local green is not a green build.

**Acceptance.** `verify (macos-latest)` passes on a hosted runner — observed,
not inferred. A test asserts the socket path stays inside the platform limit
given a realistically long `TMPDIR`, so this cannot regress silently on a
platform nobody develops on.

### D13 — a release binary cannot name its own version (S) [code]

`cairn --version` prints `p1-<commit>`, computed from build info and not
settable via `-ldflags -X`, so a tagged release artifact cannot report the
tag it was released under. S7's release notes state the discrepancy rather
than hiding it, but a binary that cannot identify its own release is a
support problem the moment anyone other than the author runs one.

**Acceptance.** A binary built by the release workflow reports its tag;
a development build still reports something honest (commit + dirty state)
rather than claiming a tag it does not have.

### DEBT non-goals

- **Reranking with an LLM** — breaks R47/R51; a model's opinion does not
  reconcile against printed arithmetic.
- **Replacing brute-force cosine** — it is the sqlite-vec path's test oracle
  and its fallback, and it stays. A vector path with no independent check is
  a silent-corruption surface.
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
| D9 residual: should session `lastUsed` persist across a daemon restart (README promises idle revocation; the reset is documented as deliberate)? Conservative interim shipped — reap on expiry + dead pid only | nothing (confirmation) |
| D10 residual: does an unworkable embedding backlog count as §8.2 debt? Conservative reading shipped — the axis is zeroed with no embedder | nothing (confirmation) |
| Mutes vs "positive grants only" | §4: D7 |
| iroh Go binding: adopt `tmc/go-iroh` (v0.0.0, days old, vendored TLS/QUIC, Go floor 1.26 per R52) vs cgo against `iroh-c-ffi` vs wait | S9: the iroh wire |
