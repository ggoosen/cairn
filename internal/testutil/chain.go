// Package testutil provides test-only helpers: a signed event-chain builder
// that produces events exactly the way the daemon will (genesis, then
// chained, signed envelopes). Used by projection/outbox/daemon test suites.
package testutil

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/identity"
)

// Chain builds signed, chained test events for one origin.
type Chain struct {
	CairnID  string
	DeviceID string
	DevPriv  ed25519.PrivateKey
	RootPriv ed25519.PrivateKey
	KeyID    string
	NextSeq  int64
	LastID   string
	clock    int64
}

// NewChain generates keys, a root-signed cert, and the genesis event.
func NewChain(t *testing.T) (*Chain, *event.Envelope, []byte) {
	t.Helper()
	rootPub, rootPriv, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	devPub, devPriv, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c := &Chain{
		CairnID:  "0190a1b2-c3d4-7e5f-8901-234567890abc",
		DeviceID: "0190a1b2-c3d4-7e5f-8901-234567890def",
		DevPriv:  devPriv,
		RootPriv: rootPriv,
		KeyID:    event.KeyID(devPub),
	}
	cert := identity.DeviceCert{
		DeviceID:   c.DeviceID,
		Pubkey:     base64.StdEncoding.EncodeToString(devPub),
		Generation: 1,
		IssuedAt:   "2026-07-11T00:00:00.000000Z",
	}
	if err := cert.SignCert(rootPriv); err != nil {
		t.Fatal(err)
	}
	env, record, err := identity.BuildGenesis(c.CairnID, rootPub, cert, devPriv, "2026-07-11T00:00:00.000000Z")
	if err != nil {
		t.Fatal(err)
	}
	c.NextSeq = config.FirstSequence + 1
	c.LastID = env.EventID
	return c, env, record
}

// Event builds and signs the next chained event of the given type. The
// payload must marshal without floats. Wall times advance deterministically.
func (c *Chain) Event(t *testing.T, eventType, objectType, objectID string, payload any, actor string) (*event.Envelope, []byte) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	c.clock++
	env := &event.Envelope{
		SchemaVersion:         config.EventSchemaVersion,
		CairnID:               c.CairnID,
		EventType:             eventType,
		OriginDeviceID:        c.DeviceID,
		OriginGeneration:      1,
		OriginSequence:        c.NextSeq,
		PreviousOriginEventID: c.LastID,
		ActorPrincipalID:      actor,
		ObjectType:            objectType,
		ObjectID:              objectID,
		WallTime:              wallTime(c.clock),
		PayloadSchema:         config.PayloadSchemaID,
		Payload:               raw,
		SigningKeyID:          c.KeyID,
	}
	record, err := env.Sign(c.DevPriv)
	if err != nil {
		t.Fatal(err)
	}
	c.NextSeq++
	c.LastID = env.EventID
	return env, record
}

// AdmitNewDevice appends a device.add for a FRESH device to the given log
// (signed by this chain's device; cert root-signed) and returns a chain for
// the new device's own origin — the migrate shape at the log layer.
func (c *Chain) AdmitNewDevice(t *testing.T, lg interface {
	Append([]byte, *event.Envelope) error
}) *Chain {
	t.Helper()
	newPub, newPriv, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	newID := "0190a1b2-c3d4-7e5f-8901-23456789beef"
	cert := identity.DeviceCert{
		DeviceID:   newID,
		Pubkey:     base64.StdEncoding.EncodeToString(newPub),
		Generation: 1,
		IssuedAt:   wallTime(c.clock + 1),
	}
	if err := cert.SignCert(c.RootPriv); err != nil {
		t.Fatal(err)
	}
	env, rec := c.Event(t, "device.add", "device", newID, map[string]any{"cert": cert}, "operator")
	if err := lg.Append(rec, env); err != nil {
		t.Fatal(err)
	}
	return &Chain{
		CairnID:  c.CairnID,
		DeviceID: newID,
		DevPriv:  newPriv,
		RootPriv: c.RootPriv,
		KeyID:    event.KeyID(newPub),
		NextSeq:  1,
		LastID:   "",
		clock:    c.clock + 100,
	}
}

// wallTime: deterministic, unique RFC3339 timestamps for replay-identity
// assertions (ordering never depends on wall time — rulings §1).
func wallTime(tick int64) string {
	return time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC).
		Add(time.Duration(tick) * time.Second).Format(config.WallTimeFormat)
}
