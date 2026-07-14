package daemon_test

// FIX-H4 (§9.2): the salience gaming surface. Operator signals are additive with
// slow decay (never a multiplier lock), deduped per principal/item/kind, agent-
// trust-weighted, and per-principal capped; and an orchestration run of N agents
// registers ONE demand cluster, not N. These are the work order's three accept
// tests. They exercise the daemon end to end over the anti-gaming layer.

import (
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
)

// (1) flood: 50 signals from one AGENT principal on one item give it no more
// salience than a single such signal (dedup), and less than the operator's one
// signal (trust) — the flood buys nothing.
func TestH4SignalFloodBounded(t *testing.T) {
	dir := initCairn(t)
	fixed := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	d, err := daemon.Start(daemon.Options{Dir: dir, Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	flooded, _ := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zz flooded item"})
	control, _ := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zz control item"})
	opItem, _ := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zz operator-signalled item"})

	for i := 0; i < 50; i++ {
		if _, err := d.Signal(flooded.MessageID, "priority_confirm", 3, "agentA"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Signal(control.MessageID, "priority_confirm", 3, "agentA"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Signal(opItem.MessageID, "priority_confirm", 3, "operator"); err != nil {
		t.Fatal(err)
	}

	s, err := d.SalienceScores()
	if err != nil {
		t.Fatal(err)
	}
	if s[flooded.MessageID] != s[control.MessageID] {
		t.Fatalf("50-signal flood %v differs from single signal %v — dedup failed (flood bought influence)",
			s[flooded.MessageID], s[control.MessageID])
	}
	if s[opItem.MessageID] <= s[flooded.MessageID] {
		t.Fatalf("operator's single signal %v should outweigh an agent's 50-signal flood %v (trust)",
			s[opItem.MessageID], s[flooded.MessageID])
	}
}

// (2) decay: a stale "important" signal's influence fades. Two identical items,
// each with one operator priority_confirm(3); one signal is 90 days old (three
// 30-day half-lives), the other fresh → the fresh item outranks the stale one.
func TestH4StaleSignalDecays(t *testing.T) {
	dir := initCairn(t)
	cur := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	d, err := daemon.Start(daemon.Options{Dir: dir, Now: func() time.Time { return cur }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	stale, _ := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zz stale important note"})
	fresh, _ := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zz fresh important note"})

	// signal the stale item NOW (at cur), then advance the clock 90 days.
	if _, err := d.Signal(stale.MessageID, "priority_confirm", 3, "operator"); err != nil {
		t.Fatal(err)
	}
	cur = cur.Add(90 * 24 * time.Hour)
	if _, err := d.Signal(fresh.MessageID, "priority_confirm", 3, "operator"); err != nil {
		t.Fatal(err)
	}

	s, err := d.SalienceScores()
	if err != nil {
		t.Fatal(err)
	}
	if s[fresh.MessageID] <= s[stale.MessageID] {
		t.Fatalf("fresh signal %v should outrank a 90-day-stale one %v (slow decay)",
			s[fresh.MessageID], s[stale.MessageID])
	}
}

// (3) orchestration-run dedup: five agents in one run (sharing the orchestrator
// principal, none supplying an operator task) all find the same item → ONE
// demand cluster, not five. Genuinely distinct operator tasks still each count.
func TestH4OrchestrationRunOneDemandCluster(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	item, _ := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zz shared run target decision"})

	// five agents, distinct instances, NO operator task (inferred) → one run.
	for i := 0; i < 5; i++ {
		out, err := d.Search(daemon.SearchOptions{
			Query: "shared", AgentInstanceID: "agent-" + string(rune('1'+i)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Outcome(out.InteractionID, "found", item.MessageID); err != nil {
			t.Fatal(err)
		}
	}
	demand, err := d.Telemetry().DemandByMessage()
	if err != nil {
		t.Fatal(err)
	}
	if demand.Fetches[item.MessageID] != 1 {
		t.Fatalf("5-agent orchestration run should be ONE demand cluster, got %d", demand.Fetches[item.MessageID])
	}

	// two genuinely distinct operator TASKS finding it add two real clusters.
	for _, task := range []string{"real-task-1", "real-task-2"} {
		out, err := d.Search(daemon.SearchOptions{Query: "shared", TaskID: task})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Outcome(out.InteractionID, "found", item.MessageID); err != nil {
			t.Fatal(err)
		}
	}
	demand, err = d.Telemetry().DemandByMessage()
	if err != nil {
		t.Fatal(err)
	}
	if demand.Fetches[item.MessageID] != 3 {
		t.Fatalf("run(1) + two distinct tasks(2) = 3 demand clusters, got %d", demand.Fetches[item.MessageID])
	}
}
