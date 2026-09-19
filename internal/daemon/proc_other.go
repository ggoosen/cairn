//go:build !linux

package daemon

// macOS (the primary platform) has no /proc, and the per-process start time
// lives in a `kinfo_proc` reachable only through `sysctl(KERN_PROC_PID)` —
// which the standard library's `syscall` package does not expose on darwin
// (it is in golang.org/x/sys/unix, a dependency this change does not add for
// one refinement, and hand-rolling the struct offsets is exactly the kind of
// fragile platform code that would break silently on a future release).
//
// So off Linux a session's pid binding is judged on LIVENESS alone: gone or
// owned by another user reaps it, anything else does not. The gap that leaves
// is narrow and fails SAFE: a pid recycled into another process OF THE SAME
// USER inside the session's TTL is read as still alive, so the record lives
// out its TTL instead of being reaped early. That is the pre-D9 behaviour for
// that one case, never a session revoked out from under a running agent.
func platformProcIdentity(int) (string, bool) { return "", false }

// Nor can it cheaply tell a zombie from a running process without the same
// sysctl, so a process killed by a parent that never wait()s reads as alive
// until its session's TTL runs out. Same failure direction as above: late, not
// wrong.
func platformIsZombie(int) (bool, bool) { return false, false }
