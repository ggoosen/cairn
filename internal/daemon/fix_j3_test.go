package daemon_test

// FIX-J3 (R45) — a reindex must never hang silently.
//
// Codex MAJOR-1: `reindex --lexical` on the WSL node hung >90s until killed
// (the managed daemon stayed healthy; post-kill doctor was clean). Investigation
// (PROGRESS J3): `cairn reindex --lexical` runs in-process and side-builds a
// .rebuild db + atomic-swaps — it does NOT take the daemon write lock, so it is
// NOT lock contention. The J1 retry sweep is bounded (fixpoint over a finite
// parked set; healed rows are deleted so cannot re-heal) — it cannot loop. The
// remaining cause is WSL2 drvfs I/O on a Windows-mounted path (environmental).
// Regardless of cause, R45 requires a reindex be bounded + observable: a stall
// watchdog + progress heartbeat now guarantee it.
//
// These tests pin the two software guarantees: the retry sweep terminates on a
// stuck park, and a cancellable reindex aborts instead of running unbounded.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/projection"
)

// TestJ3RetryParkedTerminatesOnStuckPark: a retryable park whose dependency
// never arrives must not make RetryParked loop. It attempts once, makes no
// progress, and returns — bounded, not a retry loop (the J1-related hang
// hypothesis, ruled out).
func TestJ3RetryParkedTerminatesOnStuckPark(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	// inject a retryable park (FK on a topic that never exists).
	if _, err := d.SimpleEventUnvalidatedForTest("topic.link.add", "link",
		"0190a1b2-c3d4-7e5f-8901-00000000feed",
		map[string]any{"link_id": "0190a1b2-c3d4-7e5f-8901-00000000feed",
			"message_id": "0190a1b2-c3d4-7e5f-8901-00000000beef", "topic_id": "never-arrives"}); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		healed := 0
		for i := 0; i < 5; i++ { // repeated sweeps must all terminate
			h, err := d.Projection().RetryParked()
			if err != nil {
				t.Errorf("RetryParked: %v", err)
				break
			}
			healed += h
		}
		done <- healed
	}()

	select {
	case healed := <-done:
		if healed != 0 {
			t.Fatalf("stuck park should not heal (healed=%d)", healed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("J3: RetryParked did not terminate on a stuck park (retry loop)")
	}

	// the park is still present and RETRYABLE (waiting on a dependency).
	parked, err := d.Projection().ParkedEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(parked) != 1 || !parked[0].Retryable {
		t.Fatalf("expected 1 retryable park, got %+v", parked)
	}
}

// TestJ3ReindexContextCancelAborts: the reindex is cancellable — a cancelled
// context aborts the rebuild with an error rather than running to completion or
// hanging. This is the mechanism the CLI stall watchdog uses (FIX-J3).
func TestJ3ReindexContextCancelAborts(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "reindex cancel probe"}); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled → the per-event ctx check aborts immediately

	_, err = projection.ReindexLexicalCtx(ctx, fsx.OS{}, dir, projection.DBPath(dir)+".cancel",
		nil, nil, nil)
	if err == nil {
		t.Fatal("J3: cancelled reindex completed instead of aborting")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("J3: expected a context-cancelled error, got %v", err)
	}
}
