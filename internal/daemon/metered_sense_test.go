package daemon_test

// P3-6 — automatic metered sensing, at the level where it changes behaviour:
// a thin node's decision to spend data on a remote query.
//
// The probes themselves are platform code, tested in internal/netstate. What is
// tested here is the POLICY, and it is the half that can go wrong in a way that
// costs an operator money or silence:
//
//	metered = configured metered OR sensed metered == Yes
//
// so a sensed reading can only ever WITHHOLD spending, an unreadable platform
// behaves exactly as the daemon did before sensing existed, and a sensor that
// says "not metered" can never overrule an operator who said it is.

import (
	"context"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/netstate"
)

func fakeSense(st netstate.State) netstate.Sensor {
	return netstate.SensorFunc(func(context.Context) netstate.State { return st })
}

// The new capability: nothing is configured metered, but the platform says the
// connection is — and the node stops auto-spending data, saying why.
func TestP36SensedMeteredSuppressesRemoteQuery(t *testing.T) {
	dB, _ := setupPairedPairSense(t, "", func(dev *config.DeviceConfig, addr string) {
		dev.Role = config.RoleThin
		dev.RemoteQuery = true
		dev.Metered = false // NOT configured metered — sensing must do it
		dev.SyncPeers = []string{addr}
	}, fakeSense(netstate.State{Metered: netstate.Yes, Source: "test: tethered"}))

	out, err := dB.Search(daemon.SearchOptions{Query: "roastery approval", BudgetChars: 2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if out.RemoteSource != "" {
		t.Fatalf("a sensed-metered node still spent data on remote query: %q", out.RemoteSource)
	}
	if !out.Partial || !strings.Contains(out.PartialReason, "metered") {
		t.Fatalf("partial reason does not explain the suppression: partial=%v %q", out.Partial, out.PartialReason)
	}
	if !strings.Contains(out.PartialReason, "sensed") {
		t.Fatalf("the reason does not say the platform decided it, not config: %q", out.PartialReason)
	}
}

// The fail-safe case, and the one that matters most: a platform that cannot be
// read must behave EXACTLY as before sensing existed — remote query still runs.
func TestP36UnreadablePlatformChangesNothing(t *testing.T) {
	dB, _ := setupPairedPairSense(t, "", func(dev *config.DeviceConfig, addr string) {
		dev.Role = config.RoleThin
		dev.RemoteQuery = true
		dev.SyncPeers = []string{addr}
	}, fakeSense(netstate.State{Source: "test: nothing readable"})) // Unknown/Unknown

	out, err := dB.Search(daemon.SearchOptions{Query: "roastery approval", BudgetChars: 2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if out.RemoteSource == "" {
		t.Fatalf("an UNREADABLE platform suppressed remote query — sensing must fail safe: %+v", out)
	}
	if out.Partial {
		t.Fatal("remote-consulted result marked partial")
	}
}

// A sensor reporting "not metered" must not override the operator's manual
// flag: sensing may only add caution.
func TestP36SensedUnmeteredCannotOverrideTheManualFlag(t *testing.T) {
	dB, _ := setupPairedPairSense(t, "", func(dev *config.DeviceConfig, addr string) {
		dev.Role = config.RoleThin
		dev.RemoteQuery = true
		dev.Metered = true // the operator said metered
		dev.SyncPeers = []string{addr}
	}, fakeSense(netstate.State{Metered: netstate.No, OnBattery: netstate.No, Source: "test: wired"}))

	out, err := dB.Search(daemon.SearchOptions{Query: "roastery approval", BudgetChars: 2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if out.RemoteSource != "" {
		t.Fatalf("a sensor overrode the operator's metered flag: %q", out.RemoteSource)
	}
	if !strings.Contains(out.PartialReason, "configured") {
		t.Fatalf("the reason should name the manual flag: %q", out.PartialReason)
	}
}

// Battery is SENSED but keys no policy (no spec'd battery behaviour exists):
// a node on battery, not metered, still remote-queries. If that ever changes it
// must be a deliberate decision, and this test is where it will fail first.
func TestP36BatteryAloneChangesNoPolicy(t *testing.T) {
	dB, _ := setupPairedPairSense(t, "", func(dev *config.DeviceConfig, addr string) {
		dev.Role = config.RoleThin
		dev.RemoteQuery = true
		dev.SyncPeers = []string{addr}
	}, fakeSense(netstate.State{Metered: netstate.No, OnBattery: netstate.Yes, Source: "test: on battery"}))

	out, err := dB.Search(daemon.SearchOptions{Query: "roastery approval", BudgetChars: 2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if out.RemoteSource == "" {
		t.Fatal("battery alone suppressed remote query — no spec'd battery policy exists")
	}
}
