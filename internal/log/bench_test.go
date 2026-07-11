package log_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// TestAppendRecover10k is the M1 acceptance benchmark on the REAL
// filesystem: 10k events appended with full durability ordering (crossing
// the real 10k seal threshold), then cold recovery with full verification.
// Timings are printed for PROGRESS.md. Run with: go test -run 10k -v -timeout 30m
func TestAppendRecover10k(t *testing.T) {
	if testing.Short() {
		t.Skip("10k durability benchmark skipped in -short")
	}
	dir := t.TempDir()
	fsys := fsx.OS{}

	c, genv, grec := newChain(t)
	lg, err := cairnlog.Create(fsys, dir, origin(c))
	if err != nil {
		t.Fatal(err)
	}

	const total = 10_000
	// pre-sign everything so the timing isolates the durable-append path
	records := make([][]byte, 0, total)
	envs := make([]*event.Envelope, 0, total)
	envs = append(envs, genv)
	records = append(records, grec)
	for i := 1; i < total; i++ {
		env, rec := c.next(t)
		envs = append(envs, env)
		records = append(records, rec)
	}

	start := time.Now()
	for i := 0; i < total; i++ {
		if err := lg.Append(records[i], envs[i]); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	appendDur := time.Since(start)
	lg.Close()

	start = time.Now()
	relg, report, err := cairnlog.Open(fsys, dir, origin(c), identity.NewChainVerifier().Verify, nil)
	if err != nil {
		t.Fatal(err)
	}
	recoverDur := time.Since(start)
	relg.Close()

	if report.Events != total {
		t.Fatalf("recovered %d events, want %d", report.Events, total)
	}
	if report.SealedSegments != 1 {
		t.Fatalf("expected exactly one sealed segment at the 10k threshold, got %d", report.SealedSegments)
	}

	fmt.Printf("BENCH append 10k (fsync each): total %v, per-event %v\n", appendDur, appendDur/total)
	fmt.Printf("BENCH cold recovery 10k (full verify): total %v, per-event %v\n", recoverDur, recoverDur/total)
}
