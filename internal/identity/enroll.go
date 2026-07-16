package identity

// N5 — the two-node enrolment ceremony (buildpack; RULINGS.md R27/R28).
//
//	new node:        cairn device enroll   → request file (pubkey travels;
//	                                          the private key NEVER moves)
//	signing machine: operator restores the root key from offline storage,
//	                 cairn device approve  → root-signed device.add appended
//	                                          to the local origin + a grant
//	                                          file; operator REMOVES the key
//	new node:        cairn device join     → verifies the chain from genesis,
//	                                          installs its identity
//
// R28: requests expire (1h default) and are SINGLE-USE — the request id is
// recorded in the device.add payload and re-approval is refused.
//
// All ceremony steps are OFFLINE operations (daemon stopped), mirroring
// `cairn migrate`: the write lock is held for the append, and the root key
// touches disk only where the operator explicitly restored it.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// EnrollRequest is the file the operator carries to the signing machine.
type EnrollRequest struct {
	RequestID    string `json:"request_id"` // UUIDv7; single-use marker (R28)
	DisplayName  string `json:"display_name,omitempty"`
	DevicePubkey string `json:"device_pubkey"` // base64 Ed25519
	RequestedAt  string `json:"requested_at"`
	ExpiresAt    string `json:"expires_at"` // R28: default 1h
}

// enrollStagingDir holds the new node's private key until join completes.
func enrollStagingDir(requestID string) (string, error) {
	base, err := config.DeviceStateBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "enroll-pending", requestID), nil
}

// EnrollRequestOptions parameterizes the new-node enrolment request.
type EnrollRequestOptions struct {
	DisplayName      string
	OutPath          string
	AllowUnencrypted bool // FIX-G3: operator override for an unencrypted volume
	Checker          VolumeChecker
	Now              func() time.Time
	Out              io.Writer
}

// CreateEnrollRequest generates the new node's device keypair (private key
// staged locally, never in the request) and writes the request file. FIX-G3:
// the encryption gate fires BEFORE the private key is written to staging — a
// device key never lands on unencrypted storage.
func CreateEnrollRequest(opts EnrollRequestOptions) (*EnrollRequest, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	now := opts.Now()
	reqUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	pub, priv, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	req := &EnrollRequest{
		RequestID:    reqUUID.String(),
		DisplayName:  opts.DisplayName,
		DevicePubkey: base64.StdEncoding.EncodeToString(pub),
		RequestedAt:  now.UTC().Format(config.WallTimeFormat),
		ExpiresAt:    now.Add(config.EnrollRequestTTL).UTC().Format(config.WallTimeFormat),
	}
	staging, err := enrollStagingDir(req.RequestID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(staging, config.DirPerm); err != nil {
		return nil, err
	}
	// FIX-G3: gate the volume holding the staged key BEFORE writing it.
	if err := GateEncryption(staging, opts.Checker, opts.AllowUnencrypted, opts.Out); err != nil {
		return nil, err
	}
	if err := SaveKey(filepath.Join(staging, config.DeviceKeyName), priv); err != nil {
		return nil, err
	}
	blob, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(opts.OutPath, blob, 0o600); err != nil {
		return nil, err
	}
	return req, nil
}

// JoinGrant is the file the operator carries back to the new node: the
// assigned identity plus the VERIFIABLE identity chain (genesis + every
// device.* record) so the new node can authenticate peers before any
// replication exists (N6 replaces this bootstrap with real segments).
type JoinGrant struct {
	CairnID     string     `json:"cairn_id"`
	RequestID   string     `json:"request_id"`
	DeviceID    string     `json:"device_id"`
	DisplayName string     `json:"display_name,omitempty"`
	CreatedAt   string     `json:"created_at"` // mesh creation time (portable config)
	Cert        DeviceCert `json:"cert"`       // the root-signed device certificate
	Chain       []string   `json:"chain"`      // base64 record_bytes, genesis first, admit order
}

// ApproveOptions parameterizes the signing-machine step.
type ApproveOptions struct {
	Dir         string // portable dir of the SIGNING machine's mesh
	RequestPath string
	RootKeyPath string // where the operator RESTORED the root key
	GrantPath   string // output
	Out         io.Writer
	Now         func() time.Time
	FS          fsx.FS
}

// identityChain walks every origin and returns the genesis + device.*
// records in admit order (the same fixpoint MeshTrust uses).
func identityChain(fsys fsx.FS, portableDir string) ([][]byte, *Trust, error) {
	trust, err := MeshTrust(fsys, portableDir)
	if err != nil {
		return nil, nil, err
	}
	origins, err := cairnlog.Origins(fsys, portableDir)
	if err != nil {
		return nil, nil, err
	}
	// fixpoint over origins with a fresh verifier, capturing records in the
	// order they are admitted — that order is replayable by the new node
	v := NewChainVerifier()
	var chain [][]byte
	captured := map[string]bool{}
	remaining := origins
	for len(remaining) > 0 {
		progressed := false
		var next []cairnlog.Origin
		for _, o := range remaining {
			_, err := cairnlog.Walk(fsys, portableDir, o, v.Verify, func(env *event.Envelope, rec []byte) error {
				switch env.EventType {
				case "cairn.genesis", "device.add", "device.revoke":
					if !captured[env.EventID] {
						captured[env.EventID] = true
						chain = append(chain, append([]byte(nil), rec...))
					}
				}
				return nil
			})
			if err != nil {
				next = append(next, o)
				continue
			}
			progressed = true
		}
		if !progressed {
			return nil, nil, errors.New("identity chain does not converge (unverifiable origin)")
		}
		remaining = next
	}
	return chain, trust, nil
}

// requestAlreadyUsed scans device.add payloads for the request id (R28
// single-use enforcement — the marker is durable in the log).
func requestAlreadyUsed(fsys fsx.FS, portableDir, requestID string, verify cairnlog.VerifyFunc) (bool, error) {
	origins, err := cairnlog.Origins(fsys, portableDir)
	if err != nil {
		return false, err
	}
	used := false
	for _, o := range origins {
		_, err := cairnlog.Walk(fsys, portableDir, o, verify, func(env *event.Envelope, _ []byte) error {
			if env.EventType != "device.add" {
				return nil
			}
			var pl struct {
				EnrolmentRequestID string `json:"enrolment_request_id"`
			}
			if json.Unmarshal(env.Payload, &pl) == nil && pl.EnrolmentRequestID == requestID {
				used = true
			}
			return nil
		})
		if err != nil {
			return false, err
		}
	}
	return used, nil
}

// lockDaemonOut acquires the daemon's write lock (ceremonies are OFFLINE).
func lockDaemonOut(deviceDir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(deviceDir, config.DaemonLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("the daemon is running — stop it before the enrolment ceremony (offline operation)")
	}
	return f, nil
}

// Approve validates the request (expiry, single-use), appends the
// root-signed device.add, and writes the join grant.
func Approve(opts ApproveOptions) (*JoinGrant, error) {
	if opts.FS == nil {
		opts.FS = fsx.OS{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	blob, err := os.ReadFile(opts.RequestPath)
	if err != nil {
		return nil, err
	}
	var req EnrollRequest
	if err := json.Unmarshal(blob, &req); err != nil {
		return nil, fmt.Errorf("request file: %w", err)
	}
	exp, err := time.Parse(config.WallTimeFormat, req.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("request expiry: %w", err)
	}
	if !opts.Now().Before(exp) {
		return nil, fmt.Errorf("enrolment request %s expired at %s (R28) — generate a fresh one", req.RequestID, req.ExpiresAt)
	}
	pubRaw, err := base64.StdEncoding.DecodeString(req.DevicePubkey)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return nil, errors.New("request device_pubkey is not a valid Ed25519 key")
	}

	loaded, err := Load(opts.Dir)
	if err != nil {
		return nil, err
	}
	lock, err := lockDaemonOut(loaded.DeviceDir)
	if err != nil {
		return nil, err
	}
	defer lock.Close()

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
	used, err := requestAlreadyUsed(opts.FS, opts.Dir, req.RequestID, trust.Verifier())
	if err != nil {
		return nil, err
	}
	if used {
		return nil, fmt.Errorf("enrolment request %s was already approved (R28: single-use)", req.RequestID)
	}

	devUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	now := opts.Now().UTC().Format(config.WallTimeFormat)
	cert := DeviceCert{
		DeviceID:    devUUID.String(),
		Pubkey:      req.DevicePubkey,
		Generation:  config.FirstGeneration,
		IssuedAt:    now,
		DisplayName: req.DisplayName,
	}
	if err := cert.SignCert(rootPriv); err != nil {
		return nil, err
	}

	// append device.add to THIS device's origin, signed by THIS device
	// (the cert inside is what carries root authority — same as migrate)
	devPriv, err := LoadKey(filepath.Join(loaded.DeviceDir, config.DeviceKeyName))
	if err != nil {
		return nil, err
	}
	lg, _, err := cairnlog.Open(opts.FS, opts.Dir,
		cairnlog.Origin{DeviceID: loaded.Device.DeviceID, Generation: loaded.Device.OriginGeneration},
		trust.Verifier(), nil)
	if err != nil {
		return nil, err
	}
	defer lg.Close()
	payload, err := json.Marshal(map[string]any{
		"cert":                 cert,
		"enrolment_request_id": req.RequestID,
	})
	if err != nil {
		return nil, err
	}
	devPub := devPriv.Public().(ed25519.PublicKey)
	env := &event.Envelope{
		SchemaVersion:         config.EventSchemaVersion,
		CairnID:               loaded.Portable.CairnID,
		EventType:             "device.add",
		OriginDeviceID:        loaded.Device.DeviceID,
		OriginGeneration:      loaded.Device.OriginGeneration,
		OriginSequence:        lg.NextSeq(),
		PreviousOriginEventID: lg.LastEventID(),
		ActorPrincipalID:      "operator",
		ObjectType:            "device",
		ObjectID:              cert.DeviceID,
		WallTime:              now,
		PayloadSchema:         config.PayloadSchemaID,
		Payload:               payload,
		SigningKeyID:          event.KeyID(devPub),
	}
	rec, err := env.Sign(devPriv)
	if err != nil {
		return nil, err
	}
	if err := lg.Append(rec, env); err != nil {
		return nil, err
	}
	chain = append(chain, rec)

	grant := &JoinGrant{
		CairnID:     loaded.Portable.CairnID,
		RequestID:   req.RequestID,
		DeviceID:    cert.DeviceID,
		DisplayName: req.DisplayName,
		CreatedAt:   loaded.Portable.CreatedAt,
		Cert:        cert,
	}
	for _, r := range chain {
		grant.Chain = append(grant.Chain, base64.StdEncoding.EncodeToString(r))
	}
	gblob, err := json.MarshalIndent(grant, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(opts.GrantPath, gblob, 0o600); err != nil {
		return nil, err
	}
	fmt.Fprintf(opts.Out, "approved: device %s (%s) admitted by root-signed device.add %s\n", cert.DeviceID, req.DisplayName, env.EventID)
	fmt.Fprintf(opts.Out, "grant written to %s — carry it to the new node, then REMOVE the restored root key\n", opts.GrantPath)
	return grant, nil
}

// VerifyGrantChain replays the grant's records through a fresh chain
// verifier and returns the resulting Trust. The new node trusts NOTHING it
// did not verify from genesis.
func VerifyGrantChain(grant *JoinGrant) (*Trust, error) {
	v := NewChainVerifier()
	for i, b64 := range grant.Chain {
		rec, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("grant chain[%d]: %w", i, err)
		}
		if _, err := v.Verify(rec); err != nil {
			return nil, fmt.Errorf("grant chain[%d] failed verification: %w", i, err)
		}
	}
	t := v.Trust()
	if t.CairnID != grant.CairnID {
		return nil, fmt.Errorf("grant cairn_id %s does not match the verified chain (%s)", grant.CairnID, t.CairnID)
	}
	if !t.Member(grant.DeviceID) {
		return nil, fmt.Errorf("grant device %s is not admitted by the verified chain", grant.DeviceID)
	}
	if t.Revoked(grant.DeviceID) {
		return nil, fmt.Errorf("grant device %s is revoked", grant.DeviceID)
	}
	return t, nil
}

// bootstrapChainName stores the verified grant chain device-locally until
// N6 replication delivers real segments.
const bootstrapChainName = "bootstrap-chain.json"

// JoinOptions parameterizes installing the approved identity on the new node.
type JoinOptions struct {
	GrantPath        string
	Dir              string // portable dir
	AllowUnencrypted bool   // FIX-G3: operator override, persisted device-local
	Checker          VolumeChecker
	Out              io.Writer
}

// Join installs the approved identity on the new node. FIX-G3: the encryption
// gate fires BEFORE the private key is written to device-local state, and
// --allow-unencrypted is persisted into the device config so the per-startup
// warning fires (parity with init; no device-TOML hand-edit).
func Join(opts JoinOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	grantPath, dir := opts.GrantPath, opts.Dir
	blob, err := os.ReadFile(grantPath)
	if err != nil {
		return err
	}
	var grant JoinGrant
	if err := json.Unmarshal(blob, &grant); err != nil {
		return fmt.Errorf("grant file: %w", err)
	}
	trust, err := VerifyGrantChain(&grant)
	if err != nil {
		return err
	}
	// the embedded cert must itself verify against the CHAIN's root and
	// match the admitted key — never trust grant fields the chain didn't prove
	if err := grant.Cert.Verify(trust.RootPub); err != nil {
		return fmt.Errorf("grant cert: %w", err)
	}
	if grant.Cert.DeviceID != grant.DeviceID {
		return errors.New("grant cert device id mismatch")
	}
	// the staged private key must match the cert the root signed
	staging, err := enrollStagingDir(grant.RequestID)
	if err != nil {
		return err
	}
	priv, err := LoadKey(filepath.Join(staging, config.DeviceKeyName))
	if err != nil {
		return fmt.Errorf("no staged key for request %s on THIS machine (join must run where enroll ran): %w", grant.RequestID, err)
	}
	certPub, ok := trust.DevicePub(grant.DeviceID)
	if !ok || !priv.Public().(ed25519.PublicKey).Equal(certPub) {
		return errors.New("staged private key does not match the granted certificate")
	}

	// portable dir (same cairn id; the LOG arrives via N6 replication)
	if err := os.MkdirAll(dir, config.DirPerm); err != nil {
		return err
	}
	portable := &config.PortableConfig{
		ConfigVersion: config.PortableConfigVersion,
		CairnID:       grant.CairnID,
		CreatedAt:     grant.CreatedAt,
	}
	if err := portable.SavePortable(dir); err != nil {
		return err
	}
	// device-local identity
	deviceDir, err := config.DeviceStateDir(grant.CairnID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(deviceDir, config.DirPerm); err != nil {
		return err
	}
	// FIX-G3: gate the volume holding device-local state BEFORE writing the key.
	if err := GateEncryption(deviceDir, opts.Checker, opts.AllowUnencrypted, out); err != nil {
		return err
	}
	if err := SaveKey(filepath.Join(deviceDir, config.DeviceKeyName), priv); err != nil {
		return err
	}
	device := &config.DeviceConfig{
		ConfigVersion:    config.DeviceConfigVersion,
		CairnID:          grant.CairnID,
		DeviceID:         grant.DeviceID,
		OriginGeneration: config.FirstGeneration,
		CreatedAt:        grant.CreatedAt,
		AllowUnencrypted: opts.AllowUnencrypted, // FIX-G3: persist override (per-startup warning)
		SyncListen:       config.SyncListenAuto, // R44: joined nodes auto-detect the tailnet too
	}
	if err := device.SaveDevice(deviceDir); err != nil {
		return err
	}
	certBlob, err := json.MarshalIndent(grant.Cert, "", "  ")
	if err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(fsx.OS{}, filepath.Join(deviceDir, config.DeviceCertName), certBlob, config.KeyFilePerm); err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(fsx.OS{}, filepath.Join(deviceDir, bootstrapChainName), blob, config.KeyFilePerm); err != nil {
		return err
	}
	os.RemoveAll(staging)
	fmt.Fprintf(out, "joined cairn %s as device %s (%s)\n", grant.CairnID, grant.DeviceID, grant.DisplayName)
	fmt.Fprintf(out, "identity verified from genesis (%d chain records). Peer sync delivers the log in N6;\nuntil then `cairn sync ping <addr>` proves membership.\n", len(grant.Chain))
	return nil
}

// BootstrapTrust loads the verified bootstrap chain a new node uses for peer
// authentication until real segments replicate. A device.join writes a join
// grant (bootstrap-chain.json, self-membership assured); a `cairn pair join`
// writes a pairing bootstrap (PairingBootstrapName, NO self-membership because
// the device.add is appended on arrival). This falls through to the pairing
// bootstrap when there is no join grant.
func BootstrapTrust(deviceDir string) (*Trust, error) {
	blob, err := os.ReadFile(filepath.Join(deviceDir, bootstrapChainName))
	if err != nil {
		if os.IsNotExist(err) {
			return PairingBootstrapTrust(deviceDir)
		}
		return nil, err
	}
	var grant JoinGrant
	if err := json.Unmarshal(blob, &grant); err != nil {
		return nil, err
	}
	return VerifyGrantChain(&grant)
}

// RevokeDevice appends a ROOT-signed device.revoke (offline ceremony, like
// migrate's revocation) for an enrolled device.
func RevokeDevice(dir, deviceID, rootKeyPath, reason string, fsys fsx.FS, now func() time.Time, out io.Writer) (string, error) {
	if fsys == nil {
		fsys = fsx.OS{}
	}
	if now == nil {
		now = time.Now
	}
	if out == nil {
		out = io.Discard
	}
	loaded, err := Load(dir)
	if err != nil {
		return "", err
	}
	if deviceID == loaded.Device.DeviceID {
		return "", errors.New("refusing to revoke THIS device (use `cairn migrate` for self-replacement)")
	}
	lock, err := lockDaemonOut(loaded.DeviceDir)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	rootPriv, err := LoadKey(rootKeyPath)
	if err != nil {
		return "", fmt.Errorf("root key (restore it from offline storage first): %w", err)
	}
	trust, err := MeshTrust(fsys, dir)
	if err != nil {
		return "", err
	}
	if !rootPriv.Public().(ed25519.PublicKey).Equal(trust.RootPub) {
		return "", errors.New("restored key is NOT this mesh's root key")
	}
	if !trust.Member(deviceID) {
		return "", fmt.Errorf("device %s is not a member of this mesh", deviceID)
	}
	if trust.Revoked(deviceID) {
		return "", fmt.Errorf("device %s is already revoked", deviceID)
	}
	lg, _, err := cairnlog.Open(fsys, dir,
		cairnlog.Origin{DeviceID: loaded.Device.DeviceID, Generation: loaded.Device.OriginGeneration},
		trust.Verifier(), nil)
	if err != nil {
		return "", err
	}
	defer lg.Close()
	payload := map[string]any{"device_id": deviceID}
	if reason != "" {
		payload["reason"] = reason
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	rootPub := rootPriv.Public().(ed25519.PublicKey)
	env := &event.Envelope{
		SchemaVersion:         config.EventSchemaVersion,
		CairnID:               loaded.Portable.CairnID,
		EventType:             "device.revoke",
		OriginDeviceID:        loaded.Device.DeviceID,
		OriginGeneration:      loaded.Device.OriginGeneration,
		OriginSequence:        lg.NextSeq(),
		PreviousOriginEventID: lg.LastEventID(),
		ActorPrincipalID:      "operator",
		ObjectType:            "device",
		ObjectID:              deviceID,
		WallTime:              now().UTC().Format(config.WallTimeFormat),
		PayloadSchema:         config.PayloadSchemaID,
		Payload:               raw,
		SigningKeyID:          event.KeyID(rootPub),
	}
	rec, err := env.Sign(rootPriv)
	if err != nil {
		return "", err
	}
	if err := lg.Append(rec, env); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "revoked device %s (root-signed %s) — restart the daemon so the listener refuses it; REMOVE the restored root key\n", deviceID, env.EventID)
	return env.EventID, nil
}
