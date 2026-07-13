package daemon_test

// P2-6: compaction views. A corpus with a revised + a retracted message
// collapses to its current effective state; compaction.md reports the ratio and
// what was compacted away (superseded revisions, retracted messages).

import (
	"os"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

func TestP26CompactionView(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	live, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "kept message"})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "to be retracted"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SimpleEvent("message.retract", "message", gone.MessageID,
		map[string]any{"message_id": gone.MessageID, "reason": "obsolete"},
		daemon.PublishRequest{Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	_ = live

	res, err := d.GenerateCompaction("operator")
	if err != nil {
		t.Fatal(err)
	}
	if res.Events == 0 || res.LiveEntities == 0 {
		t.Fatalf("empty compaction stats: %+v", res)
	}

	blob, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(blob)
	for _, want := range []string{
		"# cairn compaction view",
		"## headline",
		"compaction ratio",
		"live messages: 1",
		"## compacted away",
		"retracted messages: 1",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("compaction.md missing %q:\n%s", want, md)
		}
	}
}
