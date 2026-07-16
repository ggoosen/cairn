package daemon

// P3-3 — thin-node role (spec §7). A node's role is device-local config; peers
// learn each other's role at the sync frontier exchange (runtime, non-replicated
// — like blob holdership, spec §4.5). The role has three offline-buildable
// consequences, all here:
//   - a thin node is NOT counted toward the durability target (below);
//   - a thin node is never advertised as a normal node (its frontier carries
//     its true role; myRole never lies);
//   - a thin node's universal search is partial (retrieve.go marks it).
// The live remote-query dependency and battery/metered awareness are deferred
// (hardware-gated).

import "github.com/ggoosen/cairn/internal/config"

// selfIsThin reports whether THIS node is thin. A read-only restore has no
// device-local identity (d.loaded.Device == nil) — it is treated as full.
func (d *Daemon) selfIsThin() bool {
	return d.loaded != nil && d.loaded.Device != nil && d.loaded.Device.IsThin()
}

// myRole returns this node's configured role (RoleFull default). A thin node
// NEVER reports full — it is never advertised as a normal node (spec §7).
func (d *Daemon) myRole() string {
	if d.selfIsThin() {
		return config.RoleThin
	}
	return config.RoleFull
}

// recordPeerRole stores a peer's advertised role (runtime). An empty/unknown
// role is treated as full (the conservative default — it keeps the peer in the
// durability target rather than silently dropping it). Caller holds d.mu.
func (d *Daemon) recordPeerRole(deviceID, role string) {
	if deviceID == "" {
		return
	}
	if d.peerRoles == nil {
		d.peerRoles = map[string]string{}
	}
	if role == config.RoleThin {
		d.peerRoles[deviceID] = config.RoleThin
	} else {
		// full/unknown: record explicitly so a role never gets "stuck" thin
		d.peerRoles[deviceID] = config.RoleFull
	}
}

// deviceIsThin reports whether a device is known to be a thin node — self (from
// config) or a peer (from its advertised role). Caller holds d.mu.
func (d *Daemon) deviceIsThin(deviceID string) bool {
	if d.loaded != nil && d.loaded.Device != nil && deviceID == d.loaded.Device.DeviceID {
		return d.loaded.Device.IsThin()
	}
	return d.peerRoles[deviceID] == config.RoleThin
}

// countDurabilityMembers counts the members that back durability: non-revoked
// FULL nodes. A thin node is excluded from the target (spec §7: "not counted
// toward durability unless it actually holds the object" — actual holdership is
// tracked separately by blobHolderCount, so excluding thin nodes from the target
// is correct and never double-counts). Floors at 1.
func countDurabilityMembers(devices []string, revoked, thin func(string) bool) int {
	n := 0
	for _, id := range devices {
		if revoked(id) || thin(id) {
			continue
		}
		n++
	}
	if n < 1 {
		n = 1
	}
	return n
}
