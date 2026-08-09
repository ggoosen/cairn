# Audit trail — commit SHA translation

The audit reports produced during P0/P1 hardening (the N9 network audit, the
pre-N9 verification, the P2 shakedown, the P3 briefs) cite commit SHAs to pin
exactly what was tested — for example, the N9 run-6 report records
`vcs.revision=592df3ec…` for the binaries running on both nodes.

**Those SHAs do not resolve in this repository.** `git filter-repo` was run on
2026-07-24 to strip internal working documents before the tree was made public,
which rewrote every commit hash in the history.

[`commit-map-2026-07-24.txt`](commit-map-2026-07-24.txt) is the mapping that
rewrite produced: `old` (pre-filter) in the first column, `new` (present
history) in the second. To resolve a SHA quoted in an audit report:

```sh
grep ^592df3ec docs/audit/commit-map-2026-07-24.txt
# 592df3ec...  733082d...   <- look up the right-hand SHA in this repo
```

It is committed for one reason: an audit you cannot trace to a commit is an
assertion, not evidence. The reports themselves are internal working documents
and are not published — but the claims made from them in the README should stay
checkable by anyone who wants to verify what was audited and how far the code
has moved since.

Note that the audits certify the commit they name, not `master`. See the
"What 'audited' means here" section of [`SECURITY.md`](../../SECURITY.md).
