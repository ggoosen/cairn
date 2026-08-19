package cairnctl

import (
	"fmt"
	"time"
)

// Clock controls the daemon's sense of time so a run can replay months of
// mesh in minutes (BUILD-PLAN §3.4 E9). E9's recall-over-age and
// recall-under-growth curves are otherwise unrunnable: a year-long
// experiment that takes a year is not an experiment.
//
// The mechanism is an environment hook the daemon compiles in ONLY under the
// `cairn_testhooks` build tag (internal/daemon/clock_testhook.go). A release
// binary contains neither the hook nor these variable names, and cmd/cairn's
// release test asserts that — so the harness must run a testhooks build to
// use this, which FindBinary does by default.
//
// The clock is offset, never frozen: it advances at the real rate and only
// its origin moves. The offset is fixed when the daemon starts, so an
// evaluation EPOCH is a daemon lifetime — to advance simulated time, stop
// the daemon and start it again with a smaller offset. That restart boundary
// is also a useful property in itself: it exercises recovery between epochs,
// which a long-horizon experiment should be doing anyway.
type Clock struct {
	// Offset shifts the daemon's clock relative to real time. Negative
	// values put the mesh in the past, which is how a corpus is aged.
	Offset time.Duration
	// Anchor sets the daemon's clock to a specific instant at start. Mutually
	// exclusive with Offset — the daemon refuses both at once rather than
	// guessing which was meant.
	Anchor time.Time
}

// These names must match internal/daemon/clock_testhook.go. They are
// duplicated rather than imported because eval/ is a separate module and
// cannot see the daemon's internals — that is the point of the split, and
// this duplication is its (small, deliberate) cost. The driver's clock test
// fails if the two ever drift apart.
const (
	fakeClockOffsetEnv = "CAIRN_FAKE_CLOCK_OFFSET"
	fakeClockAnchorEnv = "CAIRN_FAKE_CLOCK"
)

// IsZero reports whether the clock is real time.
func (c Clock) IsZero() bool { return c.Offset == 0 && c.Anchor.IsZero() }

func (c Clock) env() []string {
	switch {
	case c.Offset != 0 && !c.Anchor.IsZero():
		// Left to the daemon to refuse would work, but failing here names
		// the harness call site that made the mistake.
		panic("cairnctl.Clock: set Offset or Anchor, not both")
	case c.Offset != 0:
		return []string{fmt.Sprintf("%s=%s", fakeClockOffsetEnv, c.Offset)}
	case !c.Anchor.IsZero():
		return []string{fmt.Sprintf("%s=%s", fakeClockAnchorEnv, c.Anchor.UTC().Format(time.RFC3339))}
	}
	return nil
}

// SimulatedClockWarning is the daemon's stderr announcement that its clock is
// not real. A harness asserts on it to confirm the hook took effect, rather
// than assuming an environment variable was honoured.
const SimulatedClockWarning = "SIMULATED CLOCK"
