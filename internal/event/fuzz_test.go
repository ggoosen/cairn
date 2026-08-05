package event_test

// CI-B3: RFC 8785 canonicalization is the signature substrate — it must
// never panic on arbitrary bytes, and canonical form must be a FIXPOINT
// (canonicalize(canonicalize(x)) == canonicalize(x)); a non-idempotent
// canonicalization would fork signatures between signer and verifier.

import (
	"bytes"
	"testing"

	"github.com/ggoosen/cairn/internal/event"
)

func FuzzCanonicalRoundTrip(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"b":1,"a":"x"}`))
	f.Add([]byte(`{"n":{"z":[1,2,{"y":"s"}],"a":"é"}}`))
	f.Add([]byte(`{"f":1.5}`))       // float: must be rejected, not canonicalized
	f.Add([]byte(`{"e":1e3}`))      // exponent: same
	f.Add([]byte(`not json`))       // must error, not panic
	f.Add([]byte(`{"u":"😀 emoji"}`))
	f.Add([]byte(`{"dup":"a","dup":"b"}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		canon, err := event.Canonicalize(raw)
		if err != nil {
			return // rejection is fine; panics are not
		}
		again, err := event.Canonicalize(canon)
		if err != nil {
			t.Fatalf("canonical form rejected on second pass: %v\nfirst: %s", err, canon)
		}
		if !bytes.Equal(canon, again) {
			t.Fatalf("canonicalization is not a fixpoint:\n1st: %s\n2nd: %s", canon, again)
		}
		// The stripped form need not be byte-identical to its input (Go's
		// decode→re-marshal can renormalize exotic Unicode; signer and
		// verifier both run the SAME function, so no fork) — but its output
		// must itself be canonical: a second pass may not change it.
		stripped, err := event.CanonicalizeStripped(canon)
		if err == nil { // CanonicalizeStripped requires an OBJECT; others error
			s2, err2 := event.Canonicalize(stripped)
			if err2 != nil || !bytes.Equal(stripped, s2) {
				t.Fatalf("CanonicalizeStripped output is not canonical:\nout: %s\n2nd: %s (%v)", stripped, s2, err2)
			}
		}
	})
}
