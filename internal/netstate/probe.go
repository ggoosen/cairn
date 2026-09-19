package netstate

// The shared helper the platform probes use to read a platform tool. Kept in
// one place, with one rule: a probe that fails, hangs, or is absent produces an
// error, and an error always becomes Unknown upstream — never a guess.

import (
	"context"
	"os/exec"
	"strings"
)

// runProbe executes a read-only platform query with the caller's deadline. It
// is a variable so tests can substitute recorded output from a real machine
// (the parsers are then tested against real text, and this layer against the
// failure modes: absent binary, non-zero exit, timeout).
var runProbe = func(ctx context.Context, name string, args ...string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// firstLine trims a message to its first line so a multi-line tool error does
// not smear across a diagnostic.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
