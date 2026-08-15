//go:build cairn_testhooks

package daemon

import (
	"fmt"
	"os"
	"time"
)

// Clock control for EVAL-PLAN §9.2: simulating a year of mesh must not take
// a year. `Options.Now` is already injectable, but only through the Go API,
// which the evaluation harness cannot reach — it is a separate module and
// drives cairn as a black box on purpose (EVAL-PLAN §3). So the daemon takes
// its simulated clock from the ENVIRONMENT instead, exactly the way the
// volume-status hook does, and for the same reason.
//
// Compiled ONLY under the `cairn_testhooks` build tag, which `make test` /
// `make vet` set and `make build` deliberately does not. A release binary
// contains neither this code nor the strings below, so nothing can tell a
// shipped daemon what time it is. (`clock_notesthook.go` is the release
// half; cmd/cairn's clock-hook test asserts the absence, both by scanning
// the release binary and by running it.)
//
// TWO DESIGN CHOICES WORTH THE COMMENT:
//
// The clock is OFFSET, never FROZEN. It advances at the real rate; only its
// origin moves. A frozen clock would stall anything that waits for time to
// pass — TTLs, leases, debounces, the housekeeping sweeps — and a harness
// hang looks disturbingly like a result. An offset also keeps the clock
// monotonic within a run, which every duration measurement in the daemon
// assumes.
//
// The offset is resolved ONCE, at Start. Re-reading the environment per call
// would let time jump under a running daemon mid-transaction; instead an
// evaluation epoch is a daemon lifetime, and the harness restarts the daemon
// to move to the next epoch. That also makes the simulated clock a property
// of a recorded run rather than of a moment in it.
//
// WHAT IT DOES NOT COVER: `cairn init` runs the identity ceremonies on the
// real clock (they are not clock-injectable), so genesis and certificate
// timestamps stay real while events can be simulated. That is safe —
// `wall_time` is NEVER used for ordering (see event.Envelope) and device
// certificates carry no validity window — but ceremonies that DO have wall
// clock TTLs (pairing invitations R28/§3.3.6, enrolment requests) will
// expire correctly if a simulated clock crosses their window. That is the
// hook behaving honestly, not a bug to work around.
const (
	fakeClockOffsetEnv = "CAIRN_FAKE_CLOCK_OFFSET" // Go duration, e.g. "-2160h"
	fakeClockAnchorEnv = "CAIRN_FAKE_CLOCK"        // RFC 3339 instant to start from
)

// simulatedClock returns a clock function, a human-readable note for the
// daemon's warn stream, and an error for a MALFORMED setting.
//
// A malformed value is fatal rather than ignored: silently falling back to
// the real clock would produce an evaluation run whose timestamps mean
// something other than what the harness believes, and that result would look
// entirely normal.
func simulatedClock() (func() time.Time, string, error) {
	offset, anchor := os.Getenv(fakeClockOffsetEnv), os.Getenv(fakeClockAnchorEnv)
	switch {
	case offset != "" && anchor != "":
		return nil, "", fmt.Errorf("%s and %s are mutually exclusive (set one)", fakeClockOffsetEnv, fakeClockAnchorEnv)

	case offset != "":
		d, err := time.ParseDuration(offset)
		if err != nil {
			return nil, "", fmt.Errorf("%s=%q: %w (want a Go duration, e.g. -2160h)", fakeClockOffsetEnv, offset, err)
		}
		return offsetClock(d), fmt.Sprintf("offset %s (%s)", d, fakeClockOffsetEnv), nil

	case anchor != "":
		t, err := time.Parse(time.RFC3339, anchor)
		if err != nil {
			return nil, "", fmt.Errorf("%s=%q: %w (want RFC 3339, e.g. 2026-01-01T00:00:00Z)", fakeClockAnchorEnv, anchor, err)
		}
		d := time.Until(t)
		return offsetClock(d), fmt.Sprintf("anchored at %s, advancing normally (%s)", t.UTC().Format(time.RFC3339), fakeClockAnchorEnv), nil
	}
	return nil, "", nil
}

func offsetClock(d time.Duration) func() time.Time {
	return func() time.Time { return time.Now().Add(d) }
}
