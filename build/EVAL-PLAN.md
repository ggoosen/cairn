# EVAL — proving the thesis (work order, planned 2026-08-09)

Cairn makes strong claims. Almost none of them are currently *evidence*.
This work order builds the mechanism that could falsify them.

The distinction that drives everything below: Cairn measures whether
**retrieval returns the right document** (intrinsic). It has never measured
whether **an agent does better work** (extrinsic). The product thesis —
"knowledge compounds instead of being re-explained" — is an extrinsic
claim, so no amount of intrinsic measurement can establish it.

---

## 1. The claims, and what actually backs them today

| Claim | Current evidence | Honest status |
|---|---|---|
| Zero acknowledged-event loss | Crash matrix in CI, deep doctor, fault injection | **Proven** (for the modelled fault set) |
| 100% budget compliance | Property test over budget sweeps; gate | **Proven** |
| 100% provenance on fetched | Automated gate | **Proven** |
| Send→visible P95 < 200 ms | Measured, 1.5 ms @ 100k | **Proven** (one machine) |
| Ranking is explainable | R47/R51 external-recompute reconciliation | **Proven** |
| Retrieval finds the right thing (Success@5 ≥ 70%) | Golden corpus — **synthetic, authored by the project author, queries and relevance judgments by the same person** | **Circular.** Validates configuration, not capability (the spec says so itself) |
| Untrusted-content envelope protects agents | Structural — content is wrapped and quoted | **Assumed, never tested.** No adversarial input has ever been run at an agent through it |
| Budget-capped digest preserves what matters | Budget compliance is proven; *usefulness under the cap* is not | **Untested** |
| Cross-model knowledge transfer works | — | **Untested** (and it is the headline pitch) |
| Knowledge compounds / beats copy-paste | 30-handoff evaluation, not yet run | **Unstarted**, and as designed: n=1 operator, self-reported, no control |
| Beats the alternatives (flat files, DB, transcript search) | — | **Untested.** The central competitive claim has never been measured against a single baseline |

## 2. The three methodological problems

**2.1 No control condition.** Everything measured is intrinsic. To support
"materially improves agent memory" you need the same tasks run *with* and
*without* Cairn, and against the alternatives people actually use. Without a
control there is no counterfactual, and without a counterfactual there is no
claim — only a description.

**2.2 Self-authorship at every layer.** The corpus, the queries, the
relevance judgments, the ranking weights, the operator, and the person
interpreting the results are all the same judgment. That is not a criticism
of the work; it is a structural property that makes the numbers unusable as
evidence for anyone else. Breaking this circularity is the single highest-
value change in this plan.

**2.3 No falsification criteria.** Nothing currently written down could come
back false. An evaluation that cannot fail is marketing. §7 fixes this by
pre-registering the kill criteria BEFORE any measurement runs.

## 3. Architecture: same repo, separate module, black box only

`eval/` becomes its own Go module (`eval/go.mod`), not a package of the main
one. Three consequences, all deliberate:

1. **Black-box access is compiler-enforced, not conventional.** A separate
   module physically cannot import `github.com/ggoosen/cairn/internal/...`
   — Go's `internal/` visibility rule does the enforcement. The harness can
   only reach Cairn the way a real agent does: the CLI and MCP. It therefore
   cannot accidentally measure implementation details, and cannot be tuned
   against internals.
2. **Dependency isolation.** The harness needs LLM API clients and
   statistics libraries. The daemon's dependency tree is deliberately small
   and offline; those deps must never enter it.
3. **Property isolation.** The main suite is offline, deterministic, free,
   and gates every commit. Agent-in-the-loop evaluation is none of those —
   it is networked, stochastic, and costs money. Mixing them would destroy
   the properties that make the main suite trustworthy.

It stays in-repo (not a separate repository) so it cannot rot out of sync
with the surface it tests.

## 4. Tiers, by cost and cadence

| Tier | What | Cost | Cadence |
|---|---|---|---|
| **T0** | Offline, deterministic: ablations + baselines over static corpora, no LLM | free | every commit (CI) |
| **T1** | Intrinsic retrieval quality over INDEPENDENT corpora; LLM only as a relevance judge where ground truth is thin | cents | nightly / pre-release |
| **T2** | Extrinsic agent-in-the-loop task battery, multi-model, multi-trial | real money | per release, pre-registered |

The existing golden corpus stays, demoted to what it honestly is: a **T0
regression gate** that catches configuration drift. It is relabelled in
place so nobody mistakes it for evidence again.

## 5. Milestones

### E1 — Claims register + pre-registration (docs, S) — DO FIRST
Machine-readable `eval/claims.yaml`: every public claim (README, spec,
CLAUDE.md agent instructions), its measurement, its threshold, and its
**kill criterion**. Nothing is measured until its criterion is written
down. This file is the contract; §7 is its summary.

### E2 — Harness skeleton (M)
`eval/` module; a driver that provisions a throwaway cairn, drives it as a
black box (CLI + MCP), and records structured results. Must support
swapping the **memory backend** so every baseline in E4 runs through one
task interface. Deterministic seeds and captured transcripts, so any result
can be re-inspected after the fact.

### E3 — Independent corpora (M) — breaks the circularity
Corpora the project author did not write, with relevance judgments the
project author did not make. The cheap trick that makes this tractable:
**mine free human-authored ground truth** —
- GitHub duplicate-issue links (a maintainer marking #B a duplicate of #A
  *is* a relevance judgment, made by someone with no stake in Cairn),
- Stack Overflow duplicate markers,
- documentation cross-references and "see also" links,
- real multi-agent session transcripts (what CAPTURE C3 would produce),
  anonymized and held out.

Corpora are versioned and checksummed so results are reproducible.

### E4 — Intrinsic quality: ablations + baselines (M)
Proper IR metrics (nDCG@k, MRR, Recall@k) — not just binary Success@5 —
over E3 corpora.

**Ablations** (does each component earn its complexity?): lexical-only ·
vector-only · RRF fusion · ±freshness · ±priority decay · ±mandatory
inclusion · P0 vs P2 profile. A component whose removal doesn't hurt should
be deleted; that is a *result*, not a failure.

**Baselines** (the competitive claim, finally tested):
- B0 no memory (cold agent)
- B1 **grep over raw transcripts** — the zero-effort baseline; this is
  roughly what shipping harnesses do today, and it is the one to beat
- B2 flat append-only markdown notes (what most people actually do)
- B3 naive vector-DB RAG (the default reach)
- B4 full-context stuffing, where the corpus fits (tests whether budget
  discipline matters at all below the context limit)
- B5 Cairn

### E5 — Extrinsic task battery (L) — THE THESIS
Handoff tasks where session B needs something session A learned, with
**objectively checkable** success (tests pass, the right constraint is
respected, the correct prior decision is cited). Run every condition from
E4, multiple trials (agents are stochastic), paired where possible, with
effect sizes and confidence intervals — not point estimates.

Primary metrics:
- **Task success rate** — the headline.
- **Rediscovery rate** — how often the agent re-derives something the mesh
  already contained. This is the thesis stated as a measurement: "knowledge
  compounds instead of being re-explained" is precisely the claim that this
  number goes down.
- **Budget survival of task-critical facts** — of the facts needed to
  succeed, what fraction survived the digest cut? Tests *usefulness* under
  the cap, which budget-compliance testing cannot see.
- **Cost to first correct action** — tokens and wall-clock.
- **Cross-model transfer** — knowledge written by model A, used by model B.
  The headline pitch is cross-model, so a single-model result does not test
  it.

### E6 — Adversarial / safety evaluation (M)
The untrusted-content claim is a *safety* claim and is therefore testable.
Plant prompt-injection payloads in mesh content ("ignore previous
instructions", fake operator directives, spoofed onboarding records,
tool-call injections) and measure agent compliance rate through the digest,
search and fetch surfaces. Also: does the R56 onboarding authorship gate
hold against a non-operator record end-to-end at the agent, not just at the
daemon? Report the compliance rate honestly, including if it is non-zero.

### E7 — Longitudinal dogfood, strengthened (M) — supersedes the bare 30-handoff
Keep the 30-handoff protocol but make it evidence rather than anecdote:
pre-registered success definitions; **randomized withholding** (a fraction
of sessions run Cairn-blind, giving a within-operator control); the existing
telemetry (interaction_id, impressions with positions, outcome→message
binding, final_rank) as the substrate; and a written statement of its own
limits — n=1 operator, non-blinded, self-reported. Report it as a case
study, and say so.

### E8 — Replication artifacts (S)
Publish corpora + harness + raw results so a third party can rerun and
disagree. An evaluation nobody else can reproduce is a press release.

### E9 — Longitudinal + mesh recall (L) — the long-term memory question

E4/E5 are **snapshot** evaluations: fixed corpus, run queries, measure. But
"long-term memory" is a claim about *time* and *growth*, and a mesh adds
*partiality*. Four failure modes live here that no snapshot can see.

**9.1 The surface distinction — get this right or the results are garbage.**
The digest profile has a 72-hour freshness half-life; search has 90 days.
That is deliberate: the digest is a *working set* ("what's new that I care
about"), search is the *memory* ("find the right thing ever written").
Testing long-horizon recall through the digest would measure the wrong
surface and produce a falsely damning result; testing it only through
search would let the digest off a hook it should be on. Measure both, with
different expectations stated up front: **the digest is allowed to forget;
search is not.**

**9.2 Prerequisite: time control.** Simulating a year of mesh must not take
a year. The daemon's clock is injectable (`Options.Now`) but only through
the Go API, which the black-box harness cannot reach by design. The
sanctioned fix already has precedent in this repo: a clock hook compiled in
**only under the `cairn_testhooks` build tag** (the same mechanism that
keeps the volume-status hook out of release builds). Eval builds get
controllable time; release builds cannot have it. Alternative if that
proves unwise: run the daemon under a controlled system clock in a
container — fully black-box, at the cost of harness complexity.

*Built (2026-08-15). The mechanism, where it is more specific than the
sketch above:*

- Two environment variables, read by the daemon at `Start` and compiled in
  only under `cairn_testhooks`: `CAIRN_FAKE_CLOCK_OFFSET` (a Go duration,
  e.g. `-2160h`) or `CAIRN_FAKE_CLOCK` (an RFC 3339 instant). Both at once
  is refused rather than resolved by precedence.
- **Offset, never frozen.** The clock advances at the real rate; only its
  origin moves. A frozen clock stalls everything that waits for time to pass
  — TTLs, leases, debounces, housekeeping — and a hung harness looks
  disturbingly like a result.
- **An epoch is a daemon lifetime.** The offset is resolved once at start, so
  simulated time never jumps under a running daemon; the harness restarts the
  daemon to advance to the next epoch (which also exercises recovery between
  epochs, as a long-horizon experiment should).
- **A malformed value is fatal.** Falling back to real time would produce a
  run whose timestamps mean something other than what the harness believes,
  and it would look entirely normal.
- **The daemon announces it** on its warn stream (`SIMULATED CLOCK …`), so
  the harness confirms the hook took effect instead of assuming the variable
  was honoured.
- **Release builds are asserted clean** by `cmd/cairn`'s release test, which
  builds an untagged (`sqlite_fts5` only) binary and checks both that the
  variable NAMES are absent from its bytes and that setting them changes no
  timestamp. Either check alone is defeatable — a rename beats the first, a
  malformed value passes the second — so both run.
- Scope: `cairn init` runs the identity ceremonies on the real clock. That is
  safe (`wall_time` is never used for ordering; device certificates carry no
  validity window), but ceremonies that DO have wall-clock TTLs — pairing
  invitations, enrolment requests — will expire if a simulated clock crosses
  their window. That is the hook being honest, not a defect to work around.

**9.3 Metrics — curves, not point estimates.**

- **Recall-over-age.** Fix (query → known-relevant-item) pairs; plot recall
  against the *age of the target at query time*. This directly tests spec
  §9.1's design claim that additive freshness "never annihilates old
  canonical material". If recall collapses past some age, the claim is
  false and the half-lives need revisiting.
- **Recall-under-growth (interference).** Same query set; grow the
  surrounding corpus 10× → 100× → 1000× while **holding the budget fixed**
  (as it is in real use). Selectivity demand rises with N, so this is the
  scariest curve in the whole plan: does a mesh that works at 1k messages
  still work at 100k? Cheap to run (T0/T1, no agent needed) and the most
  likely place to find a real limit.
- **Supersession accuracy.** For fact pairs where B supersedes A, report a
  three-way split: returns B (correct) / returns A (**stale**) / returns
  both undifferentiated (ambiguous — the agent may pick wrong). Note the
  known structural gap: `relates_to` is payload-only with no projection
  table, so supersession *across messages* is not queryable — only
  supersession *within* a message's revision chain is. This metric will
  quantify how much that gap costs.
- **Stale-confidence rate.** Of the stale returns above, how often does the
  agent act on them without signalling uncertainty? A miss is visible to
  the user; a confidently-stale hit is not. This is the dangerous one.
- **Duplicate dilution.** The same fact restated across N sessions: what
  fraction of a fixed budget does one fact consume? Directly measures the
  cost of the unimplemented duplicate/saturation penalties (spec §9.1,
  `PenaltyCap`), and tells you whether to build them.
- **Temporal competence.** Can an agent answer "what did we decide about X,
  and has that changed?" — history, not just current state. Cairn has
  revisions, retractions and `cairn compact` (current state), but no
  "history of X" retrieval surface. If this scores badly, that absence is
  the finding.

**9.4 Mesh-specific recall.** Single-node evaluation cannot see these:

- **Transitive convergence recall.** Knowledge written on node A, needed on
  node C, which has only ever synced with B. Measure recall as a function
  of time-since-write and sync topology.
- **Partiality honesty.** A thin node holds only a recent window and sets
  `partial: true`. The claim to test is that the flag is *honest in both
  directions*: partial when it genuinely is, and — far more important —
  never absent when the answer was in fact incomplete. A silently-complete
  claim on an incomplete corpus is the mesh version of stale confidence.
- **Recall across repair.** After an equivocation repair, the losing
  branch's messages are reissued under a recovery origin carrying
  `recovered_from_event_id`. Is that knowledge still findable afterwards,
  and correctly attributed? Long-term memory has to survive the ceremonies.
- **Revoked-device knowledge.** Knowledge authored by a since-revoked
  device: still retrievable? Should it be? That is a policy question with a
  recall consequence, and it should be answered deliberately rather than
  discovered.

**9.5 Highest-fidelity variant.** If CAPTURE C3 lands, real session history
with real timestamps can be replayed chronologically, querying at each
point — a true longitudinal replay rather than a synthetic one. That makes
E9 and C3 mutually reinforcing: C3 supplies the corpus E9 most wants.

## 6. Sequencing

E1 → E2 → E3 → E4 (T0/T1 value lands here) → E5/E6 in parallel → E7
alongside real usage → E8 at first tag. E1 is cheap and gates everything:
write the kill criteria before you can be tempted by results.

E9 splits: its **recall-under-growth** curve is T0 (synthetic corpus, no
agent, no LLM cost) and should land WITH E4 — it is the cheapest experiment
in the plan and the most likely to find a real limit. The rest of E9 needs
the time-control hook (§9.2) and, for the highest-fidelity variant, CAPTURE
C3.

## 7. Pre-registered kill criteria

Written before measurement, on purpose. If these fire, the honest response
is to change the product, not the metric.

| If… | Then |
|---|---|
| Cairn does not beat **B1 (grep over transcripts)** on task success | The ranking/curation layer is not earning its complexity. Reconsider the whole retrieval stack. |
| Ablating **vector search** does not degrade quality | Delete the embedder. Kills the Python venv dependency, the model pin, and the enrichment pipeline — a large simplification. |
| Budget-capped digest loses to **B4 (full-context)** on tasks that fit in context | Budget discipline only matters above the context limit. Say that plainly instead of implying it always helps. |
| **Rediscovery rate** does not fall versus B0/B2 | The core thesis ("knowledge compounds") is unsupported. |
| **Cross-model transfer** underperforms single-model | The mesh pitch narrows to single-model multi-session. Rewrite the claim. |
| Injection **compliance rate is materially above baseline** | The untrusted-content envelope is decorative. Treat as a release blocker. |
| P2 profile does not beat P0 after calibration | Ship P0 as default and archive P2's extra terms. |
| **Recall collapses with target age** (E9) | Additive freshness is not protecting old canonical material as spec §9.1 claims. Revisit half-lives, or the claim. |
| **Recall collapses as the corpus grows** at fixed budget (E9) | The mesh does not scale as long-term memory. Duplicate/saturation penalties and/or summarization become mandatory, not optional. |
| **Stale-preferred rate is material** (E9) | Cross-message supersession needs structural representation (`relates_to` is payload-only today). Until then, say plainly that Cairn recalls what was written, not what is currently true. |
| **Thin-node `partial` is ever falsely absent** (E9) | A node claims completeness it does not have. Release blocker for thin nodes: silent incompleteness is worse than declared partiality. |

## 8. Non-goals

- **Not a leaderboard.** The goal is falsification of *our* claims, not
  ranking against other products.
- **No LLM judges where structural ground truth exists.** Mined human
  labels (E3) beat model opinions; judges are a fallback, and their
  agreement with human labels must itself be reported.
- **No tuning on the evaluation set.** Corpora split into dev/holdout;
  weights are only ever calibrated on dev (the §9.3 protocol already
  requires holding out entire tasks — this extends the same discipline).
- **Not a substitute for the engineering gates.** Those stay where they
  are; this measures usefulness, not correctness.
