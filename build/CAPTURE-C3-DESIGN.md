# CAPTURE C3 — session-transcript ingest: design note (DESIGN ONLY, pre-review)

Status: **design, not sanctioned to build.** `build/BUILD-PLAN.md` §2 requires
the privacy/redaction design to get a crossed adversarial review — the same
treatment R56 got — BEFORE any code, because capture is a trust-surface
change. Nothing here is implemented. This note exists to give that review
something concrete to attack, and to record what reading the shipped code
already settles.

Precedence: RULINGS.md > `docs/rulings-v0.3.1.md` > `docs/spec-v0.3.md` >
`build/BUILD-PLAN.md` > this note.

---

## 1. What the plan fixes and what it leaves open

CAPTURE-PLAN C3 pins five non-negotiables: transcript chunks are
`eager-searchable`/`ephemeral` and never `canonical`; they are excluded from
digests and PULL-ONLY via search; `source_ref` carries path + session + chunk
position; ingest is opt-in per directory with redaction BEFORE the object
store; re-scan is idempotent via deterministic chunk boundaries.

It leaves three things to build time, and this note takes a position on each:

1. **The digest-exclusion mechanism** — topic namespace + view filters, or an
   explicit source flag in the projection. The plan says "prefer the mechanism
   that keeps `DigestCandidates` SQL simple."
2. **Redaction scope** — what a pattern-based pass can and cannot promise.
3. **Chunk boundaries** — turn-groups with a size cap, but the determinism
   requirement is load-bearing and under-specified.

## 2. Digest exclusion: the topic-namespace option does not work

Reading the shipped code settles item 1 against the plan's first option.

`Projection.DigestCandidates(topicNames)` (`internal/projection/rankq.go`)
takes the view's hard topic filter. **With an empty filter — the default for
every view — it returns every non-retracted message in the mesh.** A
`transcript/<source>` namespace therefore excludes transcripts only from views
that have opted into an explicit allow-list of topics. Any view without one
(the default) would start receiving transcript chunks as ordinary digest
candidates the moment ingest ran. That is precisely the failure the plan's own
acceptance criterion forbids ("the digest for every view is byte-unchanged
before vs after ingest").

Worse, it fails OPEN: the operator sees nothing wrong until a digest fills with
raw transcript. A mechanism whose safe state depends on every view having
configured a filter is the wrong default for a trust boundary.

**Position: an explicit source flag on the projection's `messages` table**
(e.g. `source_class TEXT NOT NULL DEFAULT 'curated'`, set to `'transcript'`
from the publish event's `source_ref`), with `DigestCandidates` filtering
`source_class = 'curated'` unconditionally in BOTH of its branches. That is one
extra predicate in two queries — simpler than the namespace option, not more
complex — and it fails CLOSED: a transcript message is excluded from digests by
default and by construction, regardless of view configuration.

Consequences to check in review:

- The **subscription/interest** path (`internal/daemon/retrieve.go`, the
  `LexicalTopK(cfg.InterestQuery, …)` call) is a second route into a digest.
  It must apply the same exclusion, or a standing interest becomes a back door
  into transcript content. Enumerate every path that composes a digest and gate
  all of them (R46 invariant sweep — this is exactly the class of bug R46
  exists for, and the note should not pretend one grep found them all).
- `search` must NOT exclude them (that is the whole point) but MUST render the
  source class in the result line, so a hit reads as "raw transcript,
  session X" and never as a curated note.
- The flag is projection-class, derived from the event. It survives reindex
  because it is recomputed from `source_ref`, not stored independently.

## 3. Redaction: what it can honestly promise

Transcripts contain pasted credentials and raw tool output. Redaction runs
BEFORE `store.Put`, because a stored object is durable, content-addressed, and
replicated — there is no unring-the-bell.

What a pattern pass can promise: **recognizable, structured secrets** — AWS
keys, GitHub/Slack/Stripe tokens, PEM private-key blocks, `Authorization:`
headers, JWTs, connection strings with inline passwords, high-entropy values
assigned to names matching `(?i)(secret|token|password|api[_-]?key)`.

What it CANNOT promise, and the doc must say so in the operator's own words
rather than in a footnote:

- unstructured secrets (a password typed in prose, a customer name, a home
  address) survive redaction;
- a bespoke internal token format is not in the pattern set;
- redaction is per-chunk and pattern-based, so a secret split across a chunk
  boundary can evade it — which is an argument for redacting the whole source
  file's text before chunking, not each chunk after.

**Position: redact the decoded transcript text before chunking**, keep the
pattern set in `internal/config` (one place, reviewable, no magic regexes
scattered in code), and make the residual risk a first-class paragraph in the
docs: *transcript ingest puts session text into a durable, replicated store;
redaction reduces the blast radius of structured secrets and does not make
transcripts safe to ingest from a directory you would not otherwise publish.*

An open question for the review: whether a redaction HIT should be recorded
(count, pattern name, chunk position — never the matched bytes) so an operator
can see that ingest is finding secrets in a directory they thought was clean.
The argument for: silence about a live secret-bearing corpus is the R45 failure
mode. The argument against: the count itself is a weak oracle.

## 4. Chunk determinism is the idempotency mechanism

"Re-scan is a no-op" is not implemented by a scan-time check — it falls out of
content addressing, but ONLY if identical input produces byte-identical chunks.
That makes the chunker a correctness surface, not a formatting choice:

- boundaries derive from the transcript's own turn structure, never from
  wall-clock, ingest order, map iteration, or a size heuristic that depends on
  anything outside the file;
- the size cap splits a turn-group at a deterministic offset (the cap itself is
  a config constant, and CHANGING it re-chunks the corpus — a versioned
  `chunker_version` in `source_ref` makes that visible instead of silently
  duplicating every message);
- a transcript file that GROWS (an appended session) must re-chunk its existing
  prefix identically, or every re-scan duplicates the whole session. This is
  the case most likely to be got wrong and the one the acceptance test should
  target first.

The existing M9 ingest path (`internal/ingest`) already models the right shape:
`scan` produces a reviewable manifest classifying each unit publish/revise/skip
against `source_refs` + head body hash; `apply` executes it through the daemon
with no operator class override. A transcript source is a new scanner over the
same manifest/apply machinery, not a new write path — which is also what makes
the durability ordering and the fault matrix inherited rather than re-argued.

## 5. Things this note deliberately does not decide

- **The adapter interface** for non-Claude-Code transcript formats. One
  concrete adapter first; generalize on the second, not before.
- **sqlite-vec (G1) and duplicate/thread-saturation penalties (G3).** Both are
  dragged forward by transcript volume, both are their own reviewed tasks, and
  G3 must move in lockstep with the why-ranked record and R47/R51 external
  reconciliation. Neither is a C3 sub-task.
- **Whether ephemeral or eager-searchable is the default class.** Ephemeral
  gives TTL-bounded exposure but inherits R50's delivery-window semantics
  (a node not connected at publish time never gets the body), which interacts
  with transcript ingest on a multi-node mesh in ways worth their own analysis.

## 6. What the crossed review should try to break

1. Ingest a transcript, then diff every view's digest byte-for-byte — including
   a view with an interest query and no topic filter, and a view with a durable
   subscription.
2. Seed a fake API key in the transcript; assert it is absent from the object
   store, from FTS (both indexes — the trigram companion indexes the same
   bytes), and from any rendered surface.
3. Seed a secret straddling a chunk boundary.
4. Re-scan unchanged → zero events. Append to the session and re-scan → only
   the new tail publishes.
5. Point ingest at a directory with a symlink escaping the root, and at a file
   whose "transcript" is 400 MB of one line.
6. A transcript chunk containing `> [CAIRN] `, region markers, or a markdown
   heading — R53's rendered-field obligation applies to transcript text exactly
   as to a topic name.
