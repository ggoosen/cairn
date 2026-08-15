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

## 6. Sequencing

E1 → E2 → E3 → E4 (T0/T1 value lands here) → E5/E6 in parallel → E7
alongside real usage → E8 at first tag. E1 is cheap and gates everything:
write the kill criteria before you can be tempted by results.

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
