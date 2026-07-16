package daemon

// P3-2b — append-on-arrival admission for one-time pairing invitations
// (operator RULING 2026-07-16, Option 1). A new node presents a pre-signed
// pairing credential (a root-signed device cert minted offline, P3-2a); a LIVE
// inviting node validates it and durably appends the device.add that admits it.
//
// The append reuses the daemon's own event path — device.add signed by THIS
// device's key, the root authority carried inside the cert — exactly as the
// offline Approve ceremony, but with NO root key on the live node (the cert was
// root-signed at mint). This adds no new trust path to the chain verifier and no
// delegation (P4 stays out): on recovery the device.add verifies precisely as an
// Approve-minted one does.
//
// HARD single-use: a given invitation admits at most one device.add per node —
// d.trust.Member catches cross-restart replays (the durable device.add is in the
// recovered trust), and the session-scoped admittedPairings set catches replays
// within one daemon lifetime (before a restart folds the device.add into
// d.trust). Cross-node double-admit of the SAME identity is benign: it is the
// same key, not a clone, and converges to a redundant device.add.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/identity"
)

// servePair is the daemon's peer.Server.OnPair callback (P3-2c). It unmarshals
// the opaque pairing payload ({cert, invite_id}), re-verifies the cert against
// OUR mesh root, and returns the device public key to challenge plus an admit
// closure that appends the device.add once the dialer proves key possession.
// The payload deliberately carries no private key — key-possession is proven by
// the peer-layer challenge, so the secret never reaches this node.
func (d *Daemon) servePair(payload []byte) (ed25519.PublicKey, func() (string, error), error) {
	var req struct {
		Cert     identity.DeviceCert `json:"cert"`
		InviteID string              `json:"invite_id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, nil, fmt.Errorf("pairing payload: %w", err)
	}
	if req.InviteID == "" {
		return nil, nil, errors.New("pairing payload missing invite_id")
	}
	d.mu.Lock()
	trust := d.trust
	d.mu.Unlock()
	if trust == nil {
		return nil, nil, errors.New("no mesh trust")
	}
	// re-verify against our own root — never trust the dialer's word (defense in
	// depth; AdmitPairedDevice re-checks under the write lock too).
	if err := req.Cert.Verify(trust.RootPub); err != nil {
		return nil, nil, fmt.Errorf("pairing cert failed root verification: %w", err)
	}
	pub, err := req.Cert.DevicePublicKey()
	if err != nil {
		return nil, nil, err
	}
	cert, inviteID := req.Cert, req.InviteID
	admit := func() (string, error) { return d.AdmitPairedDevice(cert, inviteID) }
	return pub, admit, nil
}

// AdmitPairedDevice validates a pairing credential and durably appends the
// device.add admitting it, returning the new device.add event id. It enforces,
// in order: root-signature (re-verified against OUR mesh root — peer-supplied
// data is never trusted), expiry (anchored in the cert's issued_at, unforgeable),
// and single-use. The device.add is durable before this returns (the daemon's
// append path fsyncs); the new device becomes usable on this node after the
// standard membership-takes-effect-on-restart path (as every membership change
// does today) unless a later step refreshes live trust.
func (d *Daemon) AdmitPairedDevice(cert identity.DeviceCert, inviteID string) (string, error) {
	if err := d.writable(); err != nil {
		return "", err
	}
	if inviteID == "" {
		return "", errors.New("pairing admission requires an invite id")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lg == nil {
		return "", errors.New("daemon is closed")
	}
	if d.trust == nil {
		return "", errors.New("no mesh trust")
	}

	// 1. the cert must be genuinely root-signed for this device against OUR root.
	// Never trust the peer's word — re-verify with the root the log proved.
	if err := cert.Verify(d.trust.RootPub); err != nil {
		return "", fmt.Errorf("pairing cert failed root verification: %w", err)
	}
	// 2. expiry: cert.issued_at + PairingInviteTTL. The anchor is inside the
	// root-signed cert, so a peer cannot extend the window.
	issued, err := time.Parse(config.WallTimeFormat, cert.IssuedAt)
	if err != nil {
		return "", fmt.Errorf("pairing cert issued_at: %w", err)
	}
	expiry := issued.Add(config.PairingInviteTTL)
	if !d.now().Before(expiry) {
		return "", fmt.Errorf("pairing invitation expired at %s (minted %s, TTL %s) — mint a fresh one",
			expiry.UTC().Format(config.WallTimeFormat), cert.IssuedAt, config.PairingInviteTTL)
	}
	// 3. single-use / already-known device.
	if d.trust.Member(cert.DeviceID) || d.trust.Revoked(cert.DeviceID) {
		return "", fmt.Errorf("device %s is already admitted or revoked (pairing is single-use)", cert.DeviceID)
	}
	if d.admittedPairings[inviteID] || d.admittedPairings["dev:"+cert.DeviceID] {
		return "", fmt.Errorf("pairing invitation %s was already consumed (single-use)", inviteID)
	}

	// 4. durable append: device.add on OUR active origin, signed by OUR key; the
	// root authority is the cert inside (same shape as Approve, so requestAlreadyUsed
	// dedup and the chain verifier both work unchanged).
	payload := map[string]any{"cert": cert, "enrolment_request_id": inviteID}
	env, rec, err := d.buildEvent("device.add", "device", cert.DeviceID, payload, PublishRequest{Actor: "operator"})
	if err != nil {
		return "", err
	}
	if err := d.lg.Append(rec, env); err != nil {
		return "", err
	}
	d.applyProjection(env, rec)

	if d.admittedPairings == nil {
		d.admittedPairings = map[string]bool{}
	}
	d.admittedPairings[inviteID] = true
	d.admittedPairings["dev:"+cert.DeviceID] = true
	d.kickSync()
	return env.EventID, nil
}
