package daemon

// D9: the honest half of "sessions are auto-revoked on exit". A capability
// session records the pid it was minted for (`BoundPID`); until this file
// existed that binding was decorative — recorded, printed, and read nowhere —
// so a handle stayed valid for its full 24h TTL whether or not the process
// that owned it still existed. An MCP client killed by a signal (which is how
// stdio servers are torn down) leaked exactly one immortal record per respawn.
//
// Liveness here is deliberately conservative: anything we cannot decide reads
// as ALIVE, because over-reaping revokes a running agent's handle mid-session
// and under-reaping merely leaves a record until its TTL.

import (
	"errors"
	"syscall"
)

// procState is what we can honestly say about a recorded pid.
type procState int

const (
	procAlive   procState = iota // running (or undecidable — treated as running)
	procGone                     // no such process
	procForeign                  // a process exists but is not ours, so it is not the one we minted for
)

// pidState probes a pid without touching it. Signal 0 performs the existence
// and permission checks and delivers nothing.
func pidState(pid int) procState {
	if pid <= 0 {
		return procAlive // not a binding we can judge; never reap on it
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return procAlive
	case errors.Is(err, syscall.ESRCH):
		return procGone
	case errors.Is(err, syscall.EPERM):
		// The pid exists but belongs to another user. The daemon and the
		// process it minted for run as the SAME OS user (R22), so this pid
		// has been recycled into somebody else's process — the session's
		// owner is gone.
		return procForeign
	default:
		return procAlive // undecidable: leave it to the TTL
	}
}

// procIdentity returns an opaque token identifying the RUNNING INCARNATION of
// a pid — captured when the session is minted and re-checked when it is
// swept, so a recycled pid cannot resurrect a dead session's record. ok=false
// on platforms (or in conditions) where the incarnation cannot be read, and
// the caller then falls back to liveness alone.
//
// This is the "pair the pid with when the record was created" guard from the
// D9 spec, implemented as an identity captured AT creation rather than as a
// comparison of the process's start time against `CreatedAt`. The comparison
// would be wrong under the `cairn_testhooks` simulated clock, where
// `CreatedAt` comes from an offset clock and a process start time does not —
// an evaluation run with a back-dated clock would reap every live session.
// An identity token compares like with like and needs no clock at all.
func procIdentity(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	return platformProcIdentity(pid)
}
