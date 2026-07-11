package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ggoosen/cairn/internal/event"
)

// DeviceCert is the root-signed device certificate embedded in cairn.genesis
// and device.add payloads (build/schemas/p0-events.schema.json §deviceCert).
type DeviceCert struct {
	DeviceID    string `json:"device_id"`
	Pubkey      string `json:"pubkey"` // base64 Ed25519 public key
	Generation  int    `json:"generation"`
	IssuedAt    string `json:"issued_at"` // RFC 3339 UTC, fixed µs
	DisplayName string `json:"display_name,omitempty"`
	// RootSignature: base64 Ed25519 over the RFC 8785 canonical cert with
	// this field stripped (schema: "canonical cert minus this field").
	RootSignature string `json:"root_signature,omitempty"`
}

// SignCert fills RootSignature using the root private key.
func (c *DeviceCert) SignCert(rootPriv ed25519.PrivateKey) error {
	if c.RootSignature != "" {
		return errors.New("certificate already signed")
	}
	msg, err := c.signingBytes()
	if err != nil {
		return err
	}
	c.RootSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(rootPriv, msg))
	return nil
}

// Verify checks RootSignature against the root public key.
func (c *DeviceCert) Verify(rootPub ed25519.PublicKey) error {
	if c.RootSignature == "" {
		return errors.New("certificate is unsigned")
	}
	msg, err := c.signingBytes()
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(c.RootSignature)
	if err != nil {
		return fmt.Errorf("decoding root_signature: %w", err)
	}
	if !ed25519.Verify(rootPub, msg, sig) {
		return fmt.Errorf("invalid root signature on device certificate %s", c.DeviceID)
	}
	return nil
}

// DevicePublicKey decodes the certificate's embedded public key.
func (c *DeviceCert) DevicePublicKey() (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(c.Pubkey)
	if err != nil {
		return nil, fmt.Errorf("decoding cert pubkey: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("cert pubkey has wrong length %d", len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

func (c *DeviceCert) signingBytes() ([]byte, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return event.CanonicalizeStripped(raw, "root_signature")
}
