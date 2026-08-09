// Package event implements the Cairn event envelope, RFC 8785 canonical
// serialization, the signing_bytes/record_bytes split, event IDs, and
// Ed25519 signing/verification (rulings §1, §2).
package event

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
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

// maxSafeJSONInt is the largest integer RFC 8785 can serialize exactly:
// JCS renders numbers as ES6 IEEE-754 doubles, so an integer beyond 2^53-1
// silently loses precision AND comes out in exponent form ("1e+21") — which
// this very validator would then reject on re-verification. Found by
// FuzzCanonicalRoundTrip; the i-JSON exact range is the protocol bound.
const maxSafeJSONInt = 1<<53 - 1

// ValidateNoFloats walks raw JSON and rejects any number that is not a plain
// integer literal within the IEEE-754 exact range (fraction, exponent, or
// magnitude beyond 2^53-1). This closes the float-normalization
// signature-fork risk that motivated CBOR (rulings §1).
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
			i, perr := strconv.ParseInt(s, 10, 64)
			if perr != nil || i > maxSafeJSONInt || i < -maxSafeJSONInt {
				return fmt.Errorf("integer %q exceeds the RFC 8785 exact range (±2^53-1): canonical serialization would corrupt it", s)
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
