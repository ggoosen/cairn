package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPortableConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := &PortableConfig{
		ConfigVersion: PortableConfigVersion,
		CairnID:       "0190a1b2-c3d4-7e5f-8901-234567890abc",
		CreatedAt:     "2026-07-11T00:00:00.000000Z",
	}
	if err := in.SavePortable(dir); err != nil {
		t.Fatal(err)
	}
	out, err := LoadPortable(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip mismatch: %+v != %+v", out, in)
	}
}

func TestDeviceConfigRoundTripAndPerms(t *testing.T) {
	dir := t.TempDir()
	in := &DeviceConfig{
		ConfigVersion:    DeviceConfigVersion,
		CairnID:          "0190a1b2-c3d4-7e5f-8901-234567890abc",
		DeviceID:         "0190a1b2-c3d4-7e5f-8901-234567890def",
		OriginGeneration: FirstGeneration,
		CreatedAt:        "2026-07-11T00:00:00.000000Z",
		AllowUnencrypted: true,
	}
	if err := in.SaveDevice(dir); err != nil {
		t.Fatal(err)
	}
	out, err := LoadDevice(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip mismatch: %+v != %+v", out, in)
	}
	fi, err := os.Stat(filepath.Join(dir, DeviceConfigName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != KeyFilePerm {
		t.Fatalf("device config perm = %o, want %o", fi.Mode().Perm(), KeyFilePerm)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	body := "config_version = 1\ncairn_id = \"x\"\ncreated_at = \"t\"\nmystery_knob = 7\n"
	if err := os.WriteFile(filepath.Join(dir, PortableConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPortable(dir)
	if err == nil || !strings.Contains(err.Error(), "mystery_knob") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
}

func TestLoadRejectsWrongVersion(t *testing.T) {
	dir := t.TempDir()
	body := "config_version = 99\ncairn_id = \"x\"\ncreated_at = \"t\"\n"
	if err := os.WriteFile(filepath.Join(dir, PortableConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPortable(dir); err == nil {
		t.Fatal("want version error, got nil")
	}
}

func TestPortableDirResolution(t *testing.T) {
	t.Setenv("CAIRN_DIR", "/tmp/env-cairn")
	got, err := PortableDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/env-cairn" {
		t.Fatalf("env resolution: got %q", got)
	}
	got, err = PortableDir("/explicit")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/explicit" {
		t.Fatalf("flag resolution: got %q", got)
	}
}

func TestDeviceStateDirOverride(t *testing.T) {
	t.Setenv("CAIRN_DEVICE_STATE_DIR", "/tmp/devstate")
	got, err := DeviceStateDir("abc")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/devstate", "abc", "device")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseTTLDuration(t *testing.T) {
	good := map[string]time.Duration{
		"30s":   30 * time.Second,
		"90m":   90 * time.Minute,
		"1h":    time.Hour,
		"7d":    7 * 24 * time.Hour,
		"1d12h": 36 * time.Hour,
		"0.5d":  12 * time.Hour,
	}
	for in, want := range good {
		got, err := ParseTTLDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseTTLDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "seven days", "-1h", "0s", "5x"} {
		if _, err := ParseTTLDuration(bad); err == nil {
			t.Errorf("ParseTTLDuration(%q) accepted", bad)
		}
	}
}

func TestPortableConfigDurationStrings(t *testing.T) {
	dir := t.TempDir()
	write := func(extra string) error {
		body := "config_version = 1\ncairn_id = \"x\"\ncreated_at = \"t\"\n" + extra
		if err := os.WriteFile(filepath.Join(dir, PortableConfigName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadPortable(dir)
		return err
	}
	// duration strings parse and win
	if err := write("ephemeral_ttl = \"30s\"\nhousekeep_interval = \"90m\"\n"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadPortable(dir)
	if cfg.EphemeralTTL() != 30*time.Second || cfg.HousekeepInterval() != 90*time.Minute {
		t.Fatalf("resolved: %v %v", cfg.EphemeralTTL(), cfg.HousekeepInterval())
	}
	// legacy integer keys still work
	if err := write("ephemeral_ttl_hours = 2\n"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadPortable(dir)
	if cfg.EphemeralTTL() != 2*time.Hour {
		t.Fatalf("legacy key: %v", cfg.EphemeralTTL())
	}
	// both forms → clear error
	if err := write("ephemeral_ttl = \"1h\"\nephemeral_ttl_hours = 2\n"); err == nil ||
		!strings.Contains(err.Error(), "BOTH") {
		t.Fatalf("ambiguous config accepted: %v", err)
	}
	// unparseable string → clear error at load
	if err := write("ephemeral_ttl = \"seven days\"\n"); err == nil {
		t.Fatal("bad duration accepted")
	}
}
