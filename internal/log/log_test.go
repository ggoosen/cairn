package log_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// chain builds signed, chained test events the way the daemon will.
type chain struct {
	cairnID  string
	deviceID string
	devPriv  ed25519.PrivateKey
	keyID    string
	nextSeq  int64
	lastID   string
}

func newChain(t testing.TB) (*chain, *event.Envelope, []byte) {
	t.Helper()
	rootPub, rootPriv, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	devPub, devPriv, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c := &chain{
		cairnID:  "0190a1b2-c3d4-7e5f-8901-234567890abc",
		deviceID: "0190a1b2-c3d4-7e5f-8901-234567890def",
		devPriv:  devPriv,
		keyID:    event.KeyID(devPub),
	}
	cert := identity.DeviceCert{
		DeviceID:   c.deviceID,
		Pubkey:     base64.StdEncoding.EncodeToString(devPub),
		Generation: 1,
		IssuedAt:   "2026-07-11T00:00:00.000000Z",
	}
	if err := cert.SignCert(rootPriv); err != nil {
		t.Fatal(err)
	}
	env, record, err := identity.BuildGenesis(c.cairnID, rootPub, cert, devPriv, "2026-07-11T00:00:00.000000Z")
	if err != nil {
		t.Fatal(err)
	}
	c.nextSeq = config.FirstSequence + 1
	c.lastID = env.EventID
	return c, env, record
}

// next returns the next signed event in the chain.
func (c *chain) next(t testing.TB) (*event.Envelope, []byte) {
	t.Helper()
	env := &event.Envelope{
		SchemaVersion:         config.EventSchemaVersion,
		CairnID:               c.cairnID,
		EventType:             "signal.emit",
		OriginDeviceID:        c.deviceID,
		OriginGeneration:      1,
		OriginSequence:        c.nextSeq,
		PreviousOriginEventID: c.lastID,
		ActorPrincipalID:      "operator",
		ObjectType:            "message",
		ObjectID:              "0190a1b2-c3d4-7e5f-8901-234567890aaa",
		WallTime:              "2026-07-11T00:00:01.000000Z",
		PayloadSchema:         config.PayloadSchemaID,
		Payload:               json.RawMessage(fmt.Sprintf(`{"message_id":"0190a1b2-c3d4-7e5f-8901-234567890aaa","kind":"important","seq_marker":%d}`, c.nextSeq)),
		SigningKeyID:          c.keyID,
	}
	record, err := env.Sign(c.devPriv)
	if err != nil {
		t.Fatal(err)
	}
	c.nextSeq++
	c.lastID = env.EventID
	return env, record
}

func origin(c *chain) cairnlog.Origin {
	return cairnlog.Origin{DeviceID: c.deviceID, Generation: 1}
}

// newLogWithGenesis creates a fresh log on fsys and appends genesis + n events.
func newLogWithGenesis(t testing.TB, fsys fsx.FS, dir string, n int) (*chain, *cairnlog.Log) {
	t.Helper()
	c, genv, grec := newChain(t)
	lg, err := cairnlog.Create(fsys, dir, origin(c))
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Append(grec, genv); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		env, rec := c.next(t)
		if err := lg.Append(rec, env); err != nil {
			t.Fatal(err)
		}
	}
	return c, lg
}

func TestAppendRecoverRoundTrip(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	c, lg := newLogWithGenesis(t, m, "/p", 5)
	lg.Close()

	var seqs []int64
	relg, report, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify,
		func(env *event.Envelope, _ []byte) error {
			seqs = append(seqs, env.OriginSequence)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer relg.Close()
	if report.Events != 6 || relg.NextSeq() != 7 {
		t.Fatalf("recovered %d events, next seq %d; want 6, 7", report.Events, relg.NextSeq())
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("replay out of order: %v", seqs)
		}
	}
	// the recovered log must accept appends continuing the chain
	c2 := &chain{cairnID: c.cairnID, deviceID: c.deviceID, devPriv: c.devPriv, keyID: c.keyID,
		nextSeq: relg.NextSeq(), lastID: relg.LastEventID()}
	env, rec := c2.next(t)
	if err := relg.Append(rec, env); err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
}

func TestTrailingFrameTruncatedOnRecovery(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	c, lg := newLogWithGenesis(t, m, "/p", 3)
	lg.Close()

	segPath := filepath.Join(cairnlog.SegmentDir("/p", c.deviceID, 1), cairnlog.SegmentName(1))
	// simulate a torn append: half a frame of garbage at the tail
	f, err := m.OpenFile(segPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("CRNF\x01GARBAGE-PARTIAL-FRAME"))
	f.Close()

	relg, report, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify, nil)
	if err != nil {
		t.Fatalf("recovery failed on trailing garbage: %v", err)
	}
	defer relg.Close()
	if report.TruncatedBytes == 0 {
		t.Fatal("nothing truncated")
	}
	if report.Events != 4 || relg.NextSeq() != 5 {
		t.Fatalf("events %d next %d; want 4, 5", report.Events, relg.NextSeq())
	}

	// second recovery: clean, nothing more to truncate
	relg.Close()
	_, report2, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify, nil)
	if err != nil || report2.TruncatedBytes != 0 {
		t.Fatalf("second recovery: err %v truncated %d", err, report2.TruncatedBytes)
	}
}

func TestInteriorCorruptionIsHardError(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	c, lg := newLogWithGenesis(t, m, "/p", 3)
	lg.Close()

	segPath := filepath.Join(cairnlog.SegmentDir("/p", c.deviceID, 1), cairnlog.SegmentName(1))
	blob, _ := m.ReadFile(segPath)
	blob[len(blob)/2] ^= 0x01 // bit-flip mid-file (interior frame)
	f, _ := m.OpenFile(segPath, os.O_WRONLY|os.O_TRUNC, 0o644)
	f.Write(blob)
	f.Close()

	_, _, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify, nil)
	if err == nil {
		t.Fatal("interior corruption silently accepted")
	}
	if strings.Contains(err.Error(), "truncat") && !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("interior corruption must not be repaired by truncation: %v", err)
	}
}

// A hash-valid frame whose record has a broken chain (wrong previous id) is
// rejected — and doctor reports it (M1 acceptance: doctor detects a broken
// chain).
func TestBrokenChainDetected(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	c, lg := newLogWithGenesis(t, m, "/p", 2)
	lg.Close()

	// hand-craft seq 4 with a WRONG previous_origin_event_id, properly signed
	forged := &chain{cairnID: c.cairnID, deviceID: c.deviceID, devPriv: c.devPriv, keyID: c.keyID,
		nextSeq: c.nextSeq, lastID: "0000000000000000000000000000000000000000000000000000000000000000"}
	_, rec := forged.next(t)

	segPath := filepath.Join(cairnlog.SegmentDir("/p", c.deviceID, 1), cairnlog.SegmentName(1))
	var fbuf bytes.Buffer
	cairnlog.EncodeFrame(&fbuf, rec)
	f, _ := m.OpenFile(segPath, os.O_WRONLY|os.O_APPEND, 0o644)
	f.Write(fbuf.Bytes())
	f.Close()

	if _, _, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify, nil); err == nil {
		t.Fatal("broken chain accepted by recovery")
	}

	doc, err := cairnlog.Doctor(m, "/p", identity.NewChainVerifier().Verify)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Clean() {
		t.Fatal("doctor reported clean on a broken chain")
	}
	found := false
	for _, o := range doc.Origins {
		for _, p := range o.Problems {
			if strings.Contains(p.Detail, "chain") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("doctor did not name the chain break: %+v", doc.Origins)
	}
}

// Doctor detects a deliberately corrupted interior frame (M1 acceptance) and
// never mutates the log.
func TestDoctorDetectsCorruptFrameWithoutRepairing(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	c, lg := newLogWithGenesis(t, m, "/p", 3)
	lg.Close()

	segPath := filepath.Join(cairnlog.SegmentDir("/p", c.deviceID, 1), cairnlog.SegmentName(1))
	blob, _ := m.ReadFile(segPath)
	corrupted := append([]byte(nil), blob...)
	corrupted[len(corrupted)/2] ^= 0x01
	f, _ := m.OpenFile(segPath, os.O_WRONLY|os.O_TRUNC, 0o644)
	f.Write(corrupted)
	f.Close()

	doc, err := cairnlog.Doctor(m, "/p", identity.NewChainVerifier().Verify)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Clean() {
		t.Fatal("doctor clean on corrupted frame")
	}
	after, _ := m.ReadFile(segPath)
	if string(after) != string(corrupted) {
		t.Fatal("doctor mutated the segment")
	}
}

func TestDoctorCleanOnHealthyLog(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	_, lg := newLogWithGenesis(t, m, "/p", 10)
	lg.Close()
	doc, err := cairnlog.Doctor(m, "/p", identity.NewChainVerifier().Verify)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Clean() {
		t.Fatalf("healthy log reported problems: %+v", doc.Origins)
	}
}

// Append refuses out-of-order sequences and wrong chain heads.
func TestAppendValidatesChainPosition(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	c, lg := newLogWithGenesis(t, m, "/p", 1)
	defer lg.Close()

	// wrong sequence
	bad := &chain{cairnID: c.cairnID, deviceID: c.deviceID, devPriv: c.devPriv, keyID: c.keyID,
		nextSeq: c.nextSeq + 5, lastID: c.lastID}
	env, rec := bad.next(t)
	if err := lg.Append(rec, env); err == nil {
		t.Fatal("out-of-order sequence accepted")
	}

	// wrong previous event id
	bad2 := &chain{cairnID: c.cairnID, deviceID: c.deviceID, devPriv: c.devPriv, keyID: c.keyID,
		nextSeq: c.nextSeq, lastID: "beef"}
	env2, rec2 := bad2.next(t)
	if err := lg.Append(rec2, env2); err == nil {
		t.Fatal("wrong chain head accepted")
	}
}
