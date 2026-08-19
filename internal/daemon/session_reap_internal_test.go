package daemon

// D9, unit level: the reap predicate and the pid probe. The end-to-end
// behaviour is in session_reap_test.go; these pin the two judgements that a
// black-box test cannot force — a pid recycled into a different process, and
// the "undecidable reads as alive" rule.

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

func liveRecord(now time.Time, pid int) *Session {
	return &Session{
		Token:     "t",
		Name:      "agent",
		Profile:   "agent-standard",
		Parent:    "operator",
		CreatedAt: now.UTC().Format(config.WallTimeFormat),
		ExpiresAt: now.Add(config.SessionTTLDefault).UTC().Format(config.WallTimeFormat),
		BoundPID:  pid,
	}
}

func TestD9DeadPredicate(t *testing.T) {
	now := time.Now()
	s := &sessions{deviceID: "this-device", byToken: map[string]*Session{}}

	// alive process, bound by this device, inside TTL → never dead
	rec := liveRecord(now, os.Getpid())
	rec.BoundDevice = "this-device"
	if ident, ok := procIdentity(os.Getpid()); ok {
		rec.BoundProc = ident
	}
	if reason, dead := s.deadLocked(rec, now); dead {
		t.Fatalf("live session judged dead (%s)", reason)
	}

	// expired → dead, whatever the process is doing
	expired := liveRecord(now.Add(-2*config.SessionTTLDefault), os.Getpid())
	expired.BoundDevice = "this-device"
	if reason, dead := s.deadLocked(expired, now); !dead || reason != "expired" {
		t.Fatalf("expired session: reason=%q dead=%v", reason, dead)
	}

	// unparseable expiry → dead (nothing can validate it; resolve() has
	// always treated it that way)
	bad := liveRecord(now, 0)
	bad.ExpiresAt = "not-a-time"
	if _, dead := s.deadLocked(bad, now); !dead {
		t.Fatal("record with an unreadable expiry survived")
	}

	// pid bound on ANOTHER device → not judged on liveness at all
	foreign := liveRecord(now, 999999)
	foreign.BoundDevice = "some-other-device"
	if reason, dead := s.deadLocked(foreign, now); dead {
		t.Fatalf("foreign-device pid binding was trusted (%s)", reason)
	}
	// ...and a record from before D9 carries no device: same rule
	legacy := liveRecord(now, 999999)
	if reason, dead := s.deadLocked(legacy, now); dead {
		t.Fatalf("legacy record without a device was pid-reaped (%s)", reason)
	}
	// ...but the same pid, bound HERE, is reaped
	local := liveRecord(now, 999999)
	local.BoundDevice = "this-device"
	if _, dead := s.deadLocked(local, now); !dead {
		t.Fatal("dead pid bound on this device was not reaped")
	}
}

// A recycled pid must not resurrect a dead session's record: the incarnation
// token captured at mint no longer matches the one the pid has now. Real pid
// reuse cannot be forced in a test, so the mismatch is what is asserted —
// which is exactly the condition the sweep checks.
func TestD9RecycledPidIsNotTheSameProcess(t *testing.T) {
	if _, ok := procIdentity(os.Getpid()); !ok {
		t.Skip("no process-incarnation source on this platform (see proc_other.go)")
	}
	now := time.Now()
	s := &sessions{deviceID: "this-device", byToken: map[string]*Session{}}

	rec := liveRecord(now, os.Getpid())
	rec.BoundDevice = "this-device"
	rec.BoundProc = "linux:some-other-boot:1"
	reason, dead := s.deadLocked(rec, now)
	if !dead || reason != "process-recycled" {
		t.Fatalf("a pid whose incarnation changed was kept: reason=%q dead=%v", reason, dead)
	}

	// The negative half, and the one that matters more: the token for a
	// LIVING process is stable across reads. A token that jittered would reap
	// live sessions, which is the failure this whole fix must not introduce.
	// (The token is pid + boot + start-tick, the conventional Unix process
	// identity; two processes cannot share a pid in the same 10 ms tick.)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a helper process: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	first, ok := procIdentity(cmd.Process.Pid)
	if !ok {
		t.Fatal("no incarnation token for a running child")
	}
	for i := 0; i < 5; i++ {
		again, ok := procIdentity(cmd.Process.Pid)
		if !ok || again != first {
			t.Fatalf("incarnation token changed under a live process: %q → %q", first, again)
		}
	}
	live := liveRecord(now, cmd.Process.Pid)
	live.BoundDevice = "this-device"
	live.BoundProc = first
	if reason, dead := s.deadLocked(live, now); dead {
		t.Fatalf("live child judged dead (%s)", reason)
	}
}

func TestD9PidState(t *testing.T) {
	if got := pidState(os.Getpid()); got != procAlive {
		t.Fatalf("own pid reads as %v", got)
	}
	if got := pidState(0); got != procAlive {
		t.Fatalf("unbound pid must read as alive (never reap on it), got %v", got)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a helper process: %v", err)
	}
	pid := cmd.Process.Pid
	if got := pidState(pid); got != procAlive {
		t.Fatalf("running child reads as %v", got)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait() // reap the zombie, or it still "exists"
	if got := pidState(pid); got != procGone {
		t.Fatalf("killed child reads as %v, want procGone", got)
	}
}

// A ZOMBIE — killed, but not yet reaped by its parent — answers signal 0
// exactly like a running process. A live smoke test caught this: a `cairn mcp`
// killed by a parent that does not wait() left its session resident, which is
// the very case the sweep exists to catch.
func TestD9ZombieIsNotAliveEnoughToHoldASession(t *testing.T) {
	if _, ok := platformIsZombie(os.Getpid()); !ok {
		t.Skip("no process-state source on this platform (see proc_other.go)")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a helper process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// deliberately NOT reaped: no Wait() until the assertions are done
	deadline := time.Now().Add(5 * time.Second)
	for {
		if zombie, ok := platformIsZombie(pid); ok && zombie {
			break
		}
		if time.Now().After(deadline) {
			_, _ = cmd.Process.Wait()
			t.Skip("the child never entered the zombie state (something reaped it first)")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := pidState(pid); got != procGone {
		_, _ = cmd.Process.Wait()
		t.Fatalf("zombie reads as %v — a session bound to it would survive its process", got)
	}
	now := time.Now()
	s := &sessions{deviceID: "this-device", byToken: map[string]*Session{}}
	rec := liveRecord(now, pid)
	rec.BoundDevice = "this-device"
	if reason, dead := s.deadLocked(rec, now); !dead || reason != "process-gone" {
		t.Fatalf("session bound to a zombie: reason=%q dead=%v", reason, dead)
	}
	_, _ = cmd.Process.Wait()
}
