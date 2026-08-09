# Security policy

Cairn is **pre-alpha**. It has been hardened and audited (see below for exactly
what that does and does not mean), but it has not been reviewed by human
security professionals, and it should not yet be trusted with data you cannot
afford to lose or leak.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting:
**[Security → Report a vulnerability](https://github.com/ggoosen/cairn/security/advisories/new)**.
That channel is private between you and the maintainer, and it works without
either of us publishing an email address.

Useful things to include: the commit you tested, your platform, whether the node
was single-machine or meshed, and the smallest reproduction you can manage. If
you have a patch, attach it to the advisory rather than opening a PR — a PR
discloses the issue publicly the moment it is pushed.

**Expect a first response within about a week.** This is a one-person
pre-alpha project, not a funded product; if the fix needs a coordinated
disclosure window, we agree it in the advisory thread. Credit is given by
default unless you ask otherwise.

## What is in scope

Anything that breaks one of Cairn's actual guarantees:

- **Acknowledged-event loss.** An event that was acked and then did not survive.
- **Log integrity.** Forging or altering a signed event; defeating the per-origin
  hash chain; making `cairn doctor` report clean over a tampered log.
- **Membership.** Joining a mesh, or continuing to write to one after
  revocation, without a valid device certificate chained to the mesh root.
- **Key handling.** Any path that writes a private key into the portable mesh
  directory, or lets a portable-only restore impersonate an existing origin.
- **Untrusted content.** Escaping the MCP untrusted-content envelope, forging a
  provenance line, or getting attacker-authored text treated as instructions or
  as configuration.
- **Attachment parsing.** Hanging, OOMing, or crashing the daemon with a
  malicious PDF/HTML/DOCX attachment, despite the size, page, and timeout caps.
- **Replication.** Making a peer accept data it should have refused — for
  example backfilling ephemeral text outside its delivery window.

## What is already known, and therefore not a finding

These are documented design limits, not vulnerabilities. Reports that only
restate them will be closed with a pointer here — though a report showing one is
**worse than described** is very much in scope.

- **Local processes are not isolated from each other.** The daemon cannot
  distinguish same-OS-user callers except by the handle they present. A caller
  that presents no session handle is treated as the operator. Capability
  profiles (`cairn run --profile`, `cairn mcp`) bound what an *agent* can do;
  they are not a boundary against a hostile local process. See "Security
  posture" in the README.
- **`CAIRN_FAKE_VOLUME_STATUS`** is a fault-injection hook for the encrypted
  volume tests that is present in release builds. Setting it to `encrypted`
  causes the encryption gate to pass without checking, and unlike
  `--allow-unencrypted` it does not warn on every start. It requires control of
  the daemon's environment. This should be behind a build tag and is tracked as
  a fix.
- **Key file mode is enforced on write, not on read.** Keys are written `0600`;
  a key you later loosen yourself will still load.
- **The unencrypted-volume escape hatch is real.** `--allow-unencrypted` puts
  your device key on unencrypted storage on purpose. It warns on every start.
- **The two most aggressive degradation-ladder stages are not enforced** — they
  are computed and reported, and fail open. See "Known limitation" in the README.
- **Sealed segments and stored objects are immutable by design.** Cairn will
  refuse to overwrite an object rather than dedup a hash collision; that refusal
  is intended.

## What "audited" means here

The README says P1 passed a crossed two-auditor network security audit. Read
that precisely:

- It was six rounds of adversarial testing across **two real machines over a
  real tailnet** (macOS + WSL2 Linux), by **two independent AI agents** working
  from separate briefs — not by human security reviewers. One of them found a
  genuine ephemeral-backfill leak, which was fixed and re-verified live.
- It certifies a **July 2026 commit**. The pairing, trust, and sync code has been
  extended since and has not been re-audited live.
- **P3 was only ever exercised over loopback on a single host.** The two-machine
  pass was blocked before it started.
- There is **no CI history predating August 2026**, and no third-party review of
  any kind.

Treat Cairn as thoroughly exercised, not independently certified.

## Supported versions

Pre-alpha: only `master` is supported. There are no backports, and no security
patches for older commits. Once there is a tagged release, this section will say
which tags receive fixes.
