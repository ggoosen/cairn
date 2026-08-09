package daemon_test

// SYNC-C tests: peer management is live (no restart) and persisted.

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestPeerAddRemoveListPersist(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	for _, bad := range []string{"", "no-port", ":", "host:", ":9"} {
		if _, err := d.PeerAdd(bad); err == nil {
			t.Fatalf("PeerAdd(%q) accepted an invalid address", bad)
		}
	}

	if _, err := d.PeerAdd("100.64.0.7:7473"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PeerAdd("100.64.0.7:7473"); err != nil {
		t.Fatal("re-adding an existing peer must be idempotent")
	}
	if _, err := d.PeerAdd("100.64.0.8:7473"); err != nil {
		t.Fatal(err)
	}
	if got := d.PeerList(); len(got) != 2 {
		t.Fatalf("PeerList = %v, want 2 peers", got)
	}

	// persisted: a fresh daemon (fresh config load) sees both peers
	d.Close()
	d2 := startDaemon(t, dir)
	if got := d2.PeerList(); len(got) != 2 {
		t.Fatalf("peers did not survive restart: %v", got)
	}

	if _, err := d2.PeerRemove("100.64.0.7:7473"); err != nil {
		t.Fatal(err)
	}
	if _, err := d2.PeerRemove("100.64.0.7:7473"); err == nil {
		t.Fatal("removing an absent peer must error")
	}
	d2.Close()
	d3 := startDaemon(t, dir)
	if got := d3.PeerList(); len(got) != 1 || got[0] != "100.64.0.8:7473" {
		t.Fatalf("remove did not persist: %v", got)
	}
}

// A peer added LIVE replicates without a daemon restart: the anti-entropy
// loop now runs whenever a transport exists and re-reads the peer list per
// sweep; PeerAdd kicks it.
func TestPeerAddLiveReplication(t *testing.T) {
	p := setupN6Pair(t)
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, io.Discard)
	defer cancelA()
	defer dA.Close()
	dB, cancelB, _ := startSyncNode(t, p.baseB, p.dirB, io.Discard)
	defer cancelB()
	defer dB.Close()

	if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "live peer pickup drill"}); err != nil {
		t.Fatal(err)
	}
	if n := searchCount(t, dB, "pickup drill"); n != 0 {
		t.Fatalf("B already has A's message before any peer was configured (%d hits) — test premise broken", n)
	}

	if _, err := dB.PeerAdd(addrA); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for searchCount(t, dB, "pickup drill") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("B never converged after live peer-add (no restart)")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The IPC surface: peer mutation is operator-tier; a read-only capability
// session can list but not add.
func TestPeerOpsCapability(t *testing.T) {
	_, deviceDir := serveDaemon(t)
	resp, err := daemon.Call(deviceDir, daemon.Request{
		Op: "session-create", SessionProfile: "read-only", SessionName: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	token := resp.Status["session"].(string)

	if _, err := daemon.Call(deviceDir, daemon.Request{Op: "peer-add", Peer: "100.64.0.9:7473", Session: token}); err == nil ||
		!strings.Contains(err.Error(), "capability") {
		t.Fatalf("read-only session added a peer: %v", err)
	}
	if _, err := daemon.Call(deviceDir, daemon.Request{Op: "peer-list", Session: token}); err != nil {
		t.Fatalf("peer-list should be read-tier: %v", err)
	}
	if _, err := daemon.Call(deviceDir, daemon.Request{Op: "peer-add", Peer: "100.64.0.9:7473"}); err != nil {
		t.Fatalf("operator peer-add: %v", err)
	}
	resp, err = daemon.Call(deviceDir, daemon.Request{Op: "peer-list"})
	if err != nil || len(resp.Peers) != 1 {
		t.Fatalf("peer-list after add: %+v %v", resp, err)
	}
}

// SYNC-C3: the helper `pair join` uses to persist its counterparty.
func TestAddSyncPeerPersists(t *testing.T) {
	dir := initCairn(t)
	if err := identity.AddSyncPeer(dir, "not-an-addr"); err == nil {
		t.Fatal("invalid address accepted")
	}
	if err := identity.AddSyncPeer(dir, "100.64.0.5:7473"); err != nil {
		t.Fatal(err)
	}
	if err := identity.AddSyncPeer(dir, "100.64.0.5:7473"); err != nil {
		t.Fatal("must be idempotent")
	}
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Device.SyncPeers) != 1 || loaded.Device.SyncPeers[0] != "100.64.0.5:7473" {
		t.Fatalf("sync_peers = %v", loaded.Device.SyncPeers)
	}
}
