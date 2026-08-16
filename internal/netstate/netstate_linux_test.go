//go:build linux

package netstate

// The Linux probes, exercised on real files and real recorded tool output. The
// nmcli fixtures below are the terse format `nmcli -t -f ... device show` emits;
// the sysfs fixtures are the real file layout (type/online/status), so the
// battery probe reads actual files rather than a mock of the filesystem.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeSupply(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range files {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLinuxBatteryFromSysfs(t *testing.T) {
	cases := []struct {
		name   string
		build  func(t *testing.T, root string)
		want   Tri
		reason string
	}{
		{
			name: "on battery (AC present but offline)",
			build: func(t *testing.T, root string) {
				writeSupply(t, root, "AC", map[string]string{"type": "Mains\n", "online": "0\n"})
				writeSupply(t, root, "BAT0", map[string]string{"type": "Battery\n", "status": "Discharging\n"})
			},
			want: Yes,
		},
		{
			name: "plugged in",
			build: func(t *testing.T, root string) {
				writeSupply(t, root, "AC", map[string]string{"type": "Mains\n", "online": "1\n"})
				writeSupply(t, root, "BAT0", map[string]string{"type": "Battery\n", "status": "Charging\n"})
			},
			want: No,
		},
		{
			name: "battery only, discharging",
			build: func(t *testing.T, root string) {
				writeSupply(t, root, "BAT0", map[string]string{"type": "Battery\n", "status": "Discharging\n"})
			},
			want: Yes,
		},
		{
			name: "battery present, status unreadable, no mains online",
			build: func(t *testing.T, root string) {
				writeSupply(t, root, "BAT0", map[string]string{"type": "Battery\n"})
			},
			want: Yes, // caution: we cannot prove external power
		},
		{
			name:  "no power supplies at all (VM/container)",
			build: func(t *testing.T, root string) {},
			want:  Unknown,
		},
		{
			name: "unrecognised supply type only",
			build: func(t *testing.T, root string) {
				writeSupply(t, root, "wireless", map[string]string{"type": "Wireless\n"})
			},
			want: Unknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.build(t, root)
			old := linuxSysfsPower
			linuxSysfsPower = root
			t.Cleanup(func() { linuxSysfsPower = old })
			got, detail := linuxBattery()
			if got != tc.want {
				t.Fatalf("battery = %s (%s), want %s", got, detail, tc.want)
			}
		})
	}
}

// The path this very host takes: no /sys/class/power_supply at all.
func TestLinuxBatteryUnreadableRootIsUnknown(t *testing.T) {
	old := linuxSysfsPower
	linuxSysfsPower = filepath.Join(t.TempDir(), "definitely-absent")
	t.Cleanup(func() { linuxSysfsPower = old })
	got, detail := linuxBattery()
	if got != Unknown {
		t.Fatalf("absent sysfs root gave %s (%s), want unknown", got, detail)
	}
}

func TestParseNmcliMetered(t *testing.T) {
	connectedWifiMetered := "GENERAL.DEVICE:wlan0\nGENERAL.STATE:100 (connected)\nGENERAL.METERED:yes (guessed)\n"
	cases := []struct {
		name string
		out  string
		want Tri
	}{
		{"unmetered ethernet", "GENERAL.DEVICE:eth0\nGENERAL.STATE:100 (connected)\nGENERAL.METERED:no\n", No},
		{"guessed metered (tether/cellular)", connectedWifiMetered, Yes},
		{"explicitly metered", "GENERAL.DEVICE:wwan0\nGENERAL.STATE:100 (connected)\nGENERAL.METERED:yes\n", Yes},
		{
			name: "metered wins when one of two connected devices is metered",
			out: "GENERAL.DEVICE:eth0\nGENERAL.STATE:100 (connected)\nGENERAL.METERED:no\n\n" +
				"GENERAL.DEVICE:wwan0\nGENERAL.STATE:100 (connected)\nGENERAL.METERED:yes\n",
			want: Yes,
		},
		{
			name: "a DISCONNECTED metered device does not make us metered",
			out: "GENERAL.DEVICE:eth0\nGENERAL.STATE:100 (connected)\nGENERAL.METERED:no\n\n" +
				"GENERAL.DEVICE:wwan0\nGENERAL.STATE:30 (disconnected)\nGENERAL.METERED:yes\n",
			want: No,
		},
		{"loopback is ignored", "GENERAL.DEVICE:lo\nGENERAL.STATE:100 (connected)\nGENERAL.METERED:yes\n", Unknown},
		{"nothing connected", "GENERAL.DEVICE:eth0\nGENERAL.STATE:20 (unavailable)\nGENERAL.METERED:unknown\n", Unknown},
		{"unknown metered value", "GENERAL.DEVICE:eth0\nGENERAL.STATE:100 (connected)\nGENERAL.METERED:unknown\n", Unknown},
		{"empty output", "", Unknown},
		{"garbage", "this is not nmcli output\n", Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := parseNmcliMetered(tc.out)
			if got != tc.want {
				t.Fatalf("metered = %s (%s), want %s", got, detail, tc.want)
			}
			if detail == "" {
				t.Fatal("no detail given")
			}
		})
	}
}

// A machine with no NetworkManager — this container, and every server — must
// report Unknown, never "not metered".
func TestLinuxMeteredWithoutNetworkManager(t *testing.T) {
	old := runProbe
	runProbe = func(context.Context, string, ...string) (string, error) {
		return "", errors.New(`exec: "nmcli": executable file not found in $PATH`)
	}
	t.Cleanup(func() { runProbe = old })
	got, detail := linuxMetered(context.Background())
	if got != Unknown {
		t.Fatalf("no-NetworkManager host gave %s (%s), want unknown", got, detail)
	}
}
