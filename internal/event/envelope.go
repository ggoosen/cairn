package event

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"lukechampine.com/blake3"

	"github.com/ggoosen/cairn/internal/config"
)

// Envelope is the event envelope per spec §4.1 (fields renamed per the Cairn
// rename: mesh_id → cairn_id). Payload is raw JSON so unknown payload fields
// survive verbatim in canonical storage (rulings §2).
type Envelope struct {
	SchemaVersion         int             `json:"schema_version"`
	CairnID               string          `json:"cairn_id"`
	EventID               string          `json:"event_id,omitempty"` // BLAKE3 hex; excluded from signing_bytes
	EventType             string          `json:"event_type"`
	OriginDeviceID        string          `json:"origin_device_id"`
	OriginGeneration      int             `json:"origin_generation"`
	OriginSequence        int64           `json:"origin_sequence"`
	PreviousOriginEventID string          `json:"previous_origin_event_id,omitempty"` // absent on the first event of a chain
	ActorPrincipalID      string          `json:"actor_principal_id"`
	ActorTaskID           string          `json:"actor_task_id,omitempty"`
	ActorAgentInstanceID  string          `json:"actor_agent_instance_id,omitempty"`
	ObjectType            string          `json:"object_type"`
	ObjectID              string          `json:"object_id"`
	CausationEventID      string          `json:"causation_event_id,omitempty"`
	CorrelationID         string          `json:"correlation_id,omitempty"`
	ParentEventIDs        []string        `json:"parent_event_ids,omitempty"`
	WallTime              string          `json:"wall_time"` // RFC 3339 UTC, fixed µs; NEVER used for ordering
	ObservedTime          string          `json:"observed_time,omitempty"`
	PayloadSchema         string          `json:"payload_schema"`
	Payload               json.RawMessage `json:"payload"`
	SigningKeyID          string          `json:"signing_key_id"`
	Signature             string          `json:"signature,omitempty"` // base64 Ed25519; excluded from signing_bytes
}

// EventID computes BLAKE3(EventDomainSeparator || signing_bytes), lowercase hex.
func EventID(signingBytes []byte) string {
	h := blake3.New(32, nil)
	_, _ = h.Write([]byte(config.EventDomainSeparator)) // blake3 Write never errors
	_, _ = h.Write(signingBytes)
	return hex.EncodeToString(h.Sum(nil))
}

// signatureInput is the message actually signed: the UTF-8 bytes of
// cairn_id followed by the event_id hex string (rulings §1:
// signature = Sign(device_key, cairn_id || event_id)).
func signatureInput(cairnID, eventID string) []byte {
	return []byte(cairnID + eventID)
}

// Sign completes an envelope: computes signing_bytes (canonical form minus
// event_id and signature), derives event_id, signs, and returns the final
// canonical record_bytes (which INCLUDE event_id and signature). The
// envelope's EventID and Signature fields are filled in.
func (e *Envelope) Sign(priv ed25519.PrivateKey) (recordBytes []byte, err error) {
	if e.EventID != "" || e.Signature != "" {
		return nil, errors.New("envelope already signed")
	}
	if len(e.Payload) == 0 {
		return nil, errors.New("envelope has no payload")
	}
	unsigned, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	signingBytes, err := Canonicalize(unsigned)
	if err != nil {
		return nil, fmt.Errorf("building signing_bytes: %w", err)
	}
	e.EventID = EventID(signingBytes)
	e.Signature = base64.StdEncoding.EncodeToString(
		ed25519.Sign(priv, signatureInput(e.CairnID, e.EventID)))

	full, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	recordBytes, err = Canonicalize(full)
	if err != nil {
		return nil, fmt.Errorf("building record_bytes: %w", err)
	}
	return recordBytes, nil
}

// VerifyRecord checks a stored record: recomputes signing_bytes from the
// record (generically, preserving unknown fields), verifies the event_id
// hash and the Ed25519 signature. resolveKey maps signing_key_id → public
// key. Returns the parsed envelope on success.
func VerifyRecord(recordBytes []byte, resolveKey func(keyID string) (ed25519.PublicKey, error)) (*Envelope, error) {
	obj, err := decodeObject(recordBytes)
	if err != nil {
		return nil, err
	}
	eventID, _ := obj["event_id"].(string)
	sigB64, _ := obj["signature"].(string)
	cairnID, _ := obj["cairn_id"].(string)
	keyID, _ := obj["signing_key_id"].(string)
	if eventID == "" || sigB64 == "" || cairnID == "" || keyID == "" {
		return nil, errors.New("record missing event_id, signature, cairn_id, or signing_key_id")
	}

	signingBytes, err := CanonicalizeStripped(recordBytes, "event_id", "signature")
	if err != nil {
		return nil, err
	}
	if got := EventID(signingBytes); got != eventID {
		return nil, fmt.Errorf("event_id mismatch: record says %s, content hashes to %s", eventID, got)
	}

	pub, err := resolveKey(keyID)
	if err != nil {
		return nil, fmt.Errorf("resolving signing key %s: %w", keyID, err)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("decoding signature: %w", err)
	}
	if !ed25519.Verify(pub, signatureInput(cairnID, eventID), sig) {
		return nil, fmt.Errorf("invalid signature on event %s", eventID)
	}

	var env Envelope
	if err := json.Unmarshal(recordBytes, &env); err != nil {
		return nil, fmt.Errorf("parsing verified record: %w", err)
	}
	return &env, nil
}

// KeyID returns the BLAKE3 hex hash of the canonical public-key bytes — the
// raw 32-byte Ed25519 public key (rulings §1: key IDs are BLAKE3 of
// canonical public-key bytes).
func KeyID(pub ed25519.PublicKey) string {
	sum := blake3.Sum256(pub)
	return hex.EncodeToString(sum[:])
}
