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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// invitationTokenPrefix identifies + versions the single-string pairing token so
// a mis-pasted string fails fast rather than JSON-parsing into garbage.
const invitationTokenPrefix = "cairn-pair-v1."

// EncodeInvitation renders an invitation as one paste-able token string. The
// token embeds the device credential — it is a one-time SECRET (treat like a
// password): base64url(JSON) behind the version prefix.
func EncodeInvitation(inv *PairingInvitation) (string, error) {
	blob, err := json.Marshal(inv)
	if err != nil {
		return "", err
	}
	return invitationTokenPrefix + base64.RawURLEncoding.EncodeToString(blob), nil
}

// DecodeInvitation parses a token produced by EncodeInvitation.
func DecodeInvitation(token string) (*PairingInvitation, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, invitationTokenPrefix) {
		return nil, errors.New("not a cairn pairing invitation token (missing cairn-pair-v1 prefix)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, invitationTokenPrefix))
	if err != nil {
		return nil, fmt.Errorf("decoding invitation token: %w", err)
	}
	var inv PairingInvitation
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, fmt.Errorf("parsing invitation token: %w", err)
	}
	return &inv, nil
}

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

// PairingBootstrapName stores the invitation's identity chain device-locally so
// a paired node can authenticate the inviting peer BEFORE its own device.add
// replicates — the append-on-arrival analogue of Join's bootstrap-chain.json.
// Unlike a join grant it carries NO private key (the key lives in device.key)
// and does NOT assert this device's membership: the paired device is admitted on
// arrival, so at install time it is legitimately not yet in the chain.
const PairingBootstrapName = "pairing-bootstrap.json"

type pairingBootstrap struct {
	CairnID  string   `json:"cairn_id"`
	DeviceID string   `json:"device_id"`
	Chain    []string `json:"chain"` // base64 record_bytes, genesis first
}

// verifyMeshChain replays base64 chain records through a fresh verifier and
// returns the resulting genesis-rooted Trust for cairnID. It asserts the chain
// is genesis-rooted and matches the cairn, but NOT any particular device's
// membership — the caller adds that where it applies (VerifyGrantChain asserts
// the joining device; a pairing bootstrap must not, because the paired device is
// admitted on arrival, not carried in the chain).
func verifyMeshChain(chain []string, cairnID string) (*Trust, error) {
	v := NewChainVerifier()
	for i, b64 := range chain {
		rec, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("chain[%d]: %w", i, err)
		}
		if _, err := v.Verify(rec); err != nil {
			return nil, fmt.Errorf("chain[%d] failed verification: %w", i, err)
		}
	}
	t := v.Trust()
	if t.CairnID != cairnID {
		return nil, fmt.Errorf("chain cairn_id %s does not match %s", t.CairnID, cairnID)
	}
	if t.GenesisEnv == nil {
		return nil, errors.New("bootstrap chain has no genesis")
	}
	return t, nil
}

// PairingBootstrapTrust loads the pairing bootstrap chain (used by the daemon
// when a paired node has no local genesis yet — R37, append-on-arrival variant).
func PairingBootstrapTrust(deviceDir string) (*Trust, error) {
	blob, err := os.ReadFile(filepath.Join(deviceDir, PairingBootstrapName))
	if err != nil {
		return nil, err
	}
	var pb pairingBootstrap
	if err := json.Unmarshal(blob, &pb); err != nil {
		return nil, err
	}
	return verifyMeshChain(pb.Chain, pb.CairnID)
}

// PairJoinOptions parameterizes installing a paired identity on the new node.
type PairJoinOptions struct {
	Invitation       *PairingInvitation
	Dir              string // portable dir
	AllowUnencrypted bool
	Checker          VolumeChecker
	Now              func() time.Time
	Out              io.Writer
}

// PairJoinInstall verifies an invitation and installs the new node's identity
// from it. The device key travels IN the token (Option-1 tradeoff), so nothing
// is staged. It writes the portable config, device-local key/cert/config, and
// the pairing bootstrap chain — but appends NO device.add (that lands on the
// inviting node when the pairing handshake completes). It is idempotent: a
// re-run after a failed network handshake finds the matching identity already
// installed and skips the writes, so `cairn pair join` can be retried. Returns
// the device private key for the caller to drive the pairing handshake.
func PairJoinInstall(opts PairJoinOptions) (ed25519.PrivateKey, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	inv := opts.Invitation
	if inv == nil {
		return nil, errors.New("no invitation")
	}
	trust, priv, err := VerifyPairingInvitation(inv, opts.Now())
	if err != nil {
		return nil, err
	}
	// the cert must itself verify against the CHAIN's root (never trust invitation
	// fields the chain did not prove) — VerifyPairingInvitation already did, but
	// re-assert against the returned trust for defense in depth.
	if err := inv.Cert.Verify(trust.RootPub); err != nil {
		return nil, fmt.Errorf("invitation cert: %w", err)
	}

	deviceDir, err := config.DeviceStateDir(inv.CairnID)
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(deviceDir, config.DeviceKeyName)
	// idempotency: an existing key for this cairn must MATCH the invitation, else
	// this machine already holds a different identity for the mesh.
	if existing, lerr := LoadKey(keyPath); lerr == nil {
		if !existing.Public().(ed25519.PublicKey).Equal(priv.Public().(ed25519.PublicKey)) {
			return nil, fmt.Errorf("this machine already holds a DIFFERENT identity for cairn %s — refusing to overwrite", inv.CairnID)
		}
		fmt.Fprintf(out, "identity already installed for cairn %s (device %s) — proceeding to the pairing handshake\n", inv.CairnID, inv.Cert.DeviceID)
		return priv, nil
	}

	// portable config (same cairn id; the LOG arrives via first sync)
	if err := os.MkdirAll(opts.Dir, config.DirPerm); err != nil {
		return nil, err
	}
	portable := &config.PortableConfig{
		ConfigVersion: config.PortableConfigVersion,
		CairnID:       inv.CairnID,
		CreatedAt:     inv.CreatedAt,
	}
	if err := portable.SavePortable(opts.Dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(deviceDir, config.DirPerm); err != nil {
		return nil, err
	}
	// gate the volume holding device-local state BEFORE writing the key.
	if err := GateEncryption(deviceDir, opts.Checker, opts.AllowUnencrypted, out); err != nil {
		return nil, err
	}
	if err := SaveKey(keyPath, priv); err != nil {
		return nil, err
	}
	device := &config.DeviceConfig{
		ConfigVersion:    config.DeviceConfigVersion,
		CairnID:          inv.CairnID,
		DeviceID:         inv.Cert.DeviceID,
		OriginGeneration: config.FirstGeneration,
		CreatedAt:        inv.CreatedAt,
		AllowUnencrypted: opts.AllowUnencrypted,
		SyncListen:       config.SyncListenAuto,
	}
	if err := device.SaveDevice(deviceDir); err != nil {
		return nil, err
	}
	certBlob, err := json.MarshalIndent(inv.Cert, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := fsx.WriteFileAtomic(fsx.OS{}, filepath.Join(deviceDir, config.DeviceCertName), certBlob, config.KeyFilePerm); err != nil {
		return nil, err
	}
	// the pairing bootstrap chain (no private key; no self-membership assertion)
	pbBlob, err := json.MarshalIndent(pairingBootstrap{
		CairnID:  inv.CairnID,
		DeviceID: inv.Cert.DeviceID,
		Chain:    inv.Chain,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := fsx.WriteFileAtomic(fsx.OS{}, filepath.Join(deviceDir, PairingBootstrapName), pbBlob, config.KeyFilePerm); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "installed identity for cairn %s as device %s (%s) from the invitation\n", inv.CairnID, inv.Cert.DeviceID, inv.Cert.DisplayName)
	return priv, nil
}

// AddSyncPeer persists addr into the device-local sync_peers list for the
// cairn at portableDir (SYNC-C3: `pair join` proved the counterparty
// answers there — registering it makes pairing sufficient for replication,
// with no hand-edited TOML). Idempotent; validates host:port.
func AddSyncPeer(portableDir, addr string) error {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("invalid peer address %q (want host:port): %w", addr, err)
	}
	loaded, err := Load(portableDir)
	if err != nil {
		return err
	}
	for _, p := range loaded.Device.SyncPeers {
		if p == addr {
			return nil
		}
	}
	loaded.Device.SyncPeers = append(loaded.Device.SyncPeers, addr)
	sort.Strings(loaded.Device.SyncPeers)
	return loaded.Device.SaveDevice(loaded.DeviceDir)
}
