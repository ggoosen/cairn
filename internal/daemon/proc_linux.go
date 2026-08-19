//go:build linux

package daemon

// Linux can name a process's incarnation exactly: field 22 of /proc/<pid>/stat
// is the process's start time in clock ticks since boot, and the boot id
// distinguishes one boot from the next (start-time ticks alone would collide
// across reboots, and after a reboot every recorded pid is stale anyway).

import (
	"os"
	"strconv"
	"strings"
)

// platformIsZombie reports whether the pid is a process that has exited and is
// waiting to be reaped (state Z or X in /proc/<pid>/stat). ok=false when the
// state cannot be read, and the caller then keeps the signal-0 answer.
func platformIsZombie(pid int) (bool, bool) {
	state, ok := procStatFields(pid)
	if !ok || len(state) == 0 {
		return false, false
	}
	return state[0] == "Z" || state[0] == "X", true
}

// procStatFields returns the whitespace-separated fields of /proc/<pid>/stat
// from field 3 (state) onward. The comm field (2) is parenthesised and may
// itself contain spaces and parentheses, so counting starts at the LAST ')'.
func procStatFields(pid int) ([]string, bool) {
	blob, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil, false
	}
	line := string(blob)
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 >= len(line) {
		return nil, false
	}
	return strings.Fields(line[end+2:]), true
}

func platformProcIdentity(pid int) (string, bool) {
	rest, ok := procStatFields(pid) // rest[0] is field 3 (state)
	if !ok {
		return "", false
	}
	const startTimeField = 22
	if len(rest) < startTimeField-2 {
		return "", false
	}
	start := rest[startTimeField-3]
	if _, err := strconv.ParseUint(start, 10, 64); err != nil {
		return "", false
	}
	return "linux:" + bootID() + ":" + start, true
}

// bootID is stable for the life of a boot and cheap to read. Unreadable (a
// restricted container, say) degrades to a constant: the start-time half still
// distinguishes incarnations within one boot.
func bootID() string {
	blob, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "unknown-boot"
	}
	return strings.TrimSpace(string(blob))
}
