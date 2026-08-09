package daemon

// SYNC-C1: live peer management. sync_peers was previously read-only from
// the device TOML — no verb wrote it, so two freshly-paired machines did
// not sync until the operator hand-edited config-device.toml and restarted
// the daemon. peer-add/peer-remove/peer-list mutate the live daemon AND
// persist, and the anti-entropy loop runs whenever a transport exists, so
// an added peer replicates without a restart.

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// validPeerAddr validates a host:port sync peer address.
func validPeerAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid peer address %q (want host:port): %w", addr, err)
	}
	if strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return fmt.Errorf("invalid peer address %q (empty host or port)", addr)
	}
	return nil
}

// PeerAdd registers a sync peer: live in-memory (the next sweep dials it)
// and persisted to the device TOML (it survives restart). Idempotent.
func (d *Daemon) PeerAdd(addr string) ([]string, error) {
	if err := validPeerAddr(addr); err != nil {
		return nil, err
	}
	if d.loaded.Device == nil {
		return nil, fmt.Errorf("read-only restore has no device config — peers are per-device state")
	}
	d.mu.Lock()
	for _, p := range d.loaded.Device.SyncPeers {
		if p == addr {
			peers := append([]string(nil), d.loaded.Device.SyncPeers...)
			d.mu.Unlock()
			return peers, nil
		}
	}
	d.loaded.Device.SyncPeers = append(d.loaded.Device.SyncPeers, addr)
	sort.Strings(d.loaded.Device.SyncPeers)
	peers := append([]string(nil), d.loaded.Device.SyncPeers...)
	err := d.loaded.Device.SaveDevice(d.loaded.DeviceDir)
	d.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("peer added live but persisting device config failed (it will NOT survive restart): %w", err)
	}
	d.kickSync()
	return peers, nil
}

// PeerRemove drops a sync peer live and persisted.
func (d *Daemon) PeerRemove(addr string) ([]string, error) {
	if d.loaded.Device == nil {
		return nil, fmt.Errorf("read-only restore has no device config — peers are per-device state")
	}
	d.mu.Lock()
	kept := d.loaded.Device.SyncPeers[:0]
	found := false
	for _, p := range d.loaded.Device.SyncPeers {
		if p == addr {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	if !found {
		peers := append([]string(nil), d.loaded.Device.SyncPeers...)
		d.mu.Unlock()
		return peers, fmt.Errorf("peer %q is not configured", addr)
	}
	d.loaded.Device.SyncPeers = kept
	peers := append([]string(nil), kept...)
	err := d.loaded.Device.SaveDevice(d.loaded.DeviceDir)
	d.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("peer removed live but persisting device config failed: %w", err)
	}
	return peers, nil
}

// PeerList returns the configured sync peers.
func (d *Daemon) PeerList() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loaded.Device == nil {
		return nil
	}
	return append([]string(nil), d.loaded.Device.SyncPeers...)
}
