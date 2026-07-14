package daemon_test

// P2-2: local salience (spec §9.2) end to end. A message that is shown, found
// across a task, replied to, and operator-signalled must outrank one that is
// merely shown and ignored — and every score stays bounded in [0,1].

import (
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
)

func TestP22SalienceCombinesDemandRefAndSignals(t *testing.T) {
	dir := initCairn(t)
	// Pin the clock: FIX-H4 signal decay makes salience time-dependent, so two
	// SalienceScores calls at different real instants would drift in the last
	// ULPs. A fixed clock keeps the batch/SalienceFor equality exact.
	fixed := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	d, err := daemon.Start(daemon.Options{Dir: dir, Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	hot, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "salience zzhot decision that gets used a lot"})
	if err != nil {
		t.Fatal(err)
	}
	cold, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "salience zzcold note nobody comes back to"})
	if err != nil {
		t.Fatal(err)
	}

	// impressions on both across several tasks, hot is "found" each time.
	for i, task := range []string{"t1", "t2", "t3"} {
		out, err := d.Search(daemon.SearchOptions{Query: "salience", TaskID: task})
		if err != nil {
			t.Fatalf("search %d: %v", i, err)
		}
		if err := d.Outcome(out.InteractionID, "found", hot.MessageID); err != nil {
			t.Fatalf("outcome %d: %v", i, err)
		}
	}
	// hot gets a reply (reference in-degree) and an operator signal.
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "building on it", ReplyToMessageID: hot.MessageID}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Signal(hot.MessageID, "priority_confirm", 3, "operator"); err != nil {
		t.Fatal(err)
	}

	scores, err := d.SalienceScores()
	if err != nil {
		t.Fatal(err)
	}
	for id, s := range scores {
		if s < 0 || s > 1 {
			t.Fatalf("salience %s out of [0,1]: %v", id, s)
		}
	}
	sHot, sCold := scores[hot.MessageID], scores[cold.MessageID]
	if sHot <= sCold {
		t.Fatalf("hot salience %v should exceed cold %v (demand+ref+signal vs ignored)", sHot, sCold)
	}
	// SalienceFor agrees with the batch map.
	if got, _ := d.SalienceFor(hot.MessageID); got != sHot {
		t.Fatalf("SalienceFor(hot)=%v != batch %v", got, sHot)
	}
}
