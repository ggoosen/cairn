package outbox_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/outbox"
)

const reqID = "0190a1b2-c3d4-7e5f-8901-2345678900ff"

func setup(t *testing.T) (dir string, d *daemon.Daemon, w *outbox.Watcher) {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	dir = filepath.Join(t.TempDir(), "cairn")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	w, err = outbox.NewWatcher(fsx.OS{}, d, dir, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	return dir, d, w
}

func outboxDir(dir string) string { return filepath.Join(dir, "views", "agent-a", "outbox") }

// dropBundle writes a bundle atomically: temp dir → rename to <id>.ready.
func dropBundle(t *testing.T, dir, requestID string, request map[string]any, files map[string]string) {
	t.Helper()
	ob := outboxDir(dir)
	tmp := filepath.Join(ob, requestID+".tmp-drop")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(request)
	if err := os.WriteFile(filepath.Join(tmp, "request.json"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(tmp, filepath.Join(ob, requestID+".ready")); err != nil {
		t.Fatal(err)
	}
}

func readReceipt(t *testing.T, dir, requestID string) []byte {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join(outboxDir(dir), requestID+".receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

// M4 acceptance: duplicate bundle returns the identical receipt and creates
// no new events.
func TestDuplicateBundleIdenticalReceipt(t *testing.T) {
	dir, d, w := setup(t)

	dropBundle(t, dir, reqID, map[string]any{
		"request_id": reqID, "op": "publish", "body": "bundle body one",
		"text_class": "canonical", "declared_priority": 1,
	}, nil)
	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}
	first := readReceipt(t, dir, reqID)
	if !strings.Contains(string(first), `"status": "accepted"`) {
		t.Fatalf("bad receipt: %s", first)
	}
	evs, err := d.Projection().EventsByCorrelation(reqID)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %v %v", evs, err)
	}

	// duplicate drop: same request_id
	dropBundle(t, dir, reqID, map[string]any{
		"request_id": reqID, "op": "publish", "body": "bundle body one",
		"text_class": "canonical", "declared_priority": 1,
	}, nil)
	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}
	second := readReceipt(t, dir, reqID)
	if string(first) != string(second) {
		t.Fatalf("duplicate receipt differs:\n%s\nvs\n%s", first, second)
	}
	evs, _ = d.Projection().EventsByCorrelation(reqID)
	if len(evs) != 1 {
		t.Fatalf("duplicate bundle created events: %d", len(evs))
	}
	hits, _ := d.Projection().SearchLexical("bundle", 10, false)
	if len(hits) != 1 {
		t.Fatalf("duplicate created a message: %d hits", len(hits))
	}
}

// M4 acceptance: crash during receipt write — events durable, receipt
// missing — the retry regenerates the IDENTICAL receipt.
func TestCrashDuringReceiptRetryIdentical(t *testing.T) {
	dir, _, w := setup(t)

	dropBundle(t, dir, reqID, map[string]any{
		"request_id": reqID, "op": "publish", "body": "receipt crash body",
	}, nil)
	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}
	original := readReceipt(t, dir, reqID)

	// simulate the crash window: events are durable but the receipt never
	// became visible; the bundle is re-dropped (retry per rulings §8)
	if err := os.Remove(filepath.Join(outboxDir(dir), reqID+".receipt.json")); err != nil {
		t.Fatal(err)
	}
	dropBundle(t, dir, reqID, map[string]any{
		"request_id": reqID, "op": "publish", "body": "receipt crash body",
	}, nil)
	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}
	regenerated := readReceipt(t, dir, reqID)
	if string(original) != string(regenerated) {
		t.Fatalf("regenerated receipt differs:\n%s\nvs\n%s", original, regenerated)
	}
}

// body_file variant + reply op.
func TestBundleBodyFileAndReply(t *testing.T) {
	dir, d, w := setup(t)

	dropBundle(t, dir, reqID, map[string]any{
		"request_id": reqID, "op": "publish", "body_file": "body.md",
	}, map[string]string{"body.md": "body from file"})
	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		MessageID string `json:"message_id"`
	}
	json.Unmarshal(readReceipt(t, dir, reqID), &receipt)

	replyID := "0190a1b2-c3d4-7e5f-8901-2345678900fe"
	dropBundle(t, dir, replyID, map[string]any{
		"request_id": replyID, "op": "reply", "body": "a bundle reply",
		"reply_to_message_id": receipt.MessageID,
	}, nil)
	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		MessageID string `json:"message_id"`
	}
	json.Unmarshal(readReceipt(t, dir, replyID), &rep)
	info, err := d.Projection().MessageInfo(rep.MessageID)
	if err != nil || info.ReplyToMessageID != receipt.MessageID {
		t.Fatalf("reply threading: %+v %v", info, err)
	}
}

// Invalid bundles land in rejected/ with a structured error and no receipt.
func TestInvalidBundleRejected(t *testing.T) {
	dir, _, w := setup(t)

	badID := "0190a1b2-c3d4-7e5f-8901-2345678900fd"
	dropBundle(t, dir, badID, map[string]any{
		"request_id": badID, "op": "publish", // no body at all
	}, nil)
	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outboxDir(dir), badID+".receipt.json")); err == nil {
		t.Fatal("rejected bundle got a receipt")
	}
	blob, err := os.ReadFile(filepath.Join(outboxDir(dir), "rejected", badID, "error.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"rejected"`) || !strings.Contains(string(blob), "empty body") {
		t.Fatalf("unstructured rejection: %s", blob)
	}

	// mismatched request_id inside request.json
	mmID := "0190a1b2-c3d4-7e5f-8901-2345678900fc"
	dropBundle(t, dir, mmID, map[string]any{
		"request_id": "0190a1b2-c3d4-7e5f-8901-000000000000", "op": "publish", "body": "x",
	}, nil)
	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outboxDir(dir), "rejected", mmID, "error.json")); err != nil {
		t.Fatal("mismatched request_id not rejected")
	}
}

// .md convenience shorthand: front-matter publish + plain publish; receipt
// written; file consumed; unknown front-matter fields rejected (strict).
func TestMarkdownShorthand(t *testing.T) {
	dir, d, w := setup(t)
	ob := outboxDir(dir)
	os.MkdirAll(ob, 0o700)

	// plain publish, no front-matter
	if err := os.WriteFile(filepath.Join(ob, "note.md"), []byte("plain markdown note"), 0o644); err != nil {
		t.Fatal(err)
	}
	// front-matter publish
	fm := "---\naction: publish\ntext_class: ephemeral\ndeclared_priority: 2\n---\nscratch content here"
	if err := os.WriteFile(filepath.Join(ob, "scratch.md"), []byte(fm), 0o644); err != nil {
		t.Fatal(err)
	}
	// unknown front-matter key → strict rejection
	bad := "---\naction: publish\npriority_rewrite: 3\n---\nbody"
	if err := os.WriteFile(filepath.Join(ob, "bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"note.md", "scratch.md"} {
		if _, err := os.Stat(filepath.Join(ob, name)); err == nil {
			t.Fatalf("%s not consumed", name)
		}
		blob, err := os.ReadFile(filepath.Join(ob, name+".receipt.json"))
		if err != nil {
			t.Fatalf("%s receipt: %v", name, err)
		}
		if !strings.Contains(string(blob), `"accepted"`) {
			t.Fatalf("%s receipt: %s", name, blob)
		}
	}
	if _, err := os.Stat(filepath.Join(ob, "rejected", "bad.md", "error.json")); err != nil {
		t.Fatal("strict front-matter violation not rejected")
	}

	hits, _ := d.Projection().SearchLexical("scratch", 10, false)
	if len(hits) != 1 || hits[0].TextClass != "ephemeral" {
		t.Fatalf("front-matter class not honored: %+v", hits)
	}
}

// The operator override is impossible via the outbox: an oversized canonical
// body gets downgraded even if the bundle author wishes otherwise.
func TestOutboxCannotForceCanonical(t *testing.T) {
	dir, _, w := setup(t)
	big := strings.Repeat("x", (1<<20)+1)
	id := "0190a1b2-c3d4-7e5f-8901-2345678900fb"
	dropBundle(t, dir, id, map[string]any{
		"request_id": id, "op": "publish", "body": big, "text_class": "canonical",
	}, nil)
	if err := w.ProcessOnce(); err != nil {
		t.Fatal(err)
	}
	blob := readReceipt(t, dir, id)
	if !strings.Contains(string(blob), `"downgraded": true`) || !strings.Contains(string(blob), `"text_class": "ephemeral"`) {
		t.Fatalf("outbox forced canonical: %s", blob)
	}
}
