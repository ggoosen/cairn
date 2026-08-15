//go:build !cairn_testhooks

package daemon

import "time"

// Release builds have no clock injection hook at all: the daemon's clock is
// the system clock, and nothing in the environment can move it. See
// clock_testhook.go for why the evaluation harness needs the other half, and
// why it must never ship.
func simulatedClock() (func() time.Time, string, error) { return nil, "", nil }
