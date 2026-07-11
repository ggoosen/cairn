// Package event implements the Cairn event envelope, RFC 8785 canonical
// serialization, the signing_bytes/record_bytes split, event IDs, and
// Ed25519 signing/verification (rulings §1, §2).
package event

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gowebpki/jcs"
)

// Canonicalize returns the RFC 8785 canonical form of raw JSON, after
// enforcing the no-floats rule: event payloads carry integers and strings
// only (rulings §1). Timestamps are RFC 3339 UTC strings.
func Canonicalize(raw []byte) ([]byte, error) {
	if err := ValidateNoFloats(raw); err != nil {
		return nil, err
	}
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalizing: %w", err)
	}
	return out, nil
}

// CanonicalizeStripped canonicalizes a JSON object with the named top-level
// fields removed. Used for signing_bytes (strip event_id, signature) and for
// device-cert signing (strip root_signature).
func CanonicalizeStripped(raw []byte, strip ...string) ([]byte, error) {
	obj, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	for _, f := range strip {
		delete(obj, f)
	}
	remarshalled, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return Canonicalize(remarshalled)
}

// ValidateNoFloats walks raw JSON and rejects any number that is not a plain
// integer literal (fraction or exponent present). This closes the
// float-normalization signature-fork risk that motivated CBOR (rulings §1).
func ValidateNoFloats(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return nil
			}
			return fmt.Errorf("invalid JSON: %w", err)
		}
		if n, ok := tok.(json.Number); ok {
			s := n.String()
			if strings.ContainsAny(s, ".eE") {
				return fmt.Errorf("float forbidden in event payloads: %q (integers and strings only)", s)
			}
		}
	}
}

// decodeObject parses raw into a generic map with json.Number preservation
// (numbers are never routed through float64, so re-marshalling is lossless).
func decodeObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("decoding JSON object: %w", err)
	}
	return obj, nil
}
