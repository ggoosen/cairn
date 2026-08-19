# eval — the apparatus that could falsify Cairn's claims

Work order: [`../build/BUILD-PLAN.md`](../build/BUILD-PLAN.md).
Claims register: [`claims.yaml`](claims.yaml).

Cairn's engineering claims are backed by gates. Its **product** claims are
not, and the two have been living in the same README paragraph as if they
were the same kind of statement. This directory is the machinery that could
show the product claims to be false.

## Status

| Milestone | State |
|---|---|
| E1 claims register | drafted — **every `signoff:` is `pending`** |
| E2 harness skeleton | built (this module) |
| E3 independent corpora | format, normalizers, loader and sample built; **bulk acquisition not run** — an operator step, see [corpora/ACQUISITION.md](corpora/ACQUISITION.md) |
| E4 ablations + baselines | **built, dark** — runs, computes, reports nothing |
| E5 extrinsic task battery | not started (needs the T2 agent loop) |
| E6 adversarial / safety | **built to the agent boundary, dark** — see below |
| E7, E8 | not started |
| E9 recall-under-growth | **built, dark**. Time-control hook built (`cairn_testhooks`); the rest of E9 not started |

**The apparatus runs. Nothing reports.** That is one sentence with two halves
and both are deliberate.

*It runs*, because an apparatus nobody can exercise is an apparatus nobody has
debugged. `measure`, `growth` and `adversarial` provision real meshes, ask real
questions, compute nDCG / MRR / Recall@k / Precision@k, and write structured
scorecards.

*It reports nothing*, because every kill criterion in `claims.yaml` is still
`signoff: pending`. **An unfalsifiable number is worse than no number, because
it looks like evidence.** No command prints a metric, a comparison, or a
verdict; every scorecard stamps `evidence: false` and its reason into its own
bytes; and `cmd/cairn-eval/darkness_test.go` runs the real commands and fails
if any of that erodes.

Run `cairn-eval claims` for the gate readout.

## Why this is a separate Go module

Not tidiness. Go's `internal/` visibility rule means a *separate module*
physically cannot import `github.com/ggoosen/cairn/internal/...`, so the
harness can only reach Cairn the way an agent does — the CLI and the MCP
stdio server. Black-box access stops being a convention someone has to
remember and becomes something the compiler refuses to build.
`internal/boundary` asserts both halves of that: what the harness imports
today, and that the rule itself really bites.

It also keeps the harness's future dependencies (LLM clients, statistics) out
of the daemon's small offline dependency tree, and keeps the main test
suite's properties — offline, deterministic, free, gates every commit —
uncontaminated by evaluation that will be networked, stochastic and costly.

Today this module has **no dependencies at all**, and that is worth keeping
for as long as it can be.

## Layout

```
eval/
├── claims.yaml              E1: every public claim, its threshold, its kill criterion
├── corpora/                 E3: checked-in sample corpus (bulk corpora are fetched, not committed)
├── cmd/cairn-eval/          the harness CLI
└── internal/
    ├── tunables/            every tunable constant, in one place
    ├── cairnctl/            black-box driver: provision, daemon lifecycle, CLI + MCP
    ├── backend/             the one interface all six memory conditions run through
    ├── corpus/              E3: corpus format, checksums, loader
    ├── result/              versioned run records (OBSERVATIONS, never verdicts)
    ├── claims/              the register parser — the gate reads the operator's own file
    ├── metric/              E4: nDCG, MRR, Recall@k, Precision@k, seeded bootstrap CIs
    ├── explain/             parses `cairn why-ranked`; the only black-box route to an ablation
    ├── ablation/            E4: the arm catalogue, each with its fidelity and its limits
    ├── experiment/          runs a condition, records it; computes no comparison
    ├── score/               derived scorecards AND the reporting gate
    ├── growth/              E9: recall-under-growth corpora
    ├── adversarial/         E6: injection payloads, containment checks, compliance scoring
    └── boundary/            proves the black-box boundary is compiler-enforced
```

Two boundaries in that list carry the design.

**`result` vs `score`.** An observation and a judgment have different lifetimes
and different trust. A run record is a fact about a run and stays true forever;
a score is an interpretation, and an interpretation computed before its
falsification criterion was fixed is worth nothing, because nobody can now show
the criterion was not chosen to fit it. So run records carry no metric field —
a test asserts it — and scores live in a separate, gated artifact.

**`experiment` vs `score`.** `experiment` runs things and records what
happened. `score` turns that into numbers and refuses to render them.

## Running it

```sh
make eval          # from the repo root: vet + test the harness

cairn-eval claims                # THE GATE READOUT: which kill criteria are signed
cairn-eval backends              # what each memory condition is, and what it models
cairn-eval ablations -v          # E4's arms, their fidelity, and what they cannot show
cairn-eval smoke                 # plumbing check; writes labelled run records

cairn-eval measure     -corpus <dir> [-arms all]   # E4: baselines × ablations
cairn-eval growth      -corpus <dir> [-scales …]   # E9: recall under corpus growth
cairn-eval adversarial [-list]                     # E6: planted prompt injections

cairn-eval corpus info  <dir>    # where a corpus came from and who made its labels
cairn-eval corpus verify <dir>   # bytes still match the manifest checksums
cairn-eval corpus mine …         # normalize mined human labels into a corpus
```

`measure` and `growth` refuse an INDEPENDENT corpus while the criteria they
bear on are unsigned, with no override flag. Running the synthetic sample is
fine — it is labelled not-evidence and its job is to prove the plumbing.
Running mined human ground truth produces the first half of real evidence, and
fixing the thresholds afterwards is exactly what E1 exists to prevent.

The harness module contains **no HTTP client**: corpus downloading is an
operator step (`gh api`, `curl`), so the T0 tier's offline property is
structural rather than a habit. Format: [corpora/FORMAT.md](corpora/FORMAT.md).

The driver builds `cairn` from the enclosing repository with
`-tags sqlite_fts5,cairn_testhooks`. Point `CAIRN_EVAL_BINARY` at a prebuilt
binary to evaluate a release artifact instead. `CAIRN_EVAL_KEEP=1` leaves a
throwaway instance's directory on disk for inspection.

## The memory conditions (BUILD-PLAN §5-E4)

| ID | Condition | State |
|---|---|---|
| B0 | no memory (cold agent) — the control | implemented |
| B1 | grep over raw transcripts — **the one to beat** | implemented |
| B2 | flat append-only markdown notes | implemented |
| B3 | naive vector-DB RAG | declared stub (needs T1) |
| B4 | full-context stuffing | declared stub (needs E5's agent loop) |
| B5 | Cairn, black-box over the CLI | implemented |

Two things about this table are load-bearing.

**The baselines are models, and the model is an assumption.** B1 returns
matches in file order because grep has no ranking; B2 reads newest-first
because that is what a person does with their own notes. Those choices are
recorded in each backend's `Capabilities().Notes` and travel into every run
record, because a baseline that quietly acquired ranking would stop being the
thing it represents — and beating a strawman proves nothing.

**The stubs fail loudly.** B3 and B4 return `ErrNotImplemented` rather than
empty results, and they do so INSIDE the matrix — an unrunnable condition
appears in the output as a stated refusal, not as an absence. An unimplemented
baseline that returned nothing would become a zero in someone's table, in
Cairn's favour, invisibly. That is precisely the failure this framework exists
to prevent.

## Ablation fidelity (E4)

Cairn exposes no CLI switch for ±freshness, ±priority decay or vector-only, and
this module is separate precisely so it cannot reach in and flip one. So every
arm declares HOW it was produced, and the fidelity travels into every scorecard
section next to the numbers it qualifies:

- **native** — the system under test was configured this way and measured.
  Rank profile and embedder presence via process environment; mandatory
  inclusion via how the corpus is written. Best evidence.
- **recomputed** — the returned results were re-ranked from the published
  `cairn why-ranked` arithmetic, which is the same route an external auditor
  has. A **lower bound**: it cannot surface a document retrieval never
  returned, so **Recall@K at the requested K cannot move** and must not be read
  as "the ablation did not hurt recall". Every trace is reconciled (printed
  products must sum to the printed total) before use, because a silent parse
  failure would look exactly like a successful ablation.
- **unavailable** — no black-box route exists. `priority-undecayed` is one:
  `why-ranked` publishes the decayed `P_eff` but not the undecayed normalized
  priority, so "priority does not earn its place" cannot be separated from "the
  DECAY does not earn its place". It FAILS LOUDLY rather than running the
  default condition under the arm's name.

An arm that fails to TAKE EFFECT is an error too — a P2 arm that stayed P0, a
vector-only arm on a lexical-only daemon. A silently-untaken arm reads as "this
ablation made no difference", which is a mislabelled result and worse than a
missing one.

## E6 and the agent-in-the-loop boundary

Compliance is an agent behaviour, so E6 has a seam and it is worth knowing
exactly where it is.

**Built** (T0, offline, deterministic, free): the payload catalogue, planting
into a real mesh, the exact bytes an agent would be handed on digest / search /
fetch (kept in the report as `agent_bytes`, so a third party can run their own
agent without rerunning the harness), every daemon-side containment check
including the R56 authorship gate end to end, and the scoring of a transcript
once one exists.

**Not built**: the agent runner. That is T2 — networked, stochastic, costly —
and it is E5's infrastructure; a second agent loop here would guarantee two
that disagree. The seam is `[]adversarial.Transcript → Score()`, and
`Score`/`Rate` return `ErrAgentRequired` rather than a zero. **A compliance
rate of zero that was never measured is the most damaging number this
repository could publish.**

Containment checks have three states, not two. A check whose preconditions were
never met — no injection reached this surface, the record was never planted —
is **inconclusive** and counted separately, because calling it a pass
manufactures containment and calling it a failure manufactures a bug.

The transcript scorer classifies four ways: `acted` (objective — a tool call
carrying the marker, which quoting cannot produce), `not-present`,
`quoted-while-refusing`, and `ambiguous`. Ambiguous cases go to an adjudication
queue and no rate is called final until a human clears it.

## Reading a run record

`internal/result` writes versioned JSON: run id, kind, seed, backend and its
modelling notes, corpus id/version/checksum/label-source, environment, and
per-item outcomes (what was asked, what came back, what the corpus declares
relevant, payload size, latency, raw output).

It contains **no metric fields**, and a test asserts that. Recomputed
ablations store the raw `why-ranked` traces rather than the re-ranked ordering,
so the record stays primary and the ablation derived — a third party can
recompute a different ablation from the same file, or check ours, without
rerunning anything.

## Reading a scorecard

`internal/score` writes the derived artifact: per-arm metrics with their
denominators (`n`, `excluded`, and the distinct exclusion reasons), the arm's
fidelity and limits verbatim, and the signoff state of every claim it bears on
at the moment it was computed. `evidence: false` and
`not_evidence_reason` are written into the file, so a scorecard that escapes
into a document argues against itself.

`Reportable()` opens only when **both** halves hold: every bearing claim
carries a dated operator signoff, and the corpus declares independent labels.
Neither is sufficient. Signed criteria over a corpus this project authored is
still a statement about the harness.
