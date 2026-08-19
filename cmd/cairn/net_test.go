package main

// P3-4b: `cairn net` reports transport, role, listener and peers against a live
// daemon. A thin node reports role thin; the default transport is tcp-tailnet.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestP34bNetCommand(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir, "--thin"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	waitForSocket(t, dir)
	defer func() { cancel(); d.Close() }()

	out, err := runCLI(t, "net", "--dir", dir)
	if err != nil {
		t.Fatalf("net: %v\n%s", err, out)
	}
	for _, want := range []string{"transport:", "tcp-tailnet", "role:", "thin", "listener:", "peers:", "relays:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("net output missing %q:\n%s", want, out)
		}
	}

	// json view returns the raw status
	out, err = runCLI(t, "net", "--dir", dir, "--json")
	if err != nil {
		t.Fatalf("net --json: %v\n%s", err, out)
	}
	if !strings.Contains(out, "\"transport\"") || !strings.Contains(out, "\"role\"") {
		t.Fatalf("net --json missing fields:\n%s", out)
	}
}

// D2: an origin-liveness regression must be visible on the two surfaces the
// plan names — `cairn net` and `cairn doctor`. The alarm is seeded as the
// persisted record the beacon itself writes (the daemon loads it at start), so
// this exercises the real load → status → render path rather than a stub.
func TestD2NetAndDoctorSurfaceLivenessAlarms(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	derived := filepath.Join(dir, config.DerivedDirName)
	if err := os.MkdirAll(derived, 0o700); err != nil {
		t.Fatal(err)
	}
	record := `{"watermarks":[{"origin_device":"dev-b","origin_generation":1,"next_seq":41,` +
		`"observed_at":"2026-08-16T00:00:00Z","observed_from":"local-log"}],` +
		`"alarms":[{"origin_device":"dev-b","origin_generation":1,"watermark_next_seq":41,` +
		`"watermark_observed_at":"2026-08-16T00:00:00Z","observed_next_seq":12,"peer":"dev-b",` +
		`"detected_at":"2026-08-16T01:00:00Z","last_seen_at":"2026-08-16T01:00:00Z","observations":1}]}`
	if err := os.WriteFile(filepath.Join(derived, config.LivenessRegistry), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	waitForSocket(t, dir)
	defer func() { cancel(); d.Close() }()

	out, err := runCLI(t, "net", "--dir", dir)
	if err != nil {
		t.Fatalf("net: %v\n%s", err, out)
	}
	for _, want := range []string{"liveness:", "ORIGIN REGRESSION ALARM", "dev-b/1", "41", "12", "29 event(s) missing"} {
		if !strings.Contains(out, want) {
			t.Fatalf("`cairn net` does not surface the alarm (missing %q):\n%s", want, out)
		}
	}

	// doctor reports it as a PROBLEM and exits nonzero
	out, err = runCLI(t, "doctor", "--dir", dir)
	if err == nil {
		t.Fatalf("`cairn doctor` exited clean with an unresolved regression:\n%s", out)
	}
	if !strings.Contains(out, "ORIGIN LIVENESS") || !strings.Contains(out, "dev-b/1") {
		t.Fatalf("`cairn doctor` does not report the regression:\n%s", out)
	}
}

// P3-6: `cairn net` reports the sensed device conditions, and distinguishes
// "not metered" from "could not tell". Run against the real platform sensor on
// whatever host this is — on a machine with no NetworkManager and no battery
// (CI, a container) that means the honest answer is "unknown", which is exactly
// the fail-safe path that must never read as "definitely unmetered".
func TestP36NetReportsSensedPowerState(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	waitForSocket(t, dir)
	defer func() { cancel(); d.Close() }()

	out, err := runCLI(t, "net", "--dir", dir)
	if err != nil {
		t.Fatalf("net: %v\n%s", err, out)
	}
	for _, want := range []string{"metered:", "power:", "battery=", "sensed-metered="} {
		if !strings.Contains(out, want) {
			t.Fatalf("net output missing %q:\n%s", want, out)
		}
	}
	// The policy line is the effective decision; an unsensed platform must show
	// as not-metered-because-unknown, never as a confident "no".
	if strings.Contains(out, "sensed-metered=unknown") && !strings.Contains(out, "unknown") {
		t.Fatalf("an unknown reading was rendered as certainty:\n%s", out)
	}
	t.Logf("this host reports:\n%s", out)
}

// `metered_sense = "off"` is visible as OFF, not as an unreadable platform.
func TestP36NetShowsSensingDisabled(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Device.MeteredSense = config.MeteredSenseOff
	if err := loaded.Device.SaveDevice(loaded.DeviceDir); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	waitForSocket(t, dir)
	defer func() { cancel(); d.Close() }()

	out, err := runCLI(t, "net", "--dir", dir)
	if err != nil {
		t.Fatalf("net: %v\n%s", err, out)
	}
	if !strings.Contains(out, "disabled") {
		t.Fatalf("net does not show that sensing is off:\n%s", out)
	}
	if strings.Contains(out, "metered:    true") {
		t.Fatalf("sensing off reported metered:\n%s", out)
	}
}
