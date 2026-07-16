package daemon_test

// P3-4: an unavailable transport (iroh, deferred) disables sync LOUDLY but does
// NOT take the daemon down — local reads/writes keep working (R45). The default
// transport syncs normally (covered by the N5/N6 suites).

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestP34IrohTransportDisablesSyncNotDaemon(t *testing.T) {
	dir := initCairn(t)
	// select the deferred iroh transport in device config
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Device.Transport = config.TransportIroh
	if err := loaded.Device.SaveDevice(loaded.DeviceDir); err != nil {
		t.Fatal(err)
	}

	d := startDaemon(t, dir)

	// sync is disabled with an instructive message naming iroh
	if st := d.SyncListenState(); !strings.Contains(st, "iroh") {
		t.Fatalf("sync not disabled for the deferred transport: %q", st)
	}

	// the daemon still serves local reads + writes
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "local still works under a deferred transport"}); err != nil {
		t.Fatalf("local publish failed under iroh transport: %v", err)
	}
	so, err := d.Search(daemon.SearchOptions{Query: "local", BudgetChars: 2000})
	if err != nil {
		t.Fatalf("local search failed under iroh transport: %v", err)
	}
	if len(so.Results) == 0 {
		t.Fatal("local search returned nothing")
	}

	// dialing a peer is refused (transport unavailable), not a panic
	if _, err := d.SyncWith("127.0.0.1:9"); err == nil {
		t.Fatal("SyncWith succeeded with an unavailable transport")
	}
}
