package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
)

// GenesisPayload is the cairn.genesis payload (rulings §2): it establishes
// the root public key and embeds the root-signed initial device certificate.
// The envelope is signed by that device — the event is thereby dual-signed.
type GenesisPayload struct {
	CairnID           string     `json:"cairn_id"`
	RootPubkey        string     `json:"root_pubkey"` // base64 Ed25519
	InitialDeviceCert DeviceCert `json:"initial_device_cert"`
}

// BuildGenesis constructs and device-signs the cairn.genesis event as the
// first event of (device, generation 1): sequence 1, no previous event.
// Returns the envelope and its canonical record_bytes.
func BuildGenesis(cairnID string, rootPub ed25519.PublicKey, cert DeviceCert, devicePriv ed25519.PrivateKey, wallTime string) (*event.Envelope, []byte, error) {
	payload, err := json.Marshal(GenesisPayload{
		CairnID:           cairnID,
		RootPubkey:        base64.StdEncoding.EncodeToString(rootPub),
		InitialDeviceCert: cert,
	})
	if err != nil {
		return nil, nil, err
	}
	devicePub := devicePriv.Public().(ed25519.PublicKey)
	env := &event.Envelope{
		SchemaVersion:    config.EventSchemaVersion,
		CairnID:          cairnID,
		EventType:        "cairn.genesis",
		OriginDeviceID:   cert.DeviceID,
		OriginGeneration: config.FirstGeneration,
		OriginSequence:   config.FirstSequence,
		ActorPrincipalID: "operator",
		ObjectType:       "cairn",
		ObjectID:         cairnID,
		WallTime:         wallTime,
		PayloadSchema:    config.PayloadSchemaID,
		Payload:          payload,
		SigningKeyID:     event.KeyID(devicePub),
	}
	record, err := env.Sign(devicePriv)
	if err != nil {
		return nil, nil, fmt.Errorf("signing genesis: %w", err)
	}
	return env, record, nil
}

// VerifyGenesis fully checks a genesis record: envelope signature by the
// certified device key, event_id integrity, and the root signature on the
// embedded initial device certificate.
func VerifyGenesis(recordBytes []byte) (*event.Envelope, *GenesisPayload, error) {
	// First parse (unverified) to learn the certified device key, then verify
	// the envelope against exactly that key.
	var peek struct {
		Payload GenesisPayload `json:"payload"`
	}
	if err := json.Unmarshal(recordBytes, &peek); err != nil {
		return nil, nil, fmt.Errorf("parsing genesis payload: %w", err)
	}
	pl := peek.Payload

	rootPub, err := base64.StdEncoding.DecodeString(pl.RootPubkey)
	if err != nil || len(rootPub) != ed25519.PublicKeySize {
		return nil, nil, fmt.Errorf("genesis root_pubkey invalid")
	}
	if err := pl.InitialDeviceCert.Verify(ed25519.PublicKey(rootPub)); err != nil {
		return nil, nil, err
	}
	devicePub, err := pl.InitialDeviceCert.DevicePublicKey()
	if err != nil {
		return nil, nil, err
	}

	env, err := event.VerifyRecord(recordBytes, func(keyID string) (ed25519.PublicKey, error) {
		if keyID != event.KeyID(devicePub) {
			return nil, fmt.Errorf("genesis signed by %s, not the certified initial device key", keyID)
		}
		return devicePub, nil
	})
	if err != nil {
		return nil, nil, err
	}
	if env.EventType != "cairn.genesis" {
		return nil, nil, fmt.Errorf("expected cairn.genesis, got %s", env.EventType)
	}
	if env.CairnID != pl.CairnID {
		return nil, nil, fmt.Errorf("envelope cairn_id %s != payload cairn_id %s", env.CairnID, pl.CairnID)
	}
	if env.OriginDeviceID != pl.InitialDeviceCert.DeviceID {
		return nil, nil, fmt.Errorf("genesis origin device %s != certified device %s", env.OriginDeviceID, pl.InitialDeviceCert.DeviceID)
	}
	return env, &pl, nil
}
