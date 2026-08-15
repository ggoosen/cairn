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

## 5. Scaling & distribution debt

| Item | Kind | Source |
|---|---|---|
| sqlite-vec integration (brute-force cosine is the only vector path; cliff ≈ `BruteForceMaxCandidates`; becomes urgent if CAPTURE C3 lands) | [code] | CLAUDE.md library table, PROGRESS WP-G1 |
| Prebuilt signed binary + Homebrew tap (today: build-from-source only) | [code] | README Quickstart note |
| Origin-liveness beacon: alarm when a device's last-seen (generation, seq) regresses — deferred in P0 "requires peers"; P1 has peers now | [code] | spec §13.2, rulings §2 |

## 6. Specified but never built (smaller gaps)

| Item | Kind | Source |
|---|---|---|
| Mutes (`mute(...)` listed in spec §7.1/§4.5; no event, op, or verb exists) — needs a ruling on whether it survives the "positive grants only" stance | [ruling] | spec §4.5, §7.1 vs §7.2 |
| Capability `resource_selectors` (`topic="project/x/*"`, per-session budget caps) — P0/P1 shipped coarse action tiers only | [code] | spec §7.2 |
| Third ranking profile for `explore()`-style traversal (open question; no explore surface exists yet) | [ruling] | spec §13.4 |
| `cairn adopt-standalone` (merge an ad-hoc standalone mesh into the primary) — R34 permits a documented script instead; neither exists | [code] | RULINGS R34, PROGRESS N9 |
| `budget_tokens` (P0 ruled `budget_chars` only; tokenizer budgets post-P0) | [code] | rulings §7 |

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

Maintenance rule: new planned work gets a row here (and a work-order file
if it's more than one milestone); shipped work moves to PROGRESS.md and
its row is deleted. The README roadmap table stays the coarse
phase-level view and links here for the full inventory.
