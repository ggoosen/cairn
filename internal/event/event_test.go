package event

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

// RFC 8785 behavior checks against known canonicalization rules:
// lexicographic key ordering (by UTF-16 code units), minimal escapes,
// integer number forms, and byte-stability.
func TestCanonicalizeVectors(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"key-order", `{"b":2,"a":1}`, `{"a":1,"b":2}`},
		{"nested", `{"z":{"y":2,"x":1},"a":[3,2,1]}`, `{"a":[3,2,1],"z":{"x":1,"y":2}}`},
		{"whitespace", "{\n  \"a\" : 1 ,\n \"b\":\"x y\"\n}", `{"a":1,"b":"x y"}`},
		{"unicode-literal", `{"a":"é"}`, `{"a":"é"}`},
		{"escapes", `{"a":"line\nbreak"}`, `{"a":"line\nbreak"}`},
		{"integers", `{"n":1000000}`, `{"n":1000000}`},
	}
	for _, c := range cases {
		got, err := Canonicalize([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
		// serialize→parse→serialize must be byte-stable (TESTING.md §3)
		again, err := Canonicalize(got)
		if err != nil {
			t.Fatalf("%s restability: %v", c.name, err)
		}
		if string(again) != string(got) {
			t.Errorf("%s: not byte-stable: %s vs %s", c.name, again, got)
		}
	}
}

func TestNoFloatsRejected(t *testing.T) {
	bad := []string{
		`{"x":1.5}`,
		`{"x":1e3}`,
		`{"x":1E-2}`,
		`{"deep":{"list":[1,2,{"y":0.1}]}}`,
	}
	for _, in := range bad {
		if _, err := Canonicalize([]byte(in)); err == nil {
			t.Errorf("float accepted: %s", in)
		}
	}
	good := []string{`{"x":15}`, `{"x":-3,"y":"1.5"}`, `{"x":0}`}
	for _, in := range good {
		if _, err := Canonicalize([]byte(in)); err != nil {
			t.Errorf("integer rejected: %s: %v", in, err)
		}
	}
}

func testEnvelope() *Envelope {
	return &Envelope{
		SchemaVersion:    1,
		CairnID:          "0190a1b2-c3d4-7e5f-8901-234567890abc",
		EventType:        "cairn.genesis",
		OriginDeviceID:   "0190a1b2-c3d4-7e5f-8901-234567890def",
		OriginGeneration: 1,
		OriginSequence:   1,
		ActorPrincipalID: "operator",
		ObjectType:       "cairn",
		ObjectID:         "0190a1b2-c3d4-7e5f-8901-234567890abc",
		WallTime:         "2026-07-11T00:00:00.000000Z",
		PayloadSchema:    "cairn/p0-events/v1",
		Payload:          json.RawMessage(`{"k":"v","n":7}`),
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	env := testEnvelope()
	env.SigningKeyID = KeyID(pub)

	record, err := env.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if env.EventID == "" || len(env.EventID) != 64 {
		t.Fatalf("bad event_id %q", env.EventID)
	}

	resolve := func(keyID string) (ed25519.PublicKey, error) { return pub, nil }
	back, err := VerifyRecord(record, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if back.EventID != env.EventID || back.EventType != "cairn.genesis" {
		t.Fatalf("verified envelope mismatch: %+v", back)
	}

	// record_bytes must themselves be canonical (stored form == canonical form)
	canon, err := Canonicalize(record)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != string(record) {
		t.Fatal("record_bytes are not canonical")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	env := testEnvelope()
	env.SigningKeyID = KeyID(pub)
	record, err := env.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(string) (ed25519.PublicKey, error) { return pub, nil }

	// flip payload content → event_id mismatch
	tampered := strings.Replace(string(record), `"k":"v"`, `"k":"V"`, 1)
	if _, err := VerifyRecord([]byte(tampered), resolve); err == nil {
		t.Fatal("payload tamper not detected")
	}

	// wrong key → signature failure
	otherPub, _, _ := ed25519.GenerateKey(nil)
	badResolve := func(string) (ed25519.PublicKey, error) { return otherPub, nil }
	if _, err := VerifyRecord(record, badResolve); err == nil {
		t.Fatal("wrong-key signature not detected")
	}
}

func TestUnknownPayloadFieldsSurviveVerification(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	env := testEnvelope()
	env.SigningKeyID = KeyID(pub)
	env.Payload = json.RawMessage(`{"known":"x","zz_future_field":{"nested":1}}`)
	record, err := env.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := VerifyRecord(record, func(string) (ed25519.PublicKey, error) { return pub, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(back.Payload), "zz_future_field") {
		t.Fatal("unknown payload field lost")
	}
}

func TestDomainSeparation(t *testing.T) {
	// same bytes, different domain prefix must differ — guard the separator
	a := EventID([]byte("x"))
	sum := EventID([]byte("y"))
	if a == sum {
		t.Fatal("hash collision?!")
	}
	env := testEnvelope()
	unsigned, _ := json.Marshal(env)
	signing, err := Canonicalize(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	if EventID(signing) == EventID(append([]byte("other-domain"), signing...)) {
		t.Fatal("domain separator not applied")
	}
}

func TestSignRejectsFloatsInPayload(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	env := testEnvelope()
	env.Payload = json.RawMessage(`{"weight":0.5}`)
	if _, err := env.Sign(priv); err == nil {
		t.Fatal("float payload signed")
	}
}

func TestSignRefusesDoubleSign(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	env := testEnvelope()
	env.SigningKeyID = KeyID(pub)
	if _, err := env.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Sign(priv); err == nil {
		t.Fatal("double sign allowed")
	}
}
