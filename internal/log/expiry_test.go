package log_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
)

// M2 acceptance, end to end: an ephemeral body expires and is housekept —
// the publish event replays fine, doctor stays clean, and fetch returns the
// TYPED content_expired result (never a crash, never a hard error).
func TestExpiredEphemeralEventReplaysFine(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	c, lg := newLogWithGenesis(t, m, "/p", 1)
	store := object.NewStore(m, "/p")

	// publish an ephemeral message: object first, then the signed event
	body := []byte("ephemeral scratchpad contents")
	hash, err := store.Put(body)
	if err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	env := &event.Envelope{
		SchemaVersion:         config.EventSchemaVersion,
		CairnID:               c.cairnID,
		EventType:             "message.publish",
		OriginDeviceID:        c.deviceID,
		OriginGeneration:      1,
		OriginSequence:        c.nextSeq,
		PreviousOriginEventID: c.lastID,
		ActorPrincipalID:      "operator",
		ObjectType:            "message",
		ObjectID:              "0190a1b2-c3d4-7e5f-8901-234567890ccc",
		WallTime:              publishedAt.Format(config.WallTimeFormat),
		PayloadSchema:         config.PayloadSchemaID,
		Payload: json.RawMessage(fmt.Sprintf(
			`{"message_id":"0190a1b2-c3d4-7e5f-8901-234567890ccc","revision_id":"0190a1b2-c3d4-7e5f-8901-234567890ccd","body_hash":"%s","body_len":%d,"body_mime":"text/markdown","text_class":"ephemeral","declared_priority":0}`,
			hash, len(body))),
		SigningKeyID: c.keyID,
	}
	rec, err := env.Sign(c.devPriv)
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Append(rec, env); err != nil {
		t.Fatal(err)
	}
	c.nextSeq++
	c.lastID = env.EventID
	lg.Close()

	// TTL passes; housekeeping removes the object (refs come from the
	// projection in M3+; supplied directly here)
	now := publishedAt.Add(config.EphemeralTTL + 24*time.Hour)
	refs := []object.Ref{{Hash: hash, TextClass: object.ClassEphemeral, CreatedAt: publishedAt}}
	deleted, err := store.HousekeepEphemeral(refs, now)
	if err != nil || len(deleted) != 1 {
		t.Fatalf("housekeeping: %v %v", deleted, err)
	}

	// the publish event REPLAYS FINE after the object is gone
	found := false
	relg, report, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify,
		func(e *event.Envelope, _ []byte) error {
			if e.EventID == env.EventID {
				found = true
			}
			return nil
		})
	if err != nil {
		t.Fatalf("recovery after expiry: %v", err)
	}
	relg.Close()
	if !found || report.Events != 3 {
		t.Fatalf("publish event lost after expiry: found=%v events=%d", found, report.Events)
	}

	// doctor (log integrity) stays clean — expiry is not corruption
	doc, err := cairnlog.Doctor(m, "/p", identity.NewChainVerifier().Verify)
	if err != nil || !doc.Clean() {
		t.Fatalf("doctor after expiry: %v clean=%v", err, doc != nil && doc.Clean())
	}

	// fetch returns the typed content_expired result
	_, err = store.Fetch(hash, refs, now)
	var exp *object.ExpiredError
	if !errors.As(err, &exp) {
		t.Fatalf("want *object.ExpiredError, got %v", err)
	}

	// racing variant: fetch BEFORE housekeeping ran would have served bytes;
	// fetch immediately after deletion is typed — never a crash. (Full race
	// against reindex lands with M3.)
	if _, err := store.Fetch(hash, refs, now); err == nil {
		t.Fatal("expired fetch served data")
	}
}
