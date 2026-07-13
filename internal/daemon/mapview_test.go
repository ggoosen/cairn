package daemon_test

// P2-5: local maps + rollups. Publishing across topics and a thread and then
// generating the map yields views/<agent>/map.md with the rollup totals, the
// topics by count, and the top threads.

import (
	"os"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

func TestP25MapAndRollup(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// two messages under topic "alpha", one under "beta", plus a thread.
	root, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "root note", Topics: []string{"alpha"}, AutoCreateTopics: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "second alpha", Topics: []string{"alpha"}, AutoCreateTopics: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "beta note", Topics: []string{"beta"}, AutoCreateTopics: true}); err != nil {
		t.Fatal(err)
	}
	// two replies to root → a thread of size 2
	for i := 0; i < 2; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "reply", ReplyToMessageID: root.MessageID}); err != nil {
			t.Fatal(err)
		}
	}

	res, err := d.GenerateMap("operator")
	if err != nil {
		t.Fatal(err)
	}
	// 5 messages total, 2 topics, 0 pins
	if res.TotalMessages != 5 || res.TotalTopics != 2 {
		t.Fatalf("rollup wrong: %+v", res)
	}

	blob, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(blob)
	for _, want := range []string{
		"# cairn map",
		"## rollup",
		"messages: 5",
		"topics: 2",
		"## topics",
		"alpha — 2 message(s)",
		"beta — 1 message(s)",
		"## top threads",
		root.MessageID,
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("map.md missing %q:\n%s", want, md)
		}
	}
	// alpha (2) must be listed before beta (1) — sorted by count desc
	if strings.Index(md, "alpha —") > strings.Index(md, "beta —") {
		t.Fatalf("topics not ordered by count desc:\n%s", md)
	}
}
