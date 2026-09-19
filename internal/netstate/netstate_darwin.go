//go:build darwin

package netstate

// macOS sensing (the primary platform by ruling) — and an honest account of
// what macOS will and will not tell a CLI process.
//
// BATTERY is straightforward: `pmset -g batt` reports the active power source
// in a format that has been stable for many macOS releases.
//
// METERED is not. The signal a GUI app would use is Network.framework's
// nw_path "expensive" (cellular/hotspot) and "constrained" (Low Data Mode)
// flags, and those are reachable only through the framework itself — there is
// no supported command-line read of them, and no Go binding without cgo and an
// Objective-C bridge. Rather than guess, we sense the ONE metered case macOS
// does expose in text: an active default route over a tethered iPhone/Android
// (USB tethering shows up as its own hardware port). A Wi-Fi hotspot or a Low
// Data Mode Wi-Fi network is INVISIBLE to us and reports Unknown, which means
// the manual `metered` flag remains the operator's tool for those. That is a
// real gap and is documented as one; it is not papered over with a heuristic
// that would be wrong in both directions.

import "context"

func sensePlatform(ctx context.Context) State {
	metered, msrc := darwinMetered(ctx)
	battery, bsrc := darwinBattery(ctx)
	return State{Metered: metered, OnBattery: battery, Source: msrc + "; " + bsrc}
}
