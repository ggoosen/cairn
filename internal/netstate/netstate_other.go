//go:build !linux && !darwin

package netstate

// Every other platform. Cairn ships macOS (primary) and Linux (best-effort) and
// no Windows (rulings §platform), so there is nothing to sense here — and the
// correct answer for a platform we do not read is Unknown, which leaves the
// manual `metered` flag exactly as authoritative as it was.

import "context"

func sensePlatform(context.Context) State {
	return State{Source: "metered: unknown (no sensing on this platform); battery: unknown"}
}
