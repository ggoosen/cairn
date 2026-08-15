# ROADMAP — everything still to be built, in one place

This is the consolidated index of ALL outstanding work, gathered from the
README roadmap, `build/CAPTURE-PLAN.md`, PROGRESS.md's deferred/owed
items, RULINGS.md, and the spec's own phase plan (§12) and open questions
(§13). Each item links to its authoritative source; when an item ships,
it leaves this file and gets its PROGRESS.md section.

Legend: **[code]** buildable now · **[ruling]** needs an author ruling
first · **[operator]** human activity, not code · **[hardware]** needs
the two-machine rig · **[data]** needs real usage data first.

Not indexed here because it is finished: `build/BUILD-PLAN.md` (M0–M8) is
the COMPLETED P0 plan, kept for its acceptance criteria and decision trail.
Everything still outstanding — from any phase — is below.

---

## 1. Release blockers (before the first tag)

| Item | Kind | Source |
|---|---|---|
| **30-handoff product evaluation** — the "does it beat copy-paste?" gate. Success@5 / workaround-rate are now computed by `cairn gates` from recorded outcomes; the handoffs themselves are the missing input. **As designed this is n=1, self-reported and uncontrolled — see EVAL E7, which strengthens it with pre-registration and randomized withholding rather than replacing it** | [operator] | [DOGFOOD.md §4](DOGFOOD.md), spec §11, [build/EVAL-PLAN.md](build/EVAL-PLAN.md) |
| Embed venv provisioning on each node (`scripts/cairn-embed-bootstrap.sh`) so the evaluation runs semantic, not lexical-only | [operator] | [DOGFOOD.md §2](DOGFOOD.md) |
| Overnight 1M-event synthetic scorecard (`CAIRN_SCORECARD=1`, TESTING.md §5 — includes reindex/backup/restore/RSS numbers never recorded) | [operator] | [build/TESTING.md](build/TESTING.md), PROGRESS M7 |

## 2. CAPTURE — zero-effort capture (planned work order)

Full milestones + acceptance criteria: **[build/CAPTURE-PLAN.md](build/CAPTURE-PLAN.md)**

| Item | Kind |
|---|---|
| C3 session-transcript ingest — opt-in, redacted, eager-searchable/ephemeral, pull-only; privacy design gets a crossed review before code | [code] |
| C4 residual: submit provider-directory listings (awesome-hermes-agent, Hermes Atlas) now that [docs/memory-provider.md](docs/memory-provider.md) exists | [operator] |

## 2b. EVAL — proving the thesis (planned work order)

Full milestones + pre-registered kill criteria: **[build/EVAL-PLAN.md](build/EVAL-PLAN.md)**

The claims are currently claims. Cairn measures whether retrieval returns
the right document (intrinsic); it has never measured whether an agent does
better work (extrinsic) — and the product thesis is extrinsic. The existing
golden corpus is author-written, author-queried and author-judged, so it
validates configuration, not capability.

| Item | Kind |
|---|---|
| E1 claims register — **drafted at [eval/claims.yaml](eval/claims.yaml)**; residual: operator sign-off on 21 kill criteria (gates MEASUREMENT, not apparatus) | [operator] |
| E3 residual: **acquire the corpora** — the format, normalizers, loader and sample are built; downloading duplicate-issue / SO-duplicate / doc-crossref data is an operator step with commands in [eval/corpora/ACQUISITION.md](eval/corpora/ACQUISITION.md). Gates E4/E5/E9 | [operator] |
| E4 intrinsic quality: nDCG/MRR/Recall, component ablations, baselines incl. grep-over-transcripts | [code] |
| E5 extrinsic agent-in-the-loop task battery — task success, **rediscovery rate**, budget survival, cross-model transfer | [code, large] |
| E6 adversarial/safety eval: prompt-injection compliance rate through digest/search/fetch | [code] |
| E7 longitudinal dogfood strengthened: pre-registration + randomized withholding (supersedes the bare 30-handoff) | [operator] + [code] |
| E8 replication artifacts (corpora + harness + raw results published) | [operator] |
| **E9 longitudinal + mesh recall** — recall-over-age, recall-under-growth at fixed budget, supersession accuracy, stale-confidence, thin-node partiality honesty, transitive convergence. The time-control hook behind `cairn_testhooks` is BUILT (E9.2) and asserted absent from release binaries; what remains is the measurement. The growth curve is T0 and should land with E4 | [code, large] |

## 3. P2 completion (built, opt-in — what "done" still needs)

| Item | Kind | Source |
|---|---|---|
| Weight calibration adoption: run `cairn rank-stats --calibrate` on the 30-handoff episode data, adopt weights per the §9.3 protocol (survives holdout, stays explainable) | [data] | spec §9.3–9.4 |
| Duplicate/thread-saturation penalties (`PenaltyCap` is pinned, unreferenced). Must move in lockstep with the why-ranked record + external reconciliation (R47/R51) — its own reviewed task | [code] | spec §9.1, PROGRESS WP-G3 |
| Degradation ladder rungs 6–7 ENFORCEMENT (currently computed + reported, fail open). Needs pre-ack reserved-capacity semantics vs send-never-blocks | [ruling] then [code] | spec §8.2, README "Known limitation", PROGRESS WP-G4 |

## 4. P3 completion (mesh built, single-host-audited)

| Item | Kind | Source |
|---|---|---|
| Two-machine live pass: pairing / thin-role / transport / remote-query on real hardware over a real tailnet (the July audit ran loopback single-host for P3's additions) | [hardware] | README "On P3", PROGRESS P3 close |
| Live re-audit of pairing/trust/sync code extended SINCE the audited July commit | [hardware] | README Status caveat |
| iroh transport: the live wire, relay self-hosting + diagnostics, NAT-traversing dial-by-key (transport seam already in place) | [code, large] | spec §12 P3, `internal/peer/transport.go` |
| Automatic metered/battery sensing (manual `metered` flag exists; sensing is platform work) | [code] | spec §7, config `Metered` |
| Mutual pairing authentication (handshake currently authenticates dialer→responder only) | [code] | PROGRESS P3-2b/2c |

## 5 & 6. Deferred debt and unbuilt spec surfaces (planned work order)

These are the items that map to **no phase row in the README** — deferred
scaling debt and surfaces the spec describes but no milestone built. Full
milestones + acceptance criteria: **[build/DEBT-PLAN.md](build/DEBT-PLAN.md)**

| Item | Kind | Source |
|---|---|---|
| D1 sqlite-vec integration (brute-force cosine is the only vector path; cliff ≈ `BruteForceMaxCandidates`; becomes urgent if CAPTURE C3 lands) | [code, large] | CLAUDE.md library table, PROGRESS WP-G1 |
| D2 Origin-liveness beacon: alarm when a device's last-seen (generation, seq) regresses — deferred in P0 "requires peers"; P1 has peers now | [code] | spec §13.2, rulings §2 |
| D3 Capability `resource_selectors` (`topic="project/x/*"`, per-session budget caps) — P0/P1 shipped coarse action tiers only | [code] | spec §7.2 |
| D4 `budget_tokens` (P0 ruled `budget_chars` only; tokenizer budgets post-P0) | [code] | rulings §7 |
| D5 `cairn adopt-standalone` (merge an ad-hoc standalone mesh into the primary) — R34 permits a documented script instead; neither exists | [code] | RULINGS R34, PROGRESS N9 |
| D6 Prebuilt signed binary + Homebrew tap (today: build-from-source only; notarization half is [operator]) | [code] + [operator] | README Quickstart note |
| D7 Mutes (`mute(...)` listed in spec §7.1/§4.5; no event, op, or verb exists) — needs a ruling on whether it survives the "positive grants only" stance | [ruling] | spec §4.5, §7.1 vs §7.2 |
| D8 Third ranking profile for `explore()`-style traversal (open question; no explore surface exists yet) | [ruling] | spec §13.4 |

## 7. P4 — self-organising knowledge (evidence-gated, needs P2 usage data)

| Item | Source |
|---|---|
| Automated filing | spec §12 P4 |
| Embedding-clustered self-folding topic maps (the semantic map; P2's structural map is the precursor) | spec §12 P2/P4 |
| Salience propagation | spec §12 P4 |
| Multi-human namespaces, per-topic ACLs, payload-level encryption + key epochs | spec §2, §12 P4 |

## 8. Open author rulings (blocking their items, not the release)

Tracked in PROGRESS.md "Author rulings needed" sections; markers in code
are greppable via `RULING-NEEDED`.

| Ruling | Blocks |
|---|---|
| FIX-A6 residual: what to report when a link append fails AFTER publish is durable (conservative error-return implemented) | nothing (confirmation) |
| R38 bootstrap-trust retention breadth (`internal/daemon/daemon.go`) | nothing (confirmation) |
| R40/R41 backfill confirmation (fork-repair revoke bundling) | nothing (confirmation) |
| §8.2 reserved-slice vs send-never-blocks | item 3: ladder rungs 6–7 |
| Mutes vs "positive grants only" | item 6: mutes |

---

## 9. Execution order — what to pick up next

The sections above are the inventory; this is the order to work them, and
the honest reason each blocked item is blocked. **Nothing in the first group
needs anything from the operator**, so an agent can start there immediately.

**Buildable now, in this order:**

1. **D2 origin-liveness beacon** and **D3 capability `resource_selectors`** —
   independent of everything else, each closes a spec gap that exists today,
   each has a two-daemon or dispatch-boundary test that proves it. Best
   starting point: self-contained, no gate, real user-visible value.
2. **EVAL E4** (intrinsic quality: nDCG/MRR/Recall, ablations, baselines) —
   the apparatus can be built and exercised against the sample corpus now;
   it stays *dark* (no reported numbers) until corpora land and the kill
   criteria are signed. Building it early is safe; reporting from it is not.
3. **EVAL E6** (adversarial/safety: prompt-injection compliance through
   digest/search/fetch) — needs no external corpus, only adversarial inputs
   the harness can author, so it is unblocked in a way E4/E5 are not.
4. **D4 `budget_tokens`** and **D5 `adopt-standalone`** — small, well-scoped.
5. **D1 sqlite-vec** — least urgent alone, but a hard prerequisite if C3 is
   coming, because transcript ingest walks straight off the brute-force cliff.

**Blocked, and by what:**

| Blocked on | Items |
|---|---|
| **Operator sign-off** | EVAL E1's 21 kill criteria — these are commitments about what would count as disproof; an agent must not set them |
| **Operator activity** | corpus acquisition (E3 residual → gates E4/E5/E9), the three release blockers in §1, C4 directory listings |
| **An author ruling** | ladder rungs 6–7 (§8.2 reserved-slice vs send-never-blocks), D7 mutes, D8 explore profile |
| **A crossed review** | CAPTURE C3 — the privacy/redaction model is the expensive thing to get wrong, so the design note lands before the code |
| **Real hardware** | P3 two-machine pass, live re-audit of code changed since the July commit |
| **Usage data** | P2 weight calibration — calibrating on synthetic episodes would fit noise |

**A standing rule for anything in EVAL:** apparatus may be built ahead of
sign-off, but no measurement is reported before its kill criterion is signed.
An unfalsifiable number is worse than no number, because it looks like
evidence.

---

Maintenance rule: new planned work gets a row here (and a work-order file
if it's more than one milestone); shipped work moves to PROGRESS.md and
its row is deleted. The README roadmap table stays the coarse
phase-level view and links here for the full inventory.
