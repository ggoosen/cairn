package daemon_test

// FIX-G1 (R42/R43): ephemeral bodies must never be inlined into the publish
// event. Inlining made ephemeral content replicate as chain data to every full
// node — searchable from the inline copy on a peer that was offline at send
// time — and made that peer's `cairn doctor` FAIL forever ("referenced object
// missing (class ephemeral)") until expiry. These regression tests encode
// Claude's live-run repro; they FAIL against the pre-G1 code (inline present)
// and pass once ephemeral is object-only + doctor treats a missing ephemeral
// object as informational on every node.

import (
	"errors"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
)

// TestG1EphemeralNotInlinedTwoNode is the network repro: A sends an ephemeral
// message while B has not synced; B then pulls. The ephemeral BODY object is
// withheld (a PULL never backfills ephemeral), so on B: (a) fetch fails, (b)
// search finds nothing, (c) doctor is clean (exit 0). The origin A still holds
// and indexes its own ephemeral (the object is local).
func TestG1EphemeralNotInlinedTwoNode(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	defer func() { cancelA(); dA.Close() }()
	dB, cancelB, _ := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB(); dB.Close() }()

	// A publishes one small ephemeral (well under the 64 KiB inline limit) plus
	// a canonical control, while B is silent (offline-at-send).
	eph, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzephemeralcanary secret scratch note", TextClass: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}
	if eph.TextClass != object.ClassEphemeral {
		t.Fatalf("control message not ephemeral (got %q) — policy downgraded it", eph.TextClass)
	}
	if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzcanonicalcanary durable decision"}); err != nil {
		t.Fatal(err)
	}

	// origin A indexes its own ephemeral (it holds the object locally)
	if got := searchCount(t, dA, "zzephemeralcanary"); got != 1 {
		t.Fatalf("origin A should index its own ephemeral (object local): got %d/1", got)
	}

	// B pulls A's origin (genesis + device.add + both messages, no ephemeral body)
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("B pull from A: %v\nA:\n%s\nB:\n%s", err, warnA.String(), warnB.String())
	}
	// sanity: the canonical control DID cross and is searchable on B
	if got := searchCount(t, dB, "zzcanonicalcanary"); got != 1 {
		t.Fatalf("canonical control did not replicate to B: got %d/1", got)
	}

	// (b) the ephemeral body is NOT searchable on the non-origin node
	if got := searchCount(t, dB, "zzephemeralcanary"); got != 0 {
		t.Fatalf("R42 VIOLATION: ephemeral body searchable on non-origin B (got %d hits — inline copy leaked)", got)
	}

	// (a) fetch on B returns the TYPED not-delivered result (K1/R50 upgraded
	// this from an opaque error) — and never the body
	fr, err := dB.Fetch(eph.MessageID, "operator")
	if err != nil {
		t.Fatalf("R50: fetch of a withheld ephemeral on B should return the typed result, got error: %v", err)
	}
	if !fr.NotDelivered || fr.BodyPath != "" {
		t.Fatalf("R50: fetch of a withheld ephemeral must be typed not-delivered with no body file: %+v", fr)
	}

	// (c) deep doctor on B is CLEAN — a missing ephemeral object is
	// informational on every node (R43), never a problem/exit 1.
	probs, _, err := daemon.DeepDoctor(fsx.OS{}, p.dirB, projection.DBPath(p.dirB), time.Now())
	if err != nil {
		t.Fatalf("deep doctor B: %v", err)
	}
	if len(probs) != 0 {
		t.Fatalf("R43 VIOLATION: doctor on B not clean after withheld ephemeral: %v", probs)
	}
}

// TestG1EphemeralInlineRejectedPreAck covers R42 test (e): validation rejects
// an ephemeral publish carrying inline body_bytes BEFORE acknowledgement, and
// accepts an ephemeral event that (correctly) omits them / a canonical event
// that carries them.
func TestG1EphemeralInlineRejectedPreAck(t *testing.T) {
	mk := func(class, inline string) *event.Envelope {
		payload := []byte(`{"text_class":"` + class + `"` +
			func() string {
				if inline == "" {
					return ""
				}
				return `,"body_bytes":"` + inline + `"`
			}() + `}`)
		return &event.Envelope{EventType: "message.publish", Payload: payload}
	}
	// ephemeral + inline → rejected
	if err := daemon.ValidateNoInlineEphemeral(mk("ephemeral", "leaked scratch")); err == nil {
		t.Fatal("R42(e): ephemeral publish carrying body_bytes must be rejected pre-ack")
	}
	// ephemeral without inline → accepted (the correct object-only form)
	if err := daemon.ValidateNoInlineEphemeral(mk("ephemeral", "")); err != nil {
		t.Fatalf("object-only ephemeral wrongly rejected: %v", err)
	}
	// canonical + inline → accepted (inline optimization still allowed)
	if err := daemon.ValidateNoInlineEphemeral(mk("canonical", "durable body")); err != nil {
		t.Fatalf("canonical inline wrongly rejected: %v", err)
	}
	_ = errors.New // keep import stable if assertions change
}
