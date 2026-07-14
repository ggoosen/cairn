package daemon_test

// FIX-H1 (R42/R46): the ephemeral-inline-body invariant, swept across EVERY
// write path. G1 closed the publish/reply path; the N9 audit found the
// revise/merge path (appendRevision) still inlined ephemeral bodies with only a
// size check — reintroducing the un-purgeable inline copy R42 exists to
// prevent. These tests encode the auditor's finding: an ephemeral body must
// never reach `body_bytes` on ANY path, and the pre-ack guard rejects one that
// somehow does.
//
// Enumerated body_bytes write paths (R46 sweep):
//   1. daemon.go   message.publish/reply  — GATED (G1) + ValidateNoInlineEphemeral
//   2. export.go   message.revise_body    — the H1 hole, gated here
//   3. fork_resolve.go message.publish reissue — GATED (G1/G7)

import (
	"encoding/json"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
)

// inlineBodyBytesInLog walks the active origin's log and returns, for the given
// message id, whether ANY publish/reply/revise_body event carries inline
// body_bytes. Independent of the projection — it inspects the durable event.
func inlineBodyBytesInLog(t *testing.T, dir, messageID string) bool {
	t.Helper()
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	_, err = cairnlog.Walk(fsx.OS{}, dir,
		cairnlog.Origin{DeviceID: loaded.Device.DeviceID, Generation: loaded.Device.OriginGeneration},
		identity.NewChainVerifier().Verify,
		func(env *event.Envelope, _ []byte) error {
			switch env.EventType {
			case "message.publish", "message.reply":
				var pl struct {
					MessageID string `json:"message_id"`
					BodyBytes string `json:"body_bytes"`
				}
				if json.Unmarshal(env.Payload, &pl) == nil && pl.MessageID == messageID && pl.BodyBytes != "" {
					found = true
				}
			case "message.revise_body":
				var pl struct {
					MessageID string `json:"message_id"`
					Revisions []struct {
						BodyBytes string `json:"body_bytes"`
					} `json:"revisions"`
				}
				if json.Unmarshal(env.Payload, &pl) == nil && pl.MessageID == messageID {
					for _, r := range pl.Revisions {
						if r.BodyBytes != "" {
							found = true
						}
					}
				}
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// (a) revise an ephemeral message → no body_bytes in the revise event.
// (c) after housekeeping (TTL), no body bytes survive in ANY event for it.
func TestH1EphemeralReviseNotInlined(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "ephemeral scratch v1", TextClass: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}
	if pub.TextClass != object.ClassEphemeral {
		t.Fatalf("control not ephemeral (got %q)", pub.TextClass)
	}
	if inlineBodyBytesInLog(t, dir, pub.MessageID) {
		t.Fatal("publish already inlined the ephemeral body (G1 regression)")
	}

	// revise the ephemeral message (single-revision path)
	if _, err := d.Revise(pub.MessageID, "ephemeral scratch v2 revised body"); err != nil {
		t.Fatalf("revise ephemeral: %v", err)
	}
	if inlineBodyBytesInLog(t, dir, pub.MessageID) {
		t.Fatal("R42/H1 VIOLATION: revise inlined the ephemeral body — un-purgeable copy reintroduced")
	}

	// (c) run a housekeeping sweep; the events never carried inline bytes, so
	// after TTL purge nothing of the ephemeral body survives anywhere.
	if _, err := d.Housekeep(); err != nil {
		t.Fatalf("housekeep: %v", err)
	}
	if inlineBodyBytesInLog(t, dir, pub.MessageID) {
		t.Fatal("R42/H1: ephemeral body bytes present in the log after housekeeping")
	}
}

// (e) do not over-correct: a CANONICAL message's revision still inlines
// (≤64 KiB) — the recovery-acceleration optimization is intact.
func TestH1CanonicalReviseStillInlines(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "durable decision v1"})
	if err != nil {
		t.Fatal(err)
	}
	if pub.TextClass == object.ClassEphemeral {
		t.Fatalf("control unexpectedly ephemeral")
	}
	if _, err := d.Revise(pub.MessageID, "durable decision v2 with more detail"); err != nil {
		t.Fatalf("revise canonical: %v", err)
	}
	if !inlineBodyBytesInLog(t, dir, pub.MessageID) {
		t.Fatal("canonical revise should still inline body_bytes (over-corrected)")
	}
}

// (b) the merge path (2-revision revise_body) also never inlines an ephemeral
// body. Reuses the stale-edit clean-merge dance on an ephemeral message.
func TestH1EphemeralMergeNotInlined(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "alpha\nbeta\ngamma\n", TextClass: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}
	if pub.TextClass != object.ClassEphemeral {
		t.Fatalf("control not ephemeral (got %q)", pub.TextClass)
	}
	baseHead := pub.RevisionID

	// move the head once (edit line 1)
	p2, err := d.Export(pub.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	editExportBody(t, p2, "alpha!\nbeta\ngamma\n")
	if _, err := d.IngestExport(p2); err != nil {
		t.Fatalf("intervening ingest: %v", err)
	}

	// stale edit on line 3 against the original base → clean 2-revision merge
	stale, err := renderStaleExport(t, dir, d, pub.MessageID, baseHead)
	if err != nil {
		t.Fatal(err)
	}
	editExportBody(t, stale, "alpha\nbeta\ngamma EDITED\n")
	res, err := d.IngestExport(stale)
	if err != nil {
		t.Fatalf("merge ingest: %v", err)
	}
	if res.Outcome != "merged" || len(res.NewRevisionIDs) != 2 {
		t.Fatalf("expected 2-revision merge, got %+v", res)
	}
	if inlineBodyBytesInLog(t, dir, pub.MessageID) {
		t.Fatal("R42/H1 VIOLATION: merge path inlined an ephemeral body")
	}
}

// (d) an ephemeral revise_body event carrying body_bytes is rejected pre-ack by
// the structural guard, regardless of which code path built it.
func TestH1RevisionInlineRejectedPreAck(t *testing.T) {
	mk := func(inline bool) *event.Envelope {
		body := `{"message_id":"m","revisions":[{"revision_id":"r","body_hash":"h"`
		if inline {
			body += `,"body_bytes":"leaked ephemeral revision"`
		}
		body += `}]}`
		return &event.Envelope{EventType: "message.revise_body", Payload: []byte(body)}
	}
	// ephemeral message + inline revision → rejected
	if err := daemon.ValidateNoInlineEphemeralRevisions(mk(true), object.ClassEphemeral); err == nil {
		t.Fatal("R42/H1(d): ephemeral revise carrying body_bytes must be rejected pre-ack")
	}
	// ephemeral message + object-only revision → accepted
	if err := daemon.ValidateNoInlineEphemeralRevisions(mk(false), object.ClassEphemeral); err != nil {
		t.Fatalf("object-only ephemeral revision wrongly rejected: %v", err)
	}
	// canonical message + inline revision → accepted (optimization retained)
	if err := daemon.ValidateNoInlineEphemeralRevisions(mk(true), object.ClassCanonical); err != nil {
		t.Fatalf("canonical inline revision wrongly rejected: %v", err)
	}
}
