# Context engineering and its application to Cairn

Prepared for George, 3 November 2026. Code reviewed at `7451a2e68956458c14672d02b73cb4b1ea013249`.

Cairn already implements much of the durable memory infrastructure described in this talk. The most valuable extension is an evidence-backed review process that learns from multiple sessions, proposes changes to memory, and measures whether those changes improve later tasks. Build that above the existing event log and retrieval interfaces. First establish capture, review boundaries, and evaluation; then consider automation.

## What the talk was about

The speaker introduces herself as Lamis, a member of Anthropic’s applied AI team. Her argument is that model intelligence alone does not supply knowledge of an organisation, its codebase, its preferences, or past failures. Context engineering supplies that missing information and allows useful experience to accumulate between sessions.

Source: [the supplied video](https://x.com/neeraj_here15/status/2117203284910104609). This account uses its English captions, not independently verified audio. Timestamps below locate the relevant portions. The clip lasts approximately 31 minutes and ends during an answer. The social post’s promotional wording is not treated as evidence for the talk’s claims.

### From instructions to searchable memory

At 03:06–08:19, the speaker traces a progression from startup Markdown instructions, through memory tools and skills, to searchable collections of files. A small instruction file can steer an agent effectively, but loading every accumulated detail into every session consumes attention and context. Skills address this through progressive disclosure: show a short description first, load the detailed procedure when needed.

The broader principle is to let the memory collection grow while keeping the context presented for any particular task selective. Human-readable Markdown and ordinary search tools make that collection inspectable and portable. Agents can contribute memories while working instead of depending entirely on humans to maintain them.

### Production memory needs engineering controls

At 08:19–13:33, the talk identifies four requirements:

- Version history and provenance: preserve previous content, authorship, and the session evidence behind an update so changes can be investigated and reversed.
- Concurrency control: compare the memory state read by the editor with the state at commit time. If it changed, reread and reconsider the proposed update.
- Permissions: distinguish organisation-wide guidance from narrower working memory. An agent’s ability to maintain its scratchpad should not automatically allow it to rewrite shared policy.
- Portability: accumulated knowledge should remain usable across agent products and interfaces.

These controls matter because mistakes and malicious instructions can spread through shared memory, while older information can become incorrect.

### Why a separate review process helps

At 15:08–18:48, the speaker explains two limitations of updating memory during a task. The agent must divide attention between completing the current job and helping future jobs, and it usually sees only its own session. It cannot reliably recognise a failure repeated across many sessions.

The talk calls the separate, asynchronous review process “dreaming.” It receives an existing memory collection and selected session transcripts, looks for repeated problems, and proposes improvements. Examples include missing knowledge, recurring tool configuration errors, and preferences that should be expressed consistently. Tool calls and their metadata are part of the evidence, not just conversational text.

At 21:26–23:30, proposed changes are accompanied by example transcripts and information about how widespread the problem is. A person can accept or reject them. At 28:49–29:53, the speaker says the selected transcripts should match the memory store’s permission scope.

The claimed benefits are better future accuracy, fewer repeated mistakes, and lower task cost. The talk does not establish quantitative benefits for Cairn. Here, continual learning means improving external memory; it does not imply training the underlying model weights.

## How this maps to Cairn today

The following assessment combines implementation inspection with the repository’s stated work status. It is a design analysis, not a new runtime or security audit.

| Talk principle | Cairn evidence | Assessment |
|---|---|---|
| Selective context and progressive disclosure | [Memory provider](memory-provider.md); search, digest, peek and fetch in [MCP](../internal/mcp/mcp.go) | Strong fit: bounded discovery followed by deliberate full retrieval. Keep this contract as the corpus grows. |
| Portable, human-readable knowledge | [Ingest](../internal/ingest/ingest.go), [exports](../internal/daemon/export.go), CLI and MCP | Existing Markdown import/export and multiple interfaces already support this direction. |
| Versions and provenance | [Event model](../internal/event/envelope.go), [revision/export path](../internal/daemon/export.go) | Immutable revisions and content hashes provide a foundation. A curator also needs explicit links from each proposed change to its supporting sessions. |
| Concurrent updates | [Diff3 merge](../internal/merge/merge.go), [export reconciliation](../internal/daemon/export.go) | Existing export handling preserves the base and reconciles divergence. That is useful machinery, but is not equivalent to rejecting every stale semantic proposal. |
| Scoped write permissions | [Capability dispatch and profiles](../internal/daemon/session.go) | Operations are gated, but these coarse capability sets do not establish enterprise per-memory read/write isolation. |
| Session evidence | [Capture work order](../build/CAPTURE-PLAN.md), [C3 design](../build/CAPTURE-C3-DESIGN.md), [roadmap](../ROADMAP.md) | Transcript ingest remains planned. Existing document ingest is not proof of complete session capture. |
| Memory consolidation | [Compaction implementation](../internal/daemon/compaction.go) | Compaction renders structural counts and current-state metadata. It does not review prose, identify recurring failures, or propose improved memories. |
| Demonstrable learning | [Claims register](../eval/claims.yaml), [evaluation plan](../build/EVAL-PLAN.md) | Appropriate evaluation framework exists; key product and longitudinal claims remain unproven. |

### Important boundaries

The standard agent profile grants read, send, signal and outcome. Revision, export ingestion, retraction and structural changes require admin capability. MCP does not currently offer a general memory-editing tool. A curator therefore cannot simply be told to rewrite the memory store through the current standard MCP surface.

The permissions described in the talk are more granular than Cairn’s operation groups. A topic or view filter should not be treated as a confidentiality boundary. The repository also explicitly scopes same-OS-user confinement as accident prevention. A process that can access the same underlying files is outside the protection implied by ordinary tool restrictions. See [memory-provider capability notes](memory-provider.md#the-capability-profile-it-runs-under).

The direct `Revise` path uses the current head as its base. A long-running curator must retain the revision it actually analysed and check it again when applying a proposal. Simply passing an old proposed body into a current-head revision path would lose that semantic freshness check. Even a clean text merge can combine statements that no longer make sense together.

Finally, current revision heads establish the latest version of a particular message. They do not prove that its assertions remain true, or resolve contradictions between separate messages. The claims register explicitly identifies cross-message supersession as an open retrieval concern.

## Recommended applications

### 1. Start with reviewable proposals from existing handoffs

Run a bounded, manually initiated review over an explicitly selected set of canonical handoff notes and relevant memories. This provides a useful first experiment before automatic transcript capture is ready.

Each proposal should contain the target message and base revision/hash, proposed replacement or addition, exact evidence references, the observed failure pattern, the number of distinct affected sessions and total sessions reviewed, counterexamples, intended audience, and the expected benefit. Record accept/reject decisions with reasons. Count independent sessions rather than repeated occurrences within one transcript.

For example, if several handoffs report agents repeatedly diagnosing the same setup failure, propose one concise diagnostic procedure linked to those handoffs. A future agent should find the procedure quickly and be able to inspect the original evidence. This is an illustrative experiment, not a failure observed in this review.

Initially keep proposals as local review artifacts. Publishing every speculative proposal as ordinary memory could make an unapproved suggestion appear in subsequent retrieval or digests. Any later proposal store should have an explicit state and retrieval policy.

### 2. Finish the capture substrate before broad session review

Implement C3 using its existing work order rather than introducing a parallel transcript database. Preserve session identity, turn/chunk location, tool name, relevant outcome metadata, source time, and content hashes so patterns can be traced to evidence.

Keep opt-in source selection, redaction before durable storage, deterministic chunking, idempotent ingest, and default digest exclusion. Raw transcripts should remain low-trust evidence. Their presence in search must not silently promote them to canonical guidance.

C3’s existing acceptance tests are directly relevant: a mid-session fact becomes findable; provenance identifies its source; repeat ingest is a no-op; seeded secrets never reach stored objects; and digests remain unchanged by transcript ingestion. Address duplicate saturation and retrieval scaling as the work order specifies.

### 3. Add a narrow apply contract

A future proposal-apply operation should require the expected base revision/hash and reject a stale proposal before mutation. Return the new head so the curator or reviewer can reassess. The read/check/write must be atomic at the authoritative daemon boundary; a client-side check alone leaves a race.

Reuse durable revision events, existing conflict machinery, and history rather than modifying stored objects. Record proposal identity, supporting evidence, reviewer identity and decision, and the resulting revision. Make retries idempotent. Reversal should create a new auditable revision restoring prior content, with a freshness check of its own.

Do not grant a curation process broad admin capability just to allow edits. A narrow proposal/apply surface is a separate design change requiring its own capability rules. The first experiment can remain operator-mediated.

### 4. Make stale and contradictory knowledge visible

Allow a reviewer to propose explicit supersession between distinct messages, with supporting evidence and an effective scope. Retrieval should distinguish the superseded claim from its replacement while retaining history. Test disagreement and ambiguity as well as clear replacement.

Avoid using age alone to delete or demote important knowledge. Old design decisions may remain valid. Expiry, supersession and factual verification are different concepts, and a curator should represent them separately.

### 5. Evaluate the learning loop before scheduling it

Use the existing evaluation framework to compare three conditions on matched tasks: Cairn with unchanged memory, Cairn with ordinary end-of-session handoffs, and Cairn with reviewed consolidation. Include the existing flat-Markdown or grep baseline so extra machinery has to justify itself.

Hold model, task access, retrieval budget and evaluation conditions constant. Reserve later tasks or sessions as held-out evaluation data; the curator must not see their answers. Measure task success, repeated-error rate, time to useful context, task tokens, curation tokens, review time, stale-memory use and unsupported guidance introduced. Report total cost, including failed or rejected proposals.

Adversarial cases should include an injected tool result asking the curator to change policy, conflicting sessions, a stale proposal, duplicate evidence, and evidence outside the permitted review set. Success requires correct boundaries as well as improved task performance. Do not call proposal acceptance or successful retrieval proof of learning.

## Suggested delivery order

1. Establish a baseline and conduct a small manual proposal pilot over existing handoffs. Deliver a review packet and held-out task results.
2. Complete C3 and validate privacy, provenance, idempotence and digest exclusion.
3. Design proposal states, expected-base application, evidence links and narrow authority. Test concurrent edits and retry safety before enabling application.
4. Add cross-message supersession and measure stale-answer behaviour.
5. Schedule bounded review only if the experiments show enough benefit to justify its compute and review cost.

The best near-term investment is the first pilot plus the already-planned capture and evaluation work. Cairn’s signed history and selective retrieval give it a credible foundation for this workflow. Its next differentiator would be showing that evidence-backed memory changes actually improve subsequent work, while preserving the provenance and control the system already values.
