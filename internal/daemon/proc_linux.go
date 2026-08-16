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

func platformProcIdentity(pid int) (string, bool) {
	blob, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// The comm field (2) is parenthesised and may itself contain spaces and
	// parentheses, so fields are counted from the LAST ')'.
	line := string(blob)
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 >= len(line) {
		return "", false
	}
	rest := strings.Fields(line[end+2:]) // rest[0] is field 3 (state)
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
