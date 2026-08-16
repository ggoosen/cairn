//go:build linux

package netstate

// Linux sensing (best-effort by ruling: macOS arm64 is primary).
//
// METERED comes from NetworkManager, which is the only component on a normal
// Linux desktop that knows the answer: it tracks a per-connection `metered`
// property (set by the user, or guessed from the connection type — a cellular
// modem or a phone tether guesses "yes"). We read it through `nmcli` rather
// than by speaking D-Bus, because that keeps the dependency at "a binary that
// is present iff NetworkManager is" instead of adding a D-Bus client to a
// daemon that otherwise has none. A machine with no NetworkManager — a server,
// a container, a systemd-networkd or netplan-only box — has NO reading, and the
// honest answer there is Unknown, never "no".
//
// BATTERY comes from sysfs (/sys/class/power_supply), which needs no helper
// process at all: the AC adapter's `online` file, falling back to the battery's
// `status`. A machine with no power_supply class (most VMs, most containers)
// reports Unknown.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// linuxSysfsPower is the sysfs power-supply root. A variable so tests can point
// it at a fixture tree — the probe is then exercised for real, on real files,
// rather than mocked away.
var linuxSysfsPower = "/sys/class/power_supply"

func sensePlatform(ctx context.Context) State {
	metered, msrc := linuxMetered(ctx)
	battery, bsrc := linuxBattery()
	return State{Metered: metered, OnBattery: battery, Source: msrc + "; " + bsrc}
}

// linuxMetered asks NetworkManager. Only a Yes is actionable downstream, so the
// bias in the "guessed" cases is deliberately toward caution: a guessed-metered
// connection reads as metered.
func linuxMetered(ctx context.Context) (Tri, string) {
	out, err := runProbe(ctx, "nmcli", "-t", "-f", "GENERAL.DEVICE,GENERAL.STATE,GENERAL.METERED", "device", "show")
	if err != nil {
		return Unknown, "metered: unknown (no NetworkManager/nmcli here — " + firstLine(err.Error()) + ")"
	}
	tri, detail := parseNmcliMetered(out)
	return tri, "metered: " + detail
}

// nmcliDevice is one device block of `nmcli -t ... device show` output.
type nmcliDevice struct{ name, state, metered string }

// parseNmcliMetered reads terse nmcli output — one KEY:VALUE per line, one
// block per device:
//
//	GENERAL.DEVICE:wlan0
//	GENERAL.STATE:100 (connected)
//	GENERAL.METERED:yes (guessed)
//
// Only CONNECTED devices count (a down interface's metered flag says nothing
// about the data this node is about to spend) and loopback is ignored. One
// connected metered device settles it: caution wins over a second, unmetered
// interface, because we cannot know which one a sync will actually traverse.
func parseNmcliMetered(out string) (Tri, string) {
	var devs []nmcliDevice
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch key {
		case "GENERAL.DEVICE":
			devs = append(devs, nmcliDevice{name: val})
		case "GENERAL.STATE":
			if len(devs) > 0 {
				devs[len(devs)-1].state = val
			}
		case "GENERAL.METERED":
			if len(devs) > 0 {
				devs[len(devs)-1].metered = val
			}
		}
	}
	tri, detail := Unknown, "unknown (NetworkManager reported no connected device)"
	for _, d := range devs {
		if d.name == "lo" || !strings.HasPrefix(d.state, "100") {
			continue // not connected, or loopback
		}
		switch {
		case strings.HasPrefix(d.metered, "yes"):
			return Yes, "yes (NetworkManager: " + d.name + " = " + d.metered + ")"
		case strings.HasPrefix(d.metered, "no"):
			tri, detail = No, "no (NetworkManager: "+d.name+" = "+d.metered+")"
		default:
			if tri == Unknown {
				detail = "unknown (NetworkManager: " + d.name + " = " + d.metered + ")"
			}
		}
	}
	return tri, detail
}

// linuxBattery reads sysfs. Order: the external supply's `online` (present on
// every laptop and authoritative), then a battery's `status` for machines that
// expose no mains device.
func linuxBattery() (Tri, string) {
	entries, err := os.ReadDir(linuxSysfsPower)
	if err != nil {
		return Unknown, "battery: unknown (" + linuxSysfsPower + " unreadable: " + firstLine(err.Error()) + ")"
	}
	battery := ""
	for _, e := range entries {
		dir := filepath.Join(linuxSysfsPower, e.Name())
		switch strings.TrimSpace(readSysfs(filepath.Join(dir, "type"))) {
		case "Mains", "USB", "UPS":
			if strings.TrimSpace(readSysfs(filepath.Join(dir, "online"))) == "1" {
				return No, "battery: no (" + e.Name() + " is online)"
			}
		case "Battery":
			if battery == "" {
				battery = e.Name()
			}
		}
	}
	if battery != "" {
		switch strings.TrimSpace(readSysfs(filepath.Join(linuxSysfsPower, battery, "status"))) {
		case "Discharging":
			return Yes, "battery: yes (" + battery + " discharging)"
		case "Charging", "Full", "Not charging":
			return No, "battery: no (" + battery + " on external power)"
		}
		return Yes, "battery: yes (" + battery + " present, no external supply online)"
	}
	if len(entries) == 0 {
		return Unknown, "battery: unknown (no power supplies exposed — VM or container)"
	}
	return Unknown, "battery: unknown (no readable power supply)"
}

func readSysfs(path string) string {
	blob, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(blob)
}
