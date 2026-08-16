package daemon_test

// P3-3c: a member node can ask a FULL node to search on its behalf (spec §7's
// thin-node "remote query dependency"). The full node answers with ranked
// results; a THIN node refuses to serve remote search (it has no universal
// completeness to offer).

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/peer"
	"github.com/ggoosen/cairn/internal/rank"
)

// setupPairedPair inits mesh-owner A (with the given role) on a loopback
// listener + some content, pairs a member node B into A's mesh, starts B's
// daemon, and returns (B's daemon, A's sync addr). A and B use separate
// device-state bases.
func setupPairedPair(t *testing.T, ownerRole string) (*daemon.Daemon, string) {
	return setupPairedPairCfg(t, ownerRole, nil)
}

// setupPairedPairCfg is setupPairedPair with a hook to mutate B's device config
// (role, remote_query, sync_peers) BEFORE B's daemon starts.
func setupPairedPairCfg(t *testing.T, ownerRole string, configB func(dev *config.DeviceConfig, addr string)) (*daemon.Daemon, string) {
	t.Helper()
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")

	baseA := t.TempDir()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dirA, SyncListen: "127.0.0.1:0", Role: ownerRole, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	dA, cancelA, addr := runListeningDaemon(t, dirA, &syncBuf{})
	t.Cleanup(cancelA)
	if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "the quarterly roastery planning approval is attached"}); err != nil {
		t.Fatal(err)
	}
	inv := mintInvite(t, dirA, time.Now())

	baseB := t.TempDir()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	dirB := filepath.Join(t.TempDir(), "cairnB")
	priv, err := identity.PairJoinInstall(identity.PairJoinOptions{Invitation: inv, Dir: dirB, Now: time.Now, Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"cert": inv.Cert, "invite_id": inv.InviteID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.PairDial(addr, inv.CairnID, payload, priv, inviteTrust(t, inv)); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if configB != nil {
		loadedB, err := identity.Load(dirB)
		if err != nil {
			t.Fatal(err)
		}
		configB(loadedB.Device, addr)
		if err := loadedB.Device.SaveDevice(loadedB.DeviceDir); err != nil {
			t.Fatal(err)
		}
	}
	dB := startDaemon(t, dirB)
	return dB, addr
}

func TestP33cRemoteSearchAgainstFullNode(t *testing.T) {
	dB, addr := setupPairedPair(t, "") // owner A is a full node

	out, err := dB.RemoteSearch(addr, "roastery approval", mustSpec(t, 2000, 0))
	if err != nil {
		t.Fatalf("remote search: %v", err)
	}
	if out == nil || len(out.Results) == 0 {
		t.Fatalf("remote search returned no results: %+v", out)
	}
}

func TestP33cThinNodeRefusesRemoteSearch(t *testing.T) {
	dB, addr := setupPairedPair(t, "thin") // owner A is a THIN node

	if _, err := dB.RemoteSearch(addr, "roastery approval", mustSpec(t, 2000, 0)); err == nil ||
		!strings.Contains(err.Error(), "thin") {
		t.Fatalf("thin node did not refuse remote search: %v", err)
	}
}

// P3-3d: a thin node with remote-query enabled returns a full peer's complete
// result (marked remote-sourced, not partial); without remote-query it returns
// its own partial local view.
func TestP33dThinNodeRemoteConsult(t *testing.T) {
	// B is thin, remote-query ON, peer = full node A
	dB, _ := setupPairedPairCfg(t, "", func(dev *config.DeviceConfig, addr string) {
		dev.Role = config.RoleThin
		dev.RemoteQuery = true
		dev.SyncPeers = []string{addr}
	})
	out, err := dB.Search(daemon.SearchOptions{Query: "roastery approval", BudgetChars: 2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if out.RemoteSource == "" {
		t.Fatalf("thin node did not consult a full peer (RemoteSource empty): %+v", out)
	}
	if out.Partial {
		t.Fatal("remote-consulted result still marked partial")
	}
	if len(out.Results) == 0 {
		t.Fatal("remote-consulted search returned no results")
	}
}

func TestP33dThinNodeWithoutRemoteQueryStaysLocalPartial(t *testing.T) {
	// B is thin, remote-query OFF — search stays local + partial, no consult
	dB, _ := setupPairedPairCfg(t, "", func(dev *config.DeviceConfig, addr string) {
		dev.Role = config.RoleThin
		dev.RemoteQuery = false
		dev.SyncPeers = []string{addr}
	})
	out, err := dB.Search(daemon.SearchOptions{Query: "roastery approval", BudgetChars: 2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if out.RemoteSource != "" {
		t.Fatalf("thin node remote-consulted with remote_query off: %q", out.RemoteSource)
	}
	if !out.Partial {
		t.Fatal("thin node local search not marked partial")
	}
}

// P3-3e: a metered thin node does NOT auto-spend data on remote query even with
// remote_query on — it stays local + partial, and the reason says why.
func TestP33eMeteredThinNodeSuppressesRemoteQuery(t *testing.T) {
	dB, _ := setupPairedPairCfg(t, "", func(dev *config.DeviceConfig, addr string) {
		dev.Role = config.RoleThin
		dev.RemoteQuery = true
		dev.Metered = true // metered suppresses the auto-consult
		dev.SyncPeers = []string{addr}
	})
	out, err := dB.Search(daemon.SearchOptions{Query: "roastery approval", BudgetChars: 2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if out.RemoteSource != "" {
		t.Fatalf("metered thin node still remote-consulted: %q", out.RemoteSource)
	}
	if !out.Partial {
		t.Fatal("metered thin node search not partial")
	}
	if !strings.Contains(out.PartialReason, "metered") {
		t.Fatalf("partial reason does not explain metered suppression: %q", out.PartialReason)
	}
}

// mustSpec builds a budget spec for the tests that call the daemon API
// directly (D4: a budget is a mode plus a limit, not a bare int).
func mustSpec(t *testing.T, chars, tokens int) rank.Spec {
	t.Helper()
	spec, err := rank.NewSpec(chars, tokens)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
