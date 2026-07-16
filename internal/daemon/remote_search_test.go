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

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/peer"
)

// setupPairedPair inits mesh-owner A (with the given role) on a loopback
// listener + some content, pairs a member node B into A's mesh, starts B's
// daemon, and returns (B's daemon, A's sync addr). A and B use separate
// device-state bases.
func setupPairedPair(t *testing.T, ownerRole string) (*daemon.Daemon, string) {
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
	if _, err := peer.PairDial(addr, inv.CairnID, payload, priv); err != nil {
		t.Fatalf("pair: %v", err)
	}
	dB := startDaemon(t, dirB)
	return dB, addr
}

func TestP33cRemoteSearchAgainstFullNode(t *testing.T) {
	dB, addr := setupPairedPair(t, "") // owner A is a full node

	out, err := dB.RemoteSearch(addr, "roastery approval", 2000)
	if err != nil {
		t.Fatalf("remote search: %v", err)
	}
	if out == nil || len(out.Results) == 0 {
		t.Fatalf("remote search returned no results: %+v", out)
	}
}

func TestP33cThinNodeRefusesRemoteSearch(t *testing.T) {
	dB, addr := setupPairedPair(t, "thin") // owner A is a THIN node

	if _, err := dB.RemoteSearch(addr, "roastery approval", 2000); err == nil ||
		!strings.Contains(err.Error(), "thin") {
		t.Fatalf("thin node did not refuse remote search: %v", err)
	}
}
