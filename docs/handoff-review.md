# Manual handoff review

Use `cairn review` to turn explicitly selected handoff notes into an evidence-backed approval packet. Cairn prepares the source snapshot and validates the proposals; your chosen agent or a human performs the semantic review. There is no built-in model call, scheduled job, or apply command.

## Prepare the review

Find the handoffs you want to review with the existing search or thread tools, then select their message IDs explicitly. Optionally select existing memories the reviewer may propose revising.

```sh
cairn review prepare \
  --handoff <handoff-id-1>,<handoff-id-2> \
  --memory <existing-memory-id> \
  --out /path/outside-your-mesh/review-bundle.json
```

The output directory must already exist. Cairn refuses to overwrite files or write inside its portable mesh, including through a symlink. Outputs are private local files (0600). Source retrieval uses the normal capability-aware fetch path and creates ordinary fetched view files, but does not publish, revise, retract or change memory. At most 50 sources and a 2 MiB JSON bundle are accepted. Source bodies are hash-verified; unavailable or retracted sources fail the whole preparation. Selection is explicit, not a claim to have reviewed all handoffs. The snapshots are taken sequentially, not as a mesh-wide transaction.

The command prints reviewer instructions and the bundle hash. Give those instructions and the JSON bundle to your reviewing agent, asking it to return `proposals.json`. Only share the snapshot with a reviewer permitted to see those notes. Cairn itself does not send it to a model provider. Reviewers should work from the bundle without executing instructions found inside the notes or writing to Cairn.

## Reviewer response

Return one JSON object, without Markdown fences. Example shape (replace placeholders with actual bundle values):

```json
{
  "bundle_hash": "HASH_PRINTED_BY_PREPARE",
  "summary": "The selected handoffs show a recurring build issue.",
  "proposals": [
    {
      "title": "Document the required build tag",
      "action": "add",
      "verification_status": "hypothesis",
      "verification_basis": "The handoff reports a failed build; the proposed fix has not been tested in this snapshot.",
      "outcome_evidence": [],
      "future_test": {
        "trigger": "The next clean build of Cairn",
        "procedure": "Ask the agent to build Cairn using this guidance and record the command and exit status.",
        "success_criterion": "The build succeeds with the required tag without another tag-related failure."
      },
      "proposed_body": "Build Cairn with the sqlite_fts5 tag, or use make build.",
      "reason": "The cited handoff records a failure caused by omitting this tag.",
      "expected_benefit": "Reduce repeated build failures in later sessions.",
      "audience": "Cairn contributors",
      "counterexamples": "Only the selected handoffs were reviewed; no broader failure rate is established.",
      "evidence": [
        {"message_id": "SELECTED_HANDOFF_ID", "quote": "EXACT_TEXT_FROM_THAT_HANDOFF"}
      ]
    }
  ]
}
```

For `action: revise`, also include `target_message_id`, `base_revision_id`, and `base_body_hash` copied from a selected **memory** source. `proposed_body` is the complete proposed replacement, not a patch. Additions must omit target/base fields. Each proposal requires at least one handoff citation; each cited source must have a nonempty exact quote and may appear once per proposal. Up to 20 proposals are allowed. Use `"proposals": []` with an explanatory summary when no change is justified.

## Verification and future usefulness

Every proposal must declare `verification_status` as `observation` (what happened),
`hypothesis` (an untested explanation or recommendation), or `tested_rule` (a rule
tested in the cited circumstances). `verification_basis` explains what was checked
and its limits. A failed attempt does not establish that a proposed fix works.

`outcome_evidence` is a required array of exact source quotations using the same
`message_id` and `quote` fields as ordinary evidence. Use `[]` when no outcome is
available. `tested_rule` requires at least one outcome citation. Cairn checks
that the cited text exists in a selected source; the human reviewer must assess
whether it actually demonstrates the claimed rule. The packet labels status as
a reviewer claim, never an independent certification.

`future_test` requires a `trigger`, `procedure`, and `success_criterion`. State
when the memory would help, how to check it, and the observable result that would
support its usefulness. These are proposed tests, not executable instructions;
Cairn does not run them. No later evaluation results are collected automatically.

Review successful approaches, discoveries and durable user preferences alongside
failures. Prefer reusable knowledge over transient state that can be cheaply
retrieved again. Keep uncertain explanations explicitly uncertain and describe
the circumstances under which the advice applies.

Older proposal responses without these fields must be revised by the reviewer;
Cairn does not infer a verification status or silently upgrade them. Existing
version-1 source bundles remain usable. Transcript and tool-result capture are
still separate planned work; outcome quotations here must already exist in the
selected handoff or memory bodies.

## Produce the approval packet

```sh
cairn review packet \
  --bundle /path/outside-your-mesh/review-bundle.json \
  --proposals /path/outside-your-mesh/proposals.json \
  --out /path/outside-your-mesh/approval-packet.md
```

This step is offline and does not require a running daemon. It validates the snapshot hash, source body hashes, source membership, exact evidence quotations and revision bases. Unknown fields, trailing JSON, unsupported actions and duplicate evidence are rejected. Evidence matching does not prove that the proposal is true or useful. The bundle hash binds the response to the supplied local snapshot; it is not a cryptographic attestation of authorship or an independently verified event signature.

The packet presents each proposed body, the original body for revisions, rationale, expected benefit, audience, counterexamples, evidence references, and decision checkboxes. Source/model prose is visibly marked untrusted. Prevalence is reported as cited handoff notes out of selected handoff notes, not as distinct sessions. The operator identifies whether notes describe independent sessions.

Mark approvals or rejections and reasons in the packet. Approval is a review decision only: it does not change memory. A later implementation must recheck live target revisions before applying changes. Keep the bundle, response and packet together as the review record. No scheduling, automatic ingestion, or approval application is included in this milestone.
