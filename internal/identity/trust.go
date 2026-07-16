package identity

import (
	"crypto/ed25519"
	"fmt"
	"sort"

	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// Trust is the mesh-level membership/key set (FIX-F2 ruling 1). The
// permanent security log (genesis, device.add/revoke) is ONE stream class
// regardless of which origin's segments carry it (spec §4.5); Trust is the
// result of replay pass 1 over ALL origins. Pass 2 verifies each origin's
// stream against this set, so a device admitted in another origin's log
// (migration) verifies correctly.
type Trust struct {
	CairnID        string
	RootPub        ed25519.PublicKey
	GenesisEnv     *event.Envelope
	GenesisPayload *GenesisPayload

	keys    map[string]ed25519.PublicKey
	devices map[string]ed25519.PublicKey // device_id → pubkey
	revoked map[string]bool
}

// MeshTrust runs pass 1: a fixpoint walk of every origin with ONE shared
// chain verifier, learning the root key and every root-signed device cert
// wherever it lives. Origins whose keys are admitted by another origin's
// chain are retried until the set converges.
func MeshTrust(fsys fsx.FS, portableDir string) (*Trust, error) {
	origins, err := cairnlog.Origins(fsys, portableDir)
	if err != nil {
		return nil, err
	}
	v := NewChainVerifier()
	pending := map[cairnlog.Origin]bool{}
	for _, o := range origins {
		pending[o] = true
	}
	for len(pending) > 0 {
		progressed := false
		for o := range pending {
			if _, err := cairnlog.Walk(fsys, portableDir, o, v.Verify, nil); err == nil {
				delete(pending, o)
				progressed = true
			} else if !isKeyOrderErr(err) {
				return nil, fmt.Errorf("establishing mesh trust (origin %s/%d): %w", o.DeviceID, o.Generation, err)
			}
		}
		if !progressed {
			remaining := make([]string, 0, len(pending))
			for o := range pending {
				remaining = append(remaining, fmt.Sprintf("%s/%d", o.DeviceID, o.Generation))
			}
			sort.Strings(remaining)
			return nil, fmt.Errorf("mesh trust unresolved for origin(s) %v: no chain admits their keys", remaining)
		}
	}
	return v.Trust(), nil
}

func isKeyOrderErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "before genesis") || contains(msg, "unknown signing key")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Verifier returns the pass-2 verifier: a chain verifier PRE-SEEDED with
// the full mesh key set, so any origin's stream verifies standalone.
// Revocation never invalidates historical signatures (FIX-F2 ruling 3) —
// revoked devices' keys stay in the set; Revoked() gates WRITES only.
func (t *Trust) Verifier() cairnlog.VerifyFunc {
	v := &ChainVerifier{
		cairnID:    t.CairnID,
		rootPub:    t.RootPub,
		keys:       map[string]ed25519.PublicKey{},
		revoked:    map[string]bool{},
		devicePubs: map[string]ed25519.PublicKey{},
	}
	for k, pub := range t.keys {
		v.keys[k] = pub
	}
	for id, pub := range t.devices {
		v.devicePubs[id] = pub
	}
	for id := range t.revoked {
		v.revoked[id] = true
	}
	return v.Verify
}

// WithDevice returns a COPY of the trust with one additional admitted device.
// The cert MUST be root-signed — the caller verifies that (this only extends the
// membership set). Copy-on-write: the receiver is left unchanged, so any reader
// still holding it stays consistent; the caller swaps in the returned immutable
// snapshot under its own lock. Used for live trust refresh after admitting a
// paired device (P3-2d), so the sync listener accepts the new member without a
// daemon restart. On restart, MeshTrust rebuilds the identical set from the log.
func (t *Trust) WithDevice(cert DeviceCert) (*Trust, error) {
	pub, err := cert.DevicePublicKey()
	if err != nil {
		return nil, err
	}
	nt := &Trust{
		CairnID:        t.CairnID,
		RootPub:        t.RootPub,
		GenesisEnv:     t.GenesisEnv,
		GenesisPayload: t.GenesisPayload,
		keys:           make(map[string]ed25519.PublicKey, len(t.keys)+1),
		devices:        make(map[string]ed25519.PublicKey, len(t.devices)+1),
		revoked:        make(map[string]bool, len(t.revoked)),
	}
	for k, v := range t.keys {
		nt.keys[k] = v
	}
	for k, v := range t.devices {
		nt.devices[k] = v
	}
	for k, v := range t.revoked {
		nt.revoked[k] = v
	}
	nt.keys[event.KeyID(pub)] = pub
	nt.devices[cert.DeviceID] = pub
	return nt, nil
}

// Member reports whether a device was admitted (genesis or device.add).
func (t *Trust) Member(deviceID string) bool {
	_, ok := t.devices[deviceID]
	return ok
}

// Revoked reports whether a device.revoke exists for the device.
func (t *Trust) Revoked(deviceID string) bool { return t.revoked[deviceID] }

// DevicePub returns the registered public key for an ADMITTED device (N5
// handshake lookups). ok=false for unknown devices.
func (t *Trust) DevicePub(deviceID string) (ed25519.PublicKey, bool) {
	pub, ok := t.devices[deviceID]
	return pub, ok
}

// Devices lists admitted device ids, sorted.
func (t *Trust) Devices() []string {
	out := make([]string, 0, len(t.devices))
	for id := range t.devices {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
