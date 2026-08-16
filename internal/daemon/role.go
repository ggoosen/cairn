package daemon

// P3-3 — thin-node role (spec §7). A node's role is device-local config; peers
// learn each other's role at the sync frontier exchange (runtime, non-replicated
// — like blob holdership, spec §4.5). The role has three offline-buildable
// consequences, all here:
//   - a thin node is NOT counted toward the durability target (below);
//   - a thin node is never advertised as a normal node (its frontier carries
//     its true role; myRole never lies);
//   - a thin node's universal search is partial (retrieve.go marks it).
// The live remote-query dependency is here; metered/battery awareness is P3-6
// (internal/netstate) and enters through meteredNow below.

import (
	"context"
	"fmt"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/netstate"
	"github.com/ggoosen/cairn/internal/rank"
)

// meteredNow reports whether this device should be treated as data-metered, and
// why. P3-6: the manual flag and the platform sensor are combined by OR, and
// ONLY by OR —
//
//	metered = configured metered  OR  sensed metered == Yes
//
// so sensing can withhold data spending the operator did not think to withhold,
// but can NEVER authorise spending the operator forbade, and an unreadable
// platform (Unknown) leaves behaviour exactly as it was before sensing existed.
// A sensed "No" is deliberately not a signal to spend: it is treated the same
// as Unknown.
func (d *Daemon) meteredNow() (bool, string) {
	configured := d.loaded != nil && d.loaded.Device != nil && d.loaded.Device.Metered
	if configured {
		return true, "configured: metered = true"
	}
	if d.power == nil {
		return false, "not metered (no sensor)"
	}
	st := d.power.Sense(context.Background())
	if st.Metered == netstate.Yes {
		return true, "sensed: " + st.Source
	}
	return false, "not metered (" + st.Source + ")"
}

// powerState returns the current sensed state for diagnostics (`cairn net`).
// Never a policy input on its own — meteredNow is.
func (d *Daemon) powerState() netstate.State {
	if d.power == nil {
		return netstate.State{Source: "no sensor"}
	}
	return d.power.Sense(context.Background())
}

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

// thinSearchPartialReason is the note attached to a thin node's search/digest
// output (spec §7: no offline universal-search guarantee). Empty when this node
// is full — a full node's universal search IS complete over local canonical +
// eager text.
func (d *Daemon) thinSearchPartialReason() string {
	if !d.selfIsThin() {
		return ""
	}
	base := "thin node: only a recent local window is searched — older material may exist on full nodes (spec §7); fetch it deliberately or query a full node"
	// P3-3e: explain WHY the local view wasn't supplemented when remote-query is
	// configured but metered suppressed it — otherwise the agent can't tell.
	// P3-6: the reason names WHICH input decided, so "I never set metered" and
	// "the platform says I am tethered" do not look the same.
	if d.loaded != nil && d.loaded.Device != nil && d.loaded.Device.RemoteQuery {
		if metered, why := d.meteredNow(); metered {
			base += " (remote query is configured but suppressed: the connection is metered — " + why + ")"
		}
	}
	return base
}

// shouldRemoteQuery reports whether this node should consult a full peer for
// universal search (P3-3d): a THIN node with remote-query opted in and at least
// one configured sync peer — and NOT on a metered connection (P3-3e: a metered
// thin node does not auto-spend data on remote query). A full node never
// remote-queries. P3-6: "metered" is now the configured flag OR a positive
// platform reading, so a device that becomes metered stops spending data
// without the operator editing config.
func (d *Daemon) shouldRemoteQuery() bool {
	if !d.selfIsThin() ||
		d.loaded == nil || d.loaded.Device == nil ||
		!d.loaded.Device.RemoteQuery ||
		len(d.loaded.Device.SyncPeers) == 0 {
		return false
	}
	metered, _ := d.meteredNow()
	return !metered
}

// firstSyncPeer is the peer a thin node asks for remote search ("" if none).
func (d *Daemon) firstSyncPeer() string {
	if d.loaded == nil || d.loaded.Device == nil || len(d.loaded.Device.SyncPeers) == 0 {
		return ""
	}
	return d.loaded.Device.SyncPeers[0]
}

// maybeRemoteConsult, on a thin node with remote-query enabled, asks a full peer
// to run the search and returns its complete, budget-bounded result marked
// remote-sourced. Best-effort: any failure returns nil so the caller keeps its
// local (partial) result. It returns the peer's SINGLE budgeted payload — never
// a merge of two — so the agent's budget invariant (R19) holds.
func (d *Daemon) maybeRemoteConsult(local *SearchOutput, opts SearchOptions) *SearchOutput {
	if !d.shouldRemoteQuery() {
		return nil
	}
	addr := d.firstSyncPeer()
	if addr == "" {
		return nil
	}
	spec, serr := rank.NewSpec(opts.BudgetChars, opts.BudgetTokens)
	if serr != nil {
		return nil // already refused upstream; never re-derive a budget here
	}
	spec.Ceiling = opts.BudgetCeilingChars
	remote, err := d.RemoteSearch(addr, opts.Query, spec)
	if err != nil || remote == nil {
		fmt.Fprintf(d.warn, "remote query to %s failed (%v) — returning local recent results only (partial)\n", addr, err)
		return nil
	}
	// the full peer's view is complete over the mesh; mark provenance and keep the
	// caller's interaction id so telemetry stays coherent.
	remote.RemoteSource = addr
	remote.InteractionID = local.InteractionID
	remote.Partial = false
	remote.PartialReason = ""
	return remote
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
