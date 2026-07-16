package projection_test

// FIX-P2H5 (spec §9.2): the salience reference graph was replies-only, so it
// only reflected threading. It now also counts ONWARD-ATTACHMENT edges (a later
// message that re-attaches a blob first introduced by another message
// references that origin message). Reply edges are counted at message_id
// granularity, so a reply to a since-superseded revision still counts — the
// supersedes edge is honored implicitly.

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/testutil"
)

func TestP2H5ReferenceInDegreeRepliesPlusOnwardAttachment(t *testing.T) {
	p := freshProjection(t)
	defer p.Close()
	c, genv, grec := testutil.NewChain(t)
	if err := p.Apply(genv, grec); err != nil {
		t.Fatalf("genesis: %v", err)
	}

	pub := func(id, rev, body string) {
		t.Helper()
		env, rec := c.Event(t, "message.publish", "message", id, map[string]any{
			"message_id": id, "revision_id": rev, "body_hash": "h" + id,
			"body_bytes": body, "body_len": len(body), "body_mime": "text/markdown",
			"text_class": "canonical", "declared_priority": 0,
		}, "operator")
		if err := p.Apply(env, rec); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	reply := func(id, rev, parent, body string) {
		t.Helper()
		env, rec := c.Event(t, "message.reply", "message", id, map[string]any{
			"message_id": id, "revision_id": rev, "reply_to_message_id": parent,
			"body_hash": "h" + id, "body_bytes": body, "body_len": len(body),
			"body_mime": "text/markdown", "text_class": "canonical", "declared_priority": 0,
		}, "operator")
		if err := p.Apply(env, rec); err != nil {
			t.Fatalf("reply %s: %v", id, err)
		}
	}
	pubWithBlob := func(id, rev, body, blob string) {
		t.Helper()
		env, rec := c.Event(t, "message.publish", "message", id, map[string]any{
			"message_id": id, "revision_id": rev, "body_hash": "h" + id,
			"body_bytes": body, "body_len": len(body), "body_mime": "text/markdown",
			"text_class": "canonical", "declared_priority": 0,
			"attachments": []map[string]any{{"object_hash": blob, "byte_len": 10, "mime": "image/png", "filename": "a.png"}},
		}, "operator")
		if err := p.Apply(env, rec); err != nil {
			t.Fatalf("publish+blob %s: %v", id, err)
		}
	}

	const (
		mRoot = "0190a1b2-c3d4-7e5f-8901-00000000e001"
		mR1   = "0190a1b2-c3d4-7e5f-8901-00000000e002"
		mR2   = "0190a1b2-c3d4-7e5f-8901-00000000e003"
		mBlob = "0190a1b2-c3d4-7e5f-8901-00000000e004" // origin of a blob
		mOnw  = "0190a1b2-c3d4-7e5f-8901-00000000e005" // re-attaches the same blob
	)
	// derive a DISTINCT revision id per message (swap the "e00" block for "f00"
	// so the trailing discriminator survives — a naive last-char flip would
	// collide every revision id).
	rev := func(s string) string { return strings.Replace(s, "e00", "f00", 1) }

	// root gets two replies → reply in-degree 2
	pub(mRoot, rev(mRoot), "root")
	reply(mR1, rev(mR1), mRoot, "reply one")
	reply(mR2, rev(mR2), mRoot, "reply two")

	// mBlob introduces a blob; mOnw re-attaches it → onward in-degree 1 for mBlob
	const sharedBlob = "blakeblobhashaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pubWithBlob(mBlob, rev(mBlob), "has the blob", sharedBlob)
	pubWithBlob(mOnw, rev(mOnw), "passes it onward", sharedBlob)

	deg, err := p.ReferenceInDegree()
	if err != nil {
		t.Fatal(err)
	}
	if deg[mRoot] != 2 {
		t.Fatalf("reply in-degree for root = %d, want 2 (%v)", deg[mRoot], deg)
	}
	if deg[mBlob] != 1 {
		t.Fatalf("onward-attachment in-degree for blob origin = %d, want 1 (%v)", deg[mBlob], deg)
	}
	// the onward attacher and the replies themselves have no inbound edges
	if deg[mOnw] != 0 || deg[mR1] != 0 {
		t.Fatalf("unexpected in-degree on leaf messages: onw=%d r1=%d", deg[mOnw], deg[mR1])
	}
}
