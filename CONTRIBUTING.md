# Contributing to Cairn

Issues and PRs are welcome. Cairn is pre-alpha and built by one person, so the
most valuable contribution right now is usually **a good bug report from real
use**, not a patch.

Before anything else: security problems do **not** go in a public issue — see
[SECURITY.md](SECURITY.md).

## Getting set up

```bash
git clone https://github.com/ggoosen/cairn && cd cairn
make build          # bin/cairn
make verify         # the merge bar: guard check + vet + full tagged suite
```

You need **Go 1.25+** (or any Go 1.21+ with `GOTOOLCHAIN=auto`), **git** — at
runtime too, for `git merge-file` — and a **C toolchain**, because Cairn uses cgo
for SQLite/FTS5 (`xcode-select --install` on macOS).

Always build and test through `make`. A plain `go build ./...` **fails at
compile time by design**: `mattn/go-sqlite3` only compiles FTS5 behind
`-tags sqlite_fts5`, and a silent non-FTS5 build would be worse than a loud
failure. `make verify` asserts both that the untagged build fails with the
instructive message *and* that the tagged suite is green.

Tests need a second tag, `cairn_testhooks`, which compiles in the
volume-status fault injector the encryption tests drive. `make test` sets it;
`make build` deliberately does not, so a release binary has no way to be told
"this volume is encrypted" by its environment. If you run `go test` by hand,
use `-tags sqlite_fts5,cairn_testhooks` or roughly thirty tests will fail on
the encryption gate.

macOS arm64 is the primary target, but Linux is not a second-class citizen in
CI: the verify matrix, race, lint and fuzz-smoke jobs all run on Linux and all
gate the merge.

## The bar for merging

**If your change touches the write path, it ships with crash-matrix coverage or
it doesn't ship.** Read [`build/TESTING.md`](build/TESTING.md) first. The write
path means: the event log, the object store, the outbox, durability ordering,
or anything that decides when an event is acknowledged.

That rule exists because Cairn's core promise — *zero acknowledged-event loss* —
is only worth as much as the tests behind it. `internal/fsx` provides a
fault-injecting filesystem that models real power loss (unsynced writes vanish;
directory entries survive only after a dir-fsync), and crash tests are expected
to use it rather than simulate a crash by closing a file.

Everything else, in rough order of how often it comes up:

- **`make verify` must pass** before you open the PR, and CI must be green.
- **No magic numbers.** Every tunable — ranking weights, half-lives, seal
  thresholds, limits, timeouts — lives in `internal/config/constants.go` with a
  comment explaining the value. This is enforced by review, not by a linter.
- **No floats in event payloads.** Integers and RFC 3339 UTC strings only.
  Serialization is RFC 8785 canonical JSON.
- **Don't redesign around the spec.** `docs/rulings-v0.3.1.md` and `RULINGS.md`
  bind: where the code and a ruling disagree, the ruling wins. If you think a
  ruling is wrong, open an issue about the ruling before writing the code.
- **Deferred features stay deferred.** The spec's §14 exclusions and the P4
  roadmap items are deliberate scope control, not oversights.
- **Match the surrounding code.** Its comment density is higher than typical Go —
  comments explain *why a constraint exists*, often citing a ruling number. Keep
  that.

## Commits and PRs

Commit messages in this repo describe the change and its reasoning, not just the
diff — `git log` is part of the decision trail. One logical change per commit.

Keep PRs small and single-purpose. A PR that fixes a bug and also reorganises
three packages will be asked to split. In the description, say what you tested
and on what platform.

## Contributor License Agreement

Cairn is source-available under
[PolyForm Noncommercial 1.0.0](LICENSE), with commercial licensing available
separately. Keeping that dual arrangement possible means the project needs the
rights to relicense contributed code — so **substantial contributions require a
CLA**.

In practice: small fixes (typos, docs, an obvious one-line bug) are merged
without ceremony. For anything larger, you'll be asked to sign
[**`CLA.md`**](CLA.md) before the merge — it follows the Apache ICLA almost
verbatim, you keep full ownership of your work, and signing is a comment on
your PR rather than a form to fill in. It's worth reading *before* you write a
lot of code if that's a problem for you. This is the one thing about
contributing here that differs from a typical MIT project, and it's better
raised early than at merge time.

Contributing on behalf of a company? Open an issue titled `[cla]` first — that
needs a corporate agreement instead.

## Good first contributions

- **Use it and report what broke.** Especially: a platform that isn't macOS
  arm64, a corpus much larger than one person's, or anything that makes
  `cairn doctor` unhappy.
- **Linux hardening.** Linux is genuinely best-effort; the encryption check,
  systemd service path, and performance under `synchronous=FULL` all deserve
  someone who runs Linux daily.
- **Docs that describe reality.** If something in the README or `DOGFOOD.md`
  doesn't match what the software does, that's a real bug — file it.
