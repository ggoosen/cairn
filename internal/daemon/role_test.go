package daemon

// P3-3 unit coverage: durability excludes thin nodes; role tracking is correct.

import (
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestCountDurabilityMembersExcludesThinAndRevoked(t *testing.T) {
	devices := []string{"a", "b", "c", "d"}
	revoked := func(id string) bool { return id == "d" }
	thin := func(id string) bool { return id == "c" }
	// a,b full; c thin; d revoked → 2 durability-backing members
	if n := countDurabilityMembers(devices, revoked, thin); n != 2 {
		t.Fatalf("got %d durability members, want 2", n)
	}
	// everyone thin or revoked still floors at 1 (never a zero target)
	all := func(string) bool { return true }
	none := func(string) bool { return false }
	if n := countDurabilityMembers(devices, none, all); n != 1 {
		t.Fatalf("floor: got %d, want 1", n)
	}
}

func newRoleDaemon(selfID, selfRole string) *Daemon {
	return &Daemon{
		loaded: &identity.Loaded{
			Device: &config.DeviceConfig{DeviceID: selfID, Role: selfRole},
		},
	}
}

func TestMyRoleNeverLies(t *testing.T) {
	if r := newRoleDaemon("self", config.RoleThin).myRole(); r != config.RoleThin {
		t.Fatalf("thin node reported role %q", r)
	}
	// unset role reads as full
	if r := newRoleDaemon("self", "").myRole(); r != config.RoleFull {
		t.Fatalf("unset role reported %q, want full", r)
	}
}

func TestDeviceIsThinTracksSelfAndPeers(t *testing.T) {
	d := newRoleDaemon("self", config.RoleThin)
	// self is thin from config
	if !d.deviceIsThin("self") {
		t.Fatal("self (thin) not reported thin")
	}
	// an unknown peer defaults to full (not thin) — conservative durability
	if d.deviceIsThin("peer-x") {
		t.Fatal("unknown peer defaulted to thin")
	}
	// learn a peer's advertised roles
	d.recordPeerRole("peer-thin", config.RoleThin)
	d.recordPeerRole("peer-full", config.RoleFull)
	if !d.deviceIsThin("peer-thin") {
		t.Fatal("advertised-thin peer not thin")
	}
	if d.deviceIsThin("peer-full") {
		t.Fatal("advertised-full peer read as thin")
	}
	// a role never gets stuck thin: re-advertising full clears it
	d.recordPeerRole("peer-thin", config.RoleFull)
	if d.deviceIsThin("peer-thin") {
		t.Fatal("peer stuck thin after re-advertising full")
	}
}
