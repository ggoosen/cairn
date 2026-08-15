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
| E4–E8 | not started |
| E9 time-control hook | built (`cairn_testhooks`); the rest not started |

**Nothing here has measured anything.** No metric is computed, no backend is
compared to another, and no verdict on Cairn exists in this tree. That is
deliberate and it is the ordering BUILD-PLAN §5-E1 requires: the kill criteria
are written down and accepted *before* results exist to be tempted by. Until
an operator signs off the criteria in `claims.yaml`, measurement does not
start.

The one thing that does run is a **plumbing check** over a three-document
synthetic fixture, which proves the apparatus works. Its records are labelled
`plumbing-verification` for the obvious reason.

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
├── cmd/cairn-eval/          the harness CLI (no measurement verbs yet)
└── internal/
    ├── tunables/            every tunable constant, in one place
    ├── cairnctl/            black-box driver: provision, daemon lifecycle, CLI + MCP
    ├── backend/             the one interface all six memory conditions run through
    ├── corpus/              E3: corpus format, checksums, loader
    ├── result/              versioned run records (observations, never verdicts)
    └── boundary/            proves the black-box boundary is compiler-enforced
```

## Running it

```sh
make eval          # from the repo root: vet + test the harness
cairn-eval backends              # what each memory condition is, and what it models
cairn-eval smoke                 # plumbing check; writes labelled run records
cairn-eval corpus info  <dir>    # where a corpus came from and who made its labels
cairn-eval corpus verify <dir>   # bytes still match the manifest checksums
cairn-eval corpus mine …         # normalize mined human labels into a corpus
```

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
empty results. An unimplemented baseline that returned nothing would become a
zero in someone's table, in Cairn's favour, invisibly. That is precisely the
failure this framework exists to prevent.

## Reading a run record

`internal/result` writes versioned JSON: run id, kind, seed, backend and its
modelling notes, corpus id/version/checksum/label-source, environment, and
per-item outcomes (what was asked, what came back, what the corpus declares
relevant, payload size, latency, raw output).

It contains **no metric fields**, and a test asserts that. Turning outcomes
into scores is E4's job, and E4 starts on a claim only after that claim's
kill criterion carries an operator signoff. Keeping the recorder verdict-free
is how the apparatus is prevented from publishing a result before its
criteria were fixed.
