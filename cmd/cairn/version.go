package main

// FIX-H7: end the stale-binary saga. A daemon running an OLD binary silently
// serves stale code — it cost an entire audit run. `cairn --version` and
// `cairn daemon` now dial any running daemon and warn LOUDLY when its build
// version differs from this binary's (R45 spirit: never silent about a broken
// assumption).

import (
	"fmt"
	"io"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
)

// versionMismatchWarning returns the stale-binary warning text and whether to
// emit it, comparing the running daemon's version to this CLI's. Pure — the
// dialing lives in warnIfStaleDaemon.
func versionMismatchWarning(running, cli string) (string, bool) {
	if running == cli {
		return "", false
	}
	r := running
	if r == "" {
		r = "unknown (pre-H7 build — has no version field)"
	}
	return fmt.Sprintf(
		"WARNING: a DIFFERENT cairn daemon is running (daemon %q vs this binary %q).\n"+
			"  The running daemon is STALE — recent code is NOT in effect. Reinstall + restart:\n"+
			"    make install && cairn daemon --install   (or: pkill -f 'cairn daemon', then restart)",
		r, cli), true
}

// warnIfStaleDaemon best-effort dials a running daemon and prints the mismatch
// warning to stderr. Silent when no daemon is running (nothing to compare) or
// the versions match.
func warnIfStaleDaemon(dirFlag, cliVersion string, stderr io.Writer) {
	dir, err := config.PortableDir(dirFlag)
	if err != nil {
		return
	}
	resp, err := daemon.Call(dir, daemon.Request{Op: "status"})
	if err != nil || resp == nil || !resp.OK || resp.Status == nil {
		return // no daemon / unreachable: nothing to compare against
	}
	running, _ := resp.Status["version"].(string)
	if msg, mismatch := versionMismatchWarning(running, cliVersion); mismatch {
		fmt.Fprintln(stderr, msg)
	}
}
