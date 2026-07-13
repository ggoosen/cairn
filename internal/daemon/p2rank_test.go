package daemon_test

// P2-3: the full additive ranking profile wired end to end. With P2 enabled, a
// salient message contributes its S/I/N terms — visible in the persisted
// why_ranked explanation (profile=search-P2, S/I/N present) — while P0 remains
// the default. The pure ordering guarantees live in internal/rank tests.

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
)

func TestP23FullProfileWiredAndExplained(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetRankProfileP2ForTest(true)

	hot, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "salience zzquery frequently used decision"})
	if err != nil {
		t.Fatal(err)
	}
	cold, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "salience zzquery ignored note"})
	if err != nil {
		t.Fatal(err)
	}

	// boost hot: found across three tasks, a reply, and an operator signal.
	for _, task := range []string{"t1", "t2", "t3"} {
		out, err := d.Search(daemon.SearchOptions{Query: "salience", TaskID: task})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Outcome(out.InteractionID, "found", hot.MessageID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "building on it", ReplyToMessageID: hot.MessageID}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Signal(hot.MessageID, "priority_confirm", 3, "operator"); err != nil {
		t.Fatal(err)
	}

	// P2Inputs reflects the boost.
	p2, err := d.P2Inputs()
	if err != nil {
		t.Fatal(err)
	}
	if p2[hot.MessageID].Salience <= p2[cold.MessageID].Salience {
		t.Fatalf("hot salience %v should exceed cold %v", p2[hot.MessageID].Salience, p2[cold.MessageID].Salience)
	}

	// a P2 search persists an explanation carrying the S/I/N terms.
	out, err := d.Search(daemon.SearchOptions{Query: "zzquery", TaskID: "final"})
	if err != nil {
		t.Fatal(err)
	}
	profile, cjson, _, err := d.Projection().Explanation(out.InteractionID, hot.MessageID)
	if err != nil {
		t.Fatalf("explanation for hot: %v", err)
	}
	if profile != "search-P2" {
		t.Fatalf("profile = %q, want search-P2", profile)
	}
	for _, want := range []string{`"S"`, `"I"`, `"N"`} {
		if !strings.Contains(cjson, want) {
			t.Fatalf("P2 explanation missing %s component:\n%s", want, cjson)
		}
	}
}
