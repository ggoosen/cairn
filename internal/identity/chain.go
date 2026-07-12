package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/ggoosen/cairn/internal/event"
)

// ChainVerifier is the stateful record verifier the log recovery/doctor scan
// uses: it learns the root key and device keys from the security events it
// verifies, in order. The first record it accepts for a fresh cairn must be
// the self-certifying cairn.genesis; device.add certs must be root-signed;
// device.revoke marks a key (revocation enforcement of the old origin's
// read-only state lands with `cairn migrate` in M4).
type ChainVerifier struct {
	cairnID    string
	rootPub    ed25519.PublicKey
	keys       map[string]ed25519.PublicKey // signing_key_id → pubkey
	devicePubs map[string]ed25519.PublicKey // device_id → pubkey
	revoked    map[string]bool              // device_id → revoked

	genesisEnv     *event.Envelope
	genesisPayload *GenesisPayload
}

func NewChainVerifier() *ChainVerifier {
	return &ChainVerifier{
		keys:       map[string]ed25519.PublicKey{},
		devicePubs: map[string]ed25519.PublicKey{},
		revoked:    map[string]bool{},
	}
}

// Trust snapshots the verifier's learned mesh state (FIX-F2 pass 1 result).
func (v *ChainVerifier) Trust() *Trust {
	t := &Trust{
		CairnID:        v.cairnID,
		RootPub:        v.rootPub,
		GenesisEnv:     v.genesisEnv,
		GenesisPayload: v.genesisPayload,
		keys:           map[string]ed25519.PublicKey{},
		devices:        map[string]ed25519.PublicKey{},
		revoked:        map[string]bool{},
	}
	for k, pub := range v.keys {
		t.keys[k] = pub
	}
	for id, pub := range v.devicePubs {
		t.devices[id] = pub
	}
	for id := range v.revoked {
		t.revoked[id] = true
	}
	return t
}

// Verify implements log.VerifyFunc.
func (v *ChainVerifier) Verify(recordBytes []byte) (*event.Envelope, error) {
	var peek struct {
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(recordBytes, &peek); err != nil {
		return nil, fmt.Errorf("unparseable record: %w", err)
	}

	if peek.EventType == "cairn.genesis" {
		env, pl, err := VerifyGenesis(recordBytes)
		if err != nil {
			return nil, err
		}
		if v.rootPub != nil {
			// a SEEDED verifier (pass 2) legitimately re-sees THE genesis;
			// a different genesis is a foreign mesh and always an error
			if pl.CairnID != v.cairnID {
				return nil, fmt.Errorf("second genesis event encountered (foreign cairn %s)", pl.CairnID)
			}
			return env, nil
		}
		rootPub, err := base64.StdEncoding.DecodeString(pl.RootPubkey)
		if err != nil {
			return nil, err
		}
		devicePub, err := pl.InitialDeviceCert.DevicePublicKey()
		if err != nil {
			return nil, err
		}
		v.cairnID = pl.CairnID
		v.rootPub = ed25519.PublicKey(rootPub)
		v.keys[event.KeyID(devicePub)] = devicePub
		v.devicePubs[pl.InitialDeviceCert.DeviceID] = devicePub
		// the root key signs device.revoke envelopes directly (rulings §2/§4)
		v.keys[event.KeyID(v.rootPub)] = v.rootPub
		v.genesisEnv, v.genesisPayload = env, pl
		return env, nil
	}

	if v.rootPub == nil {
		return nil, fmt.Errorf("%s record before genesis — key chain not established", peek.EventType)
	}

	env, err := event.VerifyRecord(recordBytes, func(keyID string) (ed25519.PublicKey, error) {
		pub, ok := v.keys[keyID]
		if !ok {
			return nil, fmt.Errorf("unknown signing key (no admitting device.add seen)")
		}
		return pub, nil
	})
	if err != nil {
		return nil, err
	}
	if env.CairnID != v.cairnID {
		return nil, fmt.Errorf("event %s belongs to cairn %s, chain is %s", env.EventID, env.CairnID, v.cairnID)
	}

	switch env.EventType {
	case "device.add":
		var pl struct {
			Cert DeviceCert `json:"cert"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return nil, fmt.Errorf("device.add payload: %w", err)
		}
		if err := pl.Cert.Verify(v.rootPub); err != nil {
			return nil, fmt.Errorf("device.add cert: %w", err)
		}
		pub, err := pl.Cert.DevicePublicKey()
		if err != nil {
			return nil, err
		}
		v.keys[event.KeyID(pub)] = pub
		v.devicePubs[pl.Cert.DeviceID] = pub
	case "device.revoke":
		var pl struct {
			DeviceID string `json:"device_id"`
		}
		if err := json.Unmarshal(env.Payload, &pl); err != nil {
			return nil, fmt.Errorf("device.revoke payload: %w", err)
		}
		v.revoked[pl.DeviceID] = true
	}
	return env, nil
}

// Revoked reports whether a device has been revoked in the verified chain.
func (v *ChainVerifier) Revoked(deviceID string) bool { return v.revoked[deviceID] }
