# Corpus format (v1)

EVAL-PLAN §5-E3. A corpus is a directory of three files:

```
<corpus>/
├── manifest.json   what this is, where it came from, WHOSE judgments these are, checksums
├── items.jsonl     one retrievable document per line
└── queries.jsonl   one information need per line, with its human-made relevance judgment
```

Read and verify one with `cairn-eval corpus info <dir>` / `corpus verify <dir>`;
the Go loader is `eval/internal/corpus`.

## The two fields that matter most

**`labels.provenance`** — prose: who made these relevance judgments, and how
were they mined? E3 exists because the golden corpus was written, queried and
judged by the same person whose project it evaluates. A corpus that cannot
answer this question is not evidence.

**`labels.independent`** — true only when neither the content nor the
judgments came from this project. False does not make a corpus useless; it
makes it a **regression gate** rather than evidence, and the tooling says so
out loud every time it prints such a corpus.

## manifest.json

| field | meaning |
|---|---|
| `schema_version` | format version. The loader REFUSES a version it does not know rather than misreading moved fields. |
| `id`, `version` | corpus identity, cited by every run record. |
| `source.kind` | `github-duplicate-issues` · `stackoverflow-duplicates` · `doc-cross-references` · `synthetic` |
| `source.origin` | the specific body of material (repo, site, checkout). |
| `source.command` | the exact invocation that produced it — E8 publishes corpora, and one nobody can regenerate is a snapshot, not an artifact. |
| `source.license` | mined corpora carry other people's content; their terms travel with it. |
| `labels.*` | provenance, independence, and per-kind query counts. |
| `counts` | items, queries, dev/holdout split sizes; verified against the files on load. |
| `files[]` | per-file sha256 + byte length. |
| `checksum` | sha256 over the file digests — the corpus's identity in a result record. |
| `notes[]` | the modelling choices the miner made. Read these before believing a number. |

## items.jsonl

```json
{"id":"gh-o-r-1","title":"server crashes on empty config","body":"…","topics":["corpus/github-issues"],"created_at":"2026-01-01T00:00:00Z","url":"https://…"}
```

`id` is the ground-truth key: a retrieved hit is matched back to it, so it
must be unique and stable. `created_at` is the source's own publication time,
which E9 replays chronologically against the simulated clock.

## queries.jsonl

```json
{"id":"q-gh-o-r-2","query":"crash when config file is empty","relevant":["gh-o-r-1"],
 "label_kind":"github_marked_as_duplicate","label_url":"https://…","labeled_at":"2026-01-02T01:00:00Z","split":"dev"}
```

| field | meaning |
|---|---|
| `relevant` | item ids a HUMAN judged relevant. Never empty; the loader refuses a query without a judgment, and refuses one pointing at an item that does not exist (that would read as a retrieval failure and blame the system for the corpus's mistake). |
| `label_kind` | the KIND of judgment — recorded per query, not per corpus, so a corpus may mix signals of different strength without hiding it. A duplicate marker is a strong signal; a documentation link asserts relevance, not best-answer, and is labelled differently. |
| `label_url` | where a sceptic can go to disagree with the judgment. |
| `split` | `dev` or `holdout`. |

## Splits

`corpus.AssignSplits` hashes the query id with a fixed salt
(`tunables.SplitSalt`) and holds out a fixed share
(`tunables.HoldoutFractionPerMyriad`, currently 30%).

Deterministic on purpose: anyone can recompute the partition from the query
ids alone. A split that could be re-rolled is a split that can be re-rolled
until the holdout flatters the system. Changing the salt invalidates every
result measured against the old split, and any such change belongs in the
same commit as that admission.

Weights are calibrated on `dev` only (EVAL-PLAN §8: no tuning on the
evaluation set); `holdout` answers the claim.

## Acquiring a corpus

Acquisition reaches the network, so it is an **operator step** — the T0 test
tier never does. See [`ACQUISITION.md`](ACQUISITION.md) for the commands.

## What is checked in here

Only `sample-plumbing-v1`: six documents and six questions written by this
project, declared `independent: false`, existing solely to give the format,
the checksum verification and the harness loader something to chew on
offline. It is **not evidence**, and `cairn-eval smoke -corpus` refuses to run
a corpus that claims independent labels precisely so that this path cannot
become an unsanctioned measurement.
