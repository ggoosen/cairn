package netstate

// The macOS probes, tested on every host (see darwinprobe.go for why they are
// not behind the build tag). Fixtures are the output formats of `pmset -g batt`,
// `route -n get default` and `networksetup -listallhardwareports`.
//
// NOTE, so nobody mistakes this for a macOS pass: these tests prove the parsing
// and the failure→Unknown mapping. They do NOT prove that a given macOS release
// prints these strings — that needs a Mac, and it is recorded as such in
// PROGRESS P3-6.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParsePmsetBattery(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want Tri
	}{
		{"on AC", "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1234)\t100%; charged; 0:00 remaining present: true\n", No},
		{"on battery", "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1234)\t83%; discharging; 4:41 remaining present: true\n", Yes},
		{"on UPS", "Now drawing from 'UPS Power'\n", Yes},
		{"desktop with no battery", "Now drawing from 'AC Power'\n", No},
		{"unrecognised", "pmset: something else entirely\n", Unknown},
		{"empty", "", Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, detail := parsePmsetBattery(tc.out); got != tc.want {
				t.Fatalf("battery = %s (%s), want %s", got, detail, tc.want)
			}
		})
	}
}

func TestParseRouteInterface(t *testing.T) {
	out := "   route to: default\ndestination: default\n       mask: default\n    gateway: 192.168.1.1\n  interface: en0\n      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>\n"
	if got := parseRouteInterface(out); got != "en0" {
		t.Fatalf("interface = %q", got)
	}
	if got := parseRouteInterface("route: writing to routing socket: not in table\n"); got != "" {
		t.Fatalf("no-route output yielded %q", got)
	}
}

const hardwarePorts = `Hardware Port: Wi-Fi
Device: en0
Ethernet Address: aa:bb:cc:dd:ee:ff

Hardware Port: iPhone USB
Device: en5
Ethernet Address: 11:22:33:44:55:66

Hardware Port: Thunderbolt Bridge
Device: bridge0
Ethernet Address: 77:88:99:aa:bb:cc
`

func TestParseHardwarePort(t *testing.T) {
	for iface, want := range map[string]string{
		"en0":     "Wi-Fi",
		"en5":     "iPhone USB",
		"bridge0": "Thunderbolt Bridge",
		"en9":     "",
	} {
		if got := parseHardwarePort(hardwarePorts, iface); got != want {
			t.Fatalf("%s → %q, want %q", iface, got, want)
		}
	}
}

func TestIsTetherPort(t *testing.T) {
	for _, port := range []string{"iPhone USB", "iPad USB", "Android USB tether", "Bluetooth PAN"} {
		if !isTetherPort(port) {
			t.Fatalf("%q not recognised as tethering", port)
		}
	}
	for _, port := range []string{"Wi-Fi", "Ethernet", "Thunderbolt Bridge", "USB 10/100/1000 LAN"} {
		if isTetherPort(port) {
			t.Fatalf("%q wrongly treated as tethering", port)
		}
	}
}

// The whole darwin metered probe, driven through a fake command runner: a
// tethered default route is metered; Wi-Fi is UNKNOWN (macOS exposes no CLI
// read of Low Data Mode), never "not metered"; every failure is Unknown.
func TestDarwinMeteredProbe(t *testing.T) {
	orig := runProbe
	t.Cleanup(func() { runProbe = orig })

	fake := func(route, ports string, err error) func(context.Context, string, ...string) (string, error) {
		return func(_ context.Context, name string, _ ...string) (string, error) {
			if err != nil {
				return "", err
			}
			if name == "route" {
				return route, nil
			}
			return ports, nil
		}
	}
	cases := []struct {
		name  string
		probe func(context.Context, string, ...string) (string, error)
		want  Tri
	}{
		{"tethered iPhone", fake("  interface: en5\n", hardwarePorts, nil), Yes},
		{"wi-fi is not proof of unmetered", fake("  interface: en0\n", hardwarePorts, nil), Unknown},
		{"unknown interface", fake("  interface: en9\n", hardwarePorts, nil), Unknown},
		{"no default route", fake("", hardwarePorts, nil), Unknown},
		{"tools missing", fake("", "", errors.New("executable file not found in $PATH")), Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runProbe = tc.probe
			got, detail := darwinMetered(context.Background())
			if got != tc.want {
				t.Fatalf("metered = %s (%s), want %s", got, detail, tc.want)
			}
			if got == Unknown && !strings.Contains(detail, "unknown") {
				t.Fatalf("unknown reading without an explanation: %q", detail)
			}
		})
	}
}

func TestDarwinBatteryProbeFailureIsUnknown(t *testing.T) {
	orig := runProbe
	t.Cleanup(func() { runProbe = orig })
	runProbe = func(context.Context, string, ...string) (string, error) {
		return "", errors.New("exit status 1")
	}
	if got, detail := darwinBattery(context.Background()); got != Unknown {
		t.Fatalf("failed pmset gave %s (%s)", got, detail)
	}
}
