package identity

// P3-2 — one-time pairing invitations (operator RULING 2026-07-16, Option 1:
// pre-signed credential, append-on-arrival).
//
// The N5 enrol ceremony shuttles THREE files (request → grant) and restores the
// root key at APPROVE time while the new node waits. A pairing invitation
// collapses that to ONE offline root ceremony that mints a single bearer token;
// onboarding is then `cairn pair join <token> <peer>` with no further operator
// action. The token carries a root-signed device credential (cert + its private
// key), so NO root key is ever needed on a live node — the cert is already
// root-signed offline (reusing the existing verifier path unchanged; no
// delegation, so P4 stays untouched). The device.add is appended by the live
// inviting node when the new node arrives (P3-2b), giving HARD single-use.
//
// The invitation is a one-time, expiring, high-entropy SECRET (spec §336):
// the 256-bit Ed25519 device key is the entropy; expiry is anchored in the
// root-signed cert's issued_at, so it cannot be forged. Treat the token as a
// credential — anyone holding it before it expires and is consumed can pair.

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// PairingInvitation is the single bearer token the operator hands to a new node.
type PairingInvitation struct {
	Version   int        `json:"v"`          // invitation format version (1)
	CairnID   string     `json:"cairn_id"`   // the mesh this admits into
	CreatedAt string     `json:"created_at"` // mesh creation time (portable config)
	InviteID  string     `json:"invite_id"`  // UUIDv7 — the single-use marker recorded in device.add on arrival (R28 lineage)
	Cert      DeviceCert `json:"cert"`       // root-signed; binds device_id + pubkey; issued_at is the expiry anchor
	// DevicePrivB64 is the SECRET: the device's full Ed25519 private key. It is
	// the reason the token must be treated as a credential and never logged.
	DevicePrivB64 string `json:"device_priv_b64"`
	// Chain is the verifiable identity chain (genesis + every device.* record) up
	// to the minting moment — but NOT this device's device.add, which is appended
	// on arrival. The new node trusts NOTHING it did not verify from genesis.
	Chain []string `json:"chain"`
}

// MintPairingInvitationOptions parameterizes the offline root ceremony.
type MintPairingInvitationOptions struct {
	Dir         string // portable dir of the minting (primary) node
	RootKeyPath string // where the operator RESTORED the root key
	DisplayName string
	Now         func() time.Time
	FS          fsx.FS
	Out         io.Writer
}

// MintPairingInvitation performs the ONE offline root ceremony: it generates a
// fresh device keypair, root-signs the device certificate, and packages the
// credential + verifiable chain into a single invitation. It appends NOTHING to
// the log (the device.add is appended on arrival, P3-2b), so it takes no daemon
// write lock — but it needs the root key, which is an operator/offline action.
//
// RULING-NEEDED (recorded, resolved 2026-07-16 as Option 1): the device private
// key is generated here and travels inside the invitation, reversing the N5
// "the private key never moves" property. This is the accepted tradeoff for a
// frictionless one-time invitation; the mitigations are expiry (PairingInviteTTL,
// anchored in the root-signed cert) + hard single-use on arrival.
func MintPairingInvitation(opts MintPairingInvitationOptions) (*PairingInvitation, error) {
	if opts.FS == nil {
		opts.FS = fsx.OS{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}

	loaded, err := Load(opts.Dir)
	if err != nil {
		return nil, err
	}
	rootPriv, err := LoadKey(opts.RootKeyPath)
	if err != nil {
		return nil, fmt.Errorf("root key (restore it from offline storage first): %w", err)
	}
	rootPub := rootPriv.Public().(ed25519.PublicKey)

	chain, trust, err := identityChain(opts.FS, opts.Dir)
	if err != nil {
		return nil, err
	}
	if !rootPub.Equal(trust.RootPub) {
		return nil, errors.New("restored key is NOT this mesh's root key")
	}

	pub, priv, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	devUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	inviteUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	now := opts.Now().UTC().Format(config.WallTimeFormat)
	cert := DeviceCert{
		DeviceID:    devUUID.String(),
		Pubkey:      base64.StdEncoding.EncodeToString(pub),
		Generation:  config.FirstGeneration,
		IssuedAt:    now, // the expiry anchor (now + PairingInviteTTL)
		DisplayName: opts.DisplayName,
	}
	if err := cert.SignCert(rootPriv); err != nil {
		return nil, err
	}

	inv := &PairingInvitation{
		Version:       1,
		CairnID:       loaded.Portable.CairnID,
		CreatedAt:     loaded.Portable.CreatedAt,
		InviteID:      inviteUUID.String(),
		Cert:          cert,
		DevicePrivB64: base64.StdEncoding.EncodeToString(priv),
	}
	for _, r := range chain {
		inv.Chain = append(inv.Chain, base64.StdEncoding.EncodeToString(r))
	}
	fmt.Fprintf(opts.Out, "minted pairing invitation for device %s (%s), expires %s — carry it to the new node, then REMOVE the restored root key\n",
		cert.DeviceID, opts.DisplayName, inv.expiresAt())
	return inv, nil
}

// expiresAt is the invitation's expiry: the cert's issued_at + PairingInviteTTL.
// It returns the empty string if the cert time is unparseable (VerifyPairingInvitation
// surfaces that as an error).
func (inv *PairingInvitation) expiresAt() string {
	t, err := time.Parse(config.WallTimeFormat, inv.Cert.IssuedAt)
	if err != nil {
		return ""
	}
	return t.Add(config.PairingInviteTTL).UTC().Format(config.WallTimeFormat)
}

// VerifyPairingInvitation validates an invitation against nothing but genesis:
// it replays the chain through a fresh verifier, checks the cert's root
// signature against the CHAIN's root, confirms the carried private key matches
// the cert, and enforces expiry. It returns the verified mesh Trust (which does
// NOT yet include this device — device.add is appended on arrival) and the
// device private key. The device is NOT yet a member; that is the caller's cue
// to complete the live pairing handshake (P3-2b).
func VerifyPairingInvitation(inv *PairingInvitation, now time.Time) (*Trust, ed25519.PrivateKey, error) {
	if inv.Version != 1 {
		return nil, nil, fmt.Errorf("unsupported pairing invitation version %d", inv.Version)
	}
	v := NewChainVerifier()
	for i, b64 := range inv.Chain {
		rec, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, nil, fmt.Errorf("invitation chain[%d]: %w", i, err)
		}
		if _, err := v.Verify(rec); err != nil {
			return nil, nil, fmt.Errorf("invitation chain[%d] failed verification: %w", i, err)
		}
	}
	t := v.Trust()
	if t.CairnID != inv.CairnID {
		return nil, nil, fmt.Errorf("invitation cairn_id %s does not match the verified chain (%s)", inv.CairnID, t.CairnID)
	}
	// the cert must be genuinely root-signed for this device (existing trust path)
	if err := inv.Cert.Verify(t.RootPub); err != nil {
		return nil, nil, fmt.Errorf("invitation cert: %w", err)
	}
	// the carried private key must match the certified public key
	privRaw, err := base64.StdEncoding.DecodeString(inv.DevicePrivB64)
	if err != nil {
		return nil, nil, fmt.Errorf("invitation device key: %w", err)
	}
	if len(privRaw) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("invitation device key has wrong length %d", len(privRaw))
	}
	priv := ed25519.PrivateKey(privRaw)
	certPub, err := inv.Cert.DevicePublicKey()
	if err != nil {
		return nil, nil, err
	}
	if !priv.Public().(ed25519.PublicKey).Equal(certPub) {
		return nil, nil, errors.New("invitation private key does not match the certificate")
	}
	// the device must NOT already be admitted/revoked by the verified chain — it
	// is admitted on arrival, so seeing it here means a malformed/replayed chain
	if t.Member(inv.Cert.DeviceID) {
		return nil, nil, fmt.Errorf("invitation device %s is already admitted by the chain (not a fresh pairing)", inv.Cert.DeviceID)
	}
	// expiry: anchored in the root-signed cert's issued_at, so unforgeable
	issued, err := time.Parse(config.WallTimeFormat, inv.Cert.IssuedAt)
	if err != nil {
		return nil, nil, fmt.Errorf("invitation cert issued_at: %w", err)
	}
	if !now.Before(issued.Add(config.PairingInviteTTL)) {
		return nil, nil, fmt.Errorf("pairing invitation expired at %s (minted %s, TTL %s) — mint a fresh one",
			issued.Add(config.PairingInviteTTL).UTC().Format(config.WallTimeFormat), inv.Cert.IssuedAt, config.PairingInviteTTL)
	}
	return t, priv, nil
}
