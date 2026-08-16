package netstate

// The macOS probes and their parsers, deliberately NOT behind the darwin build
// tag. Only the platform selector (netstate_darwin.go) is tagged; the reading
// and parsing logic lives here so it is compiled and TESTED on every host,
// including CI and this Linux dev box, against recorded output from a real Mac.
// What still cannot be verified off a Mac is whether `pmset`/`route`/
// `networksetup` actually print these formats on a given macOS release — that
// is a claim about macOS, not about this code, and every unrecognised output
// lands on Unknown by construction.

import (
	"context"
	"strings"
)

// darwinBattery parses `pmset -g batt`, whose first line names the active
// source: "Now drawing from 'AC Power'" / "Now drawing from 'Battery Power'".
func darwinBattery(ctx context.Context) (Tri, string) {
	out, err := runProbe(ctx, "pmset", "-g", "batt")
	if err != nil {
		return Unknown, "battery: unknown (pmset unavailable: " + firstLine(err.Error()) + ")"
	}
	tri, detail := parsePmsetBattery(out)
	return tri, "battery: " + detail
}

func parsePmsetBattery(out string) (Tri, string) {
	lower := strings.ToLower(out)
	switch {
	case strings.Contains(lower, "'battery power'"):
		return Yes, "yes (pmset: drawing from battery)"
	case strings.Contains(lower, "'ac power'"):
		return No, "no (pmset: drawing from AC)"
	case strings.Contains(lower, "'ups power'"):
		return Yes, "yes (pmset: drawing from UPS)"
	}
	return Unknown, "unknown (pmset output not recognised)"
}

// darwinMetered detects the tethered case: the interface carrying the default
// route is a hardware port macOS names for a tethering device. Everything else
// (Wi-Fi hotspot, Low Data Mode, cellular via an unnamed port) is Unknown.
func darwinMetered(ctx context.Context) (Tri, string) {
	route, err := runProbe(ctx, "route", "-n", "get", "default")
	if err != nil {
		return Unknown, "metered: unknown (no default route readable: " + firstLine(err.Error()) + ")"
	}
	iface := parseRouteInterface(route)
	if iface == "" {
		return Unknown, "metered: unknown (no default route interface)"
	}
	ports, err := runProbe(ctx, "networksetup", "-listallhardwareports")
	if err != nil {
		return Unknown, "metered: unknown (interface " + iface + ", hardware ports unreadable)"
	}
	port := parseHardwarePort(ports, iface)
	if port == "" {
		return Unknown, "metered: unknown (interface " + iface + " has no named hardware port)"
	}
	if isTetherPort(port) {
		return Yes, "metered: yes (default route over " + port + " — tethered)"
	}
	// NOT a No: macOS does not expose Low Data Mode or hotspot Wi-Fi to us, so
	// "this is Wi-Fi" is not evidence that the connection is unmetered.
	return Unknown, "metered: unknown (default route over " + port + "; macOS exposes no CLI read of Low Data Mode / hotspot — set `metered = true` manually if it is)"
}

// parseRouteInterface pulls the interface out of `route -n get default`:
//
//	   route to: default
//	destination: default
//	  interface: en0
func parseRouteInterface(out string) string {
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && key == "interface" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

// parseHardwarePort maps a BSD device to its hardware-port name in
// `networksetup -listallhardwareports`:
//
//	Hardware Port: iPhone USB
//	Device: en5
//	Ethernet Address: ...
func parseHardwarePort(out, iface string) string {
	port := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Hardware Port:"):
			port = strings.TrimSpace(strings.TrimPrefix(line, "Hardware Port:"))
		case strings.HasPrefix(line, "Device:"):
			if strings.TrimSpace(strings.TrimPrefix(line, "Device:")) == iface {
				return port
			}
		}
	}
	return ""
}

// tetherPorts are the hardware-port names macOS gives a phone sharing its
// cellular connection. Matching is case-insensitive substring, because the
// exact string has varied across releases and devices.
var tetherPorts = []string{"iphone", "ipad", "android", "usb tether", "bluetooth pan"}

func isTetherPort(port string) bool {
	lower := strings.ToLower(port)
	for _, marker := range tetherPorts {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
