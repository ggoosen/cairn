package daemon

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/identity"
)

// initTestCairn creates a real initialized cairn with device state redirected
// into the test dir (SQLite needs real paths). This file is an INTERNAL test
// (package daemon) because the property under test is the daemon's own clock,
// which is not observable from outside it.
func initTestCairn(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
}

// EVAL-PLAN §9.2 clock hook, positive half: under `cairn_testhooks` the
// daemon's clock can be offset from the environment, so a black-box harness
// (which cannot reach Options.Now — it is a separate module by design) can
// replay months of mesh in minutes.
//
// The negative half — that a RELEASE binary contains neither the hook nor
// its env names — lives in cmd/cairn/clock_hook_release_test.go, because it
// has to build and run a real untagged binary to prove it.

func TestSimulatedClockOffsetMovesEventTime(t *testing.T) {
	t.Setenv("CAIRN_FAKE_CLOCK_OFFSET", "-2160h") // 90 days back
	dir := filepath.Join(t.TempDir(), "cairn")
	initTestCairn(t, dir)

	var warn strings.Builder
	d, err := Start(Options{Dir: dir, Warn: &warn})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// The daemon must SAY it is on a simulated clock. A harness reads this
	// on stderr to confirm the hook took effect, and an operator who somehow
	// hits it should never be left guessing why timestamps look wrong.
	if !strings.Contains(warn.String(), "SIMULATED CLOCK") {
		t.Fatalf("no simulated-clock warning on the warn stream: %q", warn.String())
	}

	res, err := d.Publish(PublishRequest{Body: "clock hook probe", Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := d.proj.MessageInfo(res.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	// the projection stores fixed-µs RFC 3339, which RFC3339 parses
	created, err := time.Parse(time.RFC3339, info.CreatedAt)
	if err != nil {
		t.Fatalf("parsing created_at %q: %v", info.CreatedAt, err)
	}
	age := time.Since(created)
	if age < 89*24*time.Hour || age > 91*24*time.Hour {
		t.Fatalf("created_at %s is %v old; the -2160h offset did not reach the event clock", info.CreatedAt, age)
	}
}

func TestSimulatedClockAnchorIsHonoured(t *testing.T) {
	want := time.Now().UTC().Add(-365 * 24 * time.Hour).Truncate(time.Second)
	t.Setenv("CAIRN_FAKE_CLOCK", want.Format(time.RFC3339))
	dir := filepath.Join(t.TempDir(), "cairn")
	initTestCairn(t, dir)

	d, err := Start(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	got := d.now().UTC()
	if diff := got.Sub(want); diff < -time.Minute || diff > time.Minute {
		t.Fatalf("daemon clock %s, want ~%s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// A malformed setting must be FATAL. Falling back to real time would make an
// evaluation run mean something other than what the harness believes, and
// the run would look entirely normal.
func TestMalformedSimulatedClockRefusesToStart(t *testing.T) {
	for _, tc := range []struct{ env, val string }{
		{"CAIRN_FAKE_CLOCK_OFFSET", "ninety days"},
		{"CAIRN_FAKE_CLOCK", "yesterday"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.val)
			dir := filepath.Join(t.TempDir(), "cairn")
			initTestCairn(t, dir)
			d, err := Start(Options{Dir: dir})
			if err == nil {
				d.Close()
				t.Fatalf("%s=%q started anyway", tc.env, tc.val)
			}
			if !strings.Contains(err.Error(), "simulated clock") {
				t.Fatalf("unhelpful error for %s=%q: %v", tc.env, tc.val, err)
			}
		})
	}
}

func TestBothClockSettingsAtOnceIsRefused(t *testing.T) {
	t.Setenv("CAIRN_FAKE_CLOCK_OFFSET", "-1h")
	t.Setenv("CAIRN_FAKE_CLOCK", time.Now().UTC().Format(time.RFC3339))
	dir := filepath.Join(t.TempDir(), "cairn")
	initTestCairn(t, dir)
	d, err := Start(Options{Dir: dir})
	if err == nil {
		d.Close()
		t.Fatal("both clock settings at once must be refused: their meanings differ and guessing is worse than failing")
	}
}

// An explicit Options.Now still wins: the environment hook is the fallback
// for callers that cannot reach the Go API, never an override of one that
// can. Main-suite tests set their own clocks and must be unaffected.
func TestExplicitOptionsNowBeatsTheEnvironment(t *testing.T) {
	t.Setenv("CAIRN_FAKE_CLOCK_OFFSET", "-8760h")
	dir := filepath.Join(t.TempDir(), "cairn")
	initTestCairn(t, dir)
	fixed := time.Date(2030, 3, 4, 5, 6, 7, 0, time.UTC)
	d, err := Start(Options{Dir: dir, Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !d.now().Equal(fixed) {
		t.Fatalf("daemon clock %s, want the injected %s", d.now(), fixed)
	}
}
