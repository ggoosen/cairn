// Package identity implements Cairn's identity layer: Ed25519 root and
// device keys, root-signed device certificates, the genesis event, the
// device-local state directory, and the encrypted-volume startup check
// (spec §3, rulings §4, §9).
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ggoosen/cairn/internal/config"
)

// keyFile is the on-disk format for private keys in device-local state.
// Private keys NEVER live under the portable cairn directory.
type keyFile struct {
	Type          string `json:"type"` // "ed25519"
	PrivateKeyB64 string `json:"private_key_b64"`
}

// GenerateKey returns a fresh Ed25519 keypair.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// SaveKey writes a private key to path with 0600 permissions and fsyncs it.
func SaveKey(path string, priv ed25519.PrivateKey) error {
	blob, err := json.Marshal(keyFile{
		Type:          "ed25519",
		PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, config.KeyFilePerm)
	if err != nil {
		return fmt.Errorf("writing key %s: %w", path, err)
	}
	if _, err := f.Write(blob); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// LoadKey reads a private key written by SaveKey.
func LoadKey(path string) (ed25519.PrivateKey, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var kf keyFile
	if err := json.Unmarshal(blob, &kf); err != nil {
		return nil, fmt.Errorf("parsing key file %s: %w", path, err)
	}
	if kf.Type != "ed25519" {
		return nil, fmt.Errorf("unsupported key type %q in %s", kf.Type, path)
	}
	raw, err := base64.StdEncoding.DecodeString(kf.PrivateKeyB64)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("key file %s: wrong private key length %d", path, len(raw))
	}
	return ed25519.PrivateKey(raw), nil
}
