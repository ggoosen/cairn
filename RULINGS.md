# RULINGS.md — Binding Post-P0 Rulings

**Process rule (FIX-F5):** design-conversation rulings are implemented ONLY
after they land in this file. Precedence:
`docs/rulings-v0.3.1.md` > `docs/spec-v0.3.md` > **this file** > CLAUDE.md >
builder judgment. This file supplements the v0.3.1 rulings for everything
decided after them; where it conflicts with v0.3.1 it records an explicit
adjudication, not a silent override.

Provenance: retroactive capture of the design-conversation rulings that
never reached the repo (the F5 process failure found by both audits), plus
every ruling in `docs/cairn-p0-fix-workorder.md` (adjudication of the two
FAIL audits of commit d0aca05).

---

## R1 — Root key: P0 storage, export-root, P1 ceremony

1. **P0 storage:** the mesh root key is generated at `cairn init` and stored
   DEVICE-LOCAL (0600), never inside the portable mesh directory. `init`
   prints an explicit offline-backup instruction. (Adjudicates the M0 open
   ruling; supersedes spec §3.1's offline-only placement for P0 because the
   P0 migrate ceremony must root-sign `device.add`/`device.revoke` offline.)
2. **`cairn identity export-root [--out <path>] [--remove-local]`:** writes
   the root key material for offline storage, VERIFIES the export by
   read-back before offering anything, prints storage/usage instructions,
   and OFFERS (interactive prompt, or the explicit flag — never forced in
   P0) to remove the device-local copy. The export path must be OUTSIDE the
   portable mesh directory (it must never travel with backups). Existing
   files are never overwritten. After removal, `cairn migrate` requires
   restoring the exported key and says so.
3. **P1 device-add ceremony:** admitting a second device in P1 is an
   operator ceremony that requires producing the root key (from the export
   or the device-local copy) to sign the new cert — never an automatic or
   network-negotiated act. (Recorded for P1 design; no P0 code.)

## R2 — Resolve-merge base semantics (M5 adjudication)

`cairn resolve` applies `RESOLVE.md` as the authoritative human resolution
**relative to the conflict-time head** (the CURRENT.md the operator saw).
The original export base is NOT replayed (it re-conflicts forever on the
resolved lines). If the head moved again during resolution, the resolution
is diff3-merged against the new head (base = conflict-time head): clean →
apply; conflict → the materialization is REFRESHED (CURRENT.md, RESOLVE.md
seed, manifest) and **RESOLVE.md content is never lost**. The resolution
event preserves the operator branch (OPERATOR_EDIT.md, parent = original
base) and the resolved head (parents = [branch, current head]), with
`machine_merged=false` (human merge).

## R3 — Enrichment timing (confirms rulings v0.3.1 §6)

Append + fsync + ack are synchronous; the lexical FTS insert is synchronous
(digest visibility inside the 200 ms gate); embeddings run on an in-process
background goroutine. A just-sent message may briefly be `lexical_only` —
degradation-ladder step 4, not an error. Senders NEVER wait on enrichment
and NEVER see rejection after acknowledgement.

## R4 — Topic resolution and pre-ack referential validation (FIX-F1)

1. `cairn send --topic <ref>` resolves each ref as topic_id, then topic
   name, against the projection head. Unresolved refs on the operator CLI
   path AUTO-CREATE: `topic.create` then `topic.link.add`, in that order,
   in the SAME request — all events durably appended before the single
   ack/receipt. Outbox callers and `cairn link` never auto-create:
   unresolved refs are rejected BEFORE ack with a clear error.
2. **General rule:** any event whose intra-mesh references (topic ids,
   message ids targeted by links/replies/signals/retractions, pin ids,
   link ids, pinned object hashes) cannot resolve against the projection
   head or an earlier event in the same request batch is rejected before
   acknowledgement. An event that cannot project is never acked. Pre-ack
   rejection is legal; post-ack rejection never is.
3. **Projector parking (defense in depth):** a historical event that
   nevertheless fails projection is parked in `parked_events(event_id,
   error, parked_at, …)` within the same projection transaction, logged
   loudly, and the stream CONTINUES — startup replay, live sends, and
   reindex share identical semantics. `reindex` completes with exit 0 plus
   a prominent parked report; it never aborts. Doctor treats parked events
   as a failure condition. The log is never rewritten — parking is a
   projection-layer concept only.

## R5 — Two-pass replay and mesh-level trust (FIX-F2)

1. Replay pass 1 scans ALL origins for security-log events (genesis,
   device.add, device.revoke) and establishes the mesh membership/key set
   (root signatures make certs verifiable independent of origin placement;
   implementation: shared chain verifier iterated to fixpoint). Pass 2
   verifies and projects each origin's stream against that seeded set. The
   permanent security log is ONE stream class (spec §4.5) regardless of
   which origin's segments carry it.
2. `cairn identity show` verifies MESH-level trust: the genesis wherever it
   lives, plus the current device's membership and revocation status. It
   works identically before and after migration.
3. **Revocation ordering:** device.revoke gates WRITES from its causal
   position onward; events validly signed before revocation remain valid on
   replay. Keys of revoked devices stay in the verification set.

## R6 — Doctor depth (FIX-F3)

`cairn doctor` fails loudly (nonzero exit) on ANY of: parked events;
projection checkpoint AHEAD of the log or an unverifiable origin; missing
or hash-invalid objects referenced by non-expired, non-ephemeral revisions/
attachments; unresolved cross-origin trust; any log-integrity problem.
Informational (never failures): expired ephemerals; an ABSENT projection
database (derived, rebuildable); a checkpoint BEHIND the log with zero
parked events (parking makes silent stalls impossible, so behind always
heals by replay — e.g. after an offline migrate appends security events).
`cairn gates`' zero-loss row cites this deepened doctor.

## R7 — Build integrity (FIX-F4)

A binary that compiles but cannot start is never acceptable. The untagged
build fails AT COMPILE TIME with an instructive error naming the fix
(`make build` / `go build -tags sqlite_fts5`). `make verify` asserts both
the instructive failure of the plain command and the green tagged suite,
and is the CI entry point.

## R8 — CLI contract (FIX-F5.3)

Unknown subcommands — and bare group commands — exit NONZERO. Help text may
accompany the error; exit 0 may not.

## R9 — Restore semantics (FIX-F6.1; refines spec §3.2)

A portable-only restore (data without device-local identity) refuses ALL
writes and says so, pointing at `cairn init --adopt`. Read-only operations
— doctor, search, digest, fetch, identity show — are PERMITTED against the
restored data (spec §3.2 mandates refusing writes; read access is
explicitly allowed).

## R10 — Ephemeral TTL ergonomics (FIX-F6.2)

`EphemeralTTL` and `HousekeepInterval` are portable-config tunables
(constants remain the defaults). The daemon runs one housekeeping sweep at
startup; `cairn housekeep` triggers one manually.

## R11 — Version string (FIX-F6.3)

The binary version derives from build info (VCS revision when available)
and reflects P0 status — never a hardcoded milestone string.

## R12 — Interpretations adjudicated during P0 (recorded for re-audit)

- **source_ref paths** are stored as `repo/relpath`, making the
  single-column `source_refs.path` primary key collision-free across repos.
- **Protected-link removal:** `topic.link.remove` from a non-operator
  principal skips protected links; operator removals always apply
  (spec §5.5 "auto-processes may not remove"). Revisit under P1
  capabilities.
- **Invalid signature in the local log:** recovery refuses to start
  (single-writer log ⇒ corruption); doctor surfaces it. Quarantine-and-
  continue semantics belong to P1 replication of foreign origins.
- **Adopt:** `cairn init --adopt` archives the old history read-only at
  `events-preadopt-<old-cairn-id>/` (never deleted), retains objects,
  drops stale derived state, and creates a new origin identity.
