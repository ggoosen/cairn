package daemon_test

// FIX-J2 (R45 spirit) — the ack→lexical-visible P95 gate must not FAIL on a
// sample too small to be meaningful.
//
// Codex Brief A run 2's automated gate FAILed with "P95 243ms over 4 sends" on
// an unencrypted WSL node that dropped off the tailnet mid-run. P95 is read by
// rank offset, so with 4 samples "P95" is just the slowest send — one hiccup
// fails the gate. A stable 200-send measurement on a quiet mac node put the real
// ack→lexical-visible P95 at ~2 ms (100x under the 200 ms gate; matches the
// prior 100k scorecard's 1.52 ms), so run2's result was small-sample/degraded-
// node noise, not a regression. The gate now reports INCONCLUSIVE below
// GateLatencyMinSamples and only PASS/FAILs at or above it.

import (
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
)

func TestJ2SmallSampleP95IsInconclusiveNotFail(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	// Reproduce the Codex shape: a handful of samples, one well over 200 ms.
	now := time.Now()
	slow := []time.Duration{40 * time.Millisecond, 60 * time.Millisecond, 55 * time.Millisecond, 243 * time.Millisecond}
	for _, s := range slow {
		if err := d.Telemetry().RecordLatency("ack_to_lexical_visible", s, now); err != nil {
			t.Fatal(err)
		}
	}

	var report strings.Builder
	if err := d.GatesReport(&report); err != nil {
		t.Fatal(err)
	}
	line := latencyLine(t, report.String())
	if strings.Contains(line, "FAIL") {
		t.Fatalf("J2 REGRESSION: a %d-sample P95 FAILed the gate (must be INCONCLUSIVE):\n%s",
			len(slow), line)
	}
	if !strings.Contains(line, "INCONCLUSIVE") {
		t.Fatalf("J2: small sample should be INCONCLUSIVE:\n%s", line)
	}
	// the operator must see the sample size and the requirement (R45: the gate
	// reports why it declined to judge).
	if !strings.Contains(line, "4 sends") || !strings.Contains(line, "≥30") {
		t.Fatalf("J2: gate line should name the sample size and the ≥%d requirement:\n%s",
			config.GateLatencyMinSamples, line)
	}
}

func TestJ2AdequateSampleGivesVerdict(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	// ≥ GateLatencyMinSamples fast samples → a real verdict; all well under the
	// 200 ms gate → PASS.
	now := time.Now()
	for i := 0; i < config.GateLatencyMinSamples+5; i++ {
		if err := d.Telemetry().RecordLatency("ack_to_lexical_visible", 3*time.Millisecond, now); err != nil {
			t.Fatal(err)
		}
	}
	var report strings.Builder
	if err := d.GatesReport(&report); err != nil {
		t.Fatal(err)
	}
	line := latencyLine(t, report.String())
	if !strings.Contains(line, "PASS") {
		t.Fatalf("J2: an adequate fast sample should PASS:\n%s", line)
	}
	if strings.Contains(line, "INCONCLUSIVE") {
		t.Fatalf("J2: an adequate sample must not be INCONCLUSIVE:\n%s", line)
	}
}

// latencyLine extracts the "send-ack → lexical-visible" gate row from the report.
func latencyLine(t *testing.T, report string) string {
	t.Helper()
	for _, l := range strings.Split(report, "\n") {
		if strings.Contains(l, "lexical-visible") {
			return l
		}
	}
	t.Fatalf("no lexical-visible gate row in report:\n%s", report)
	return ""
}
