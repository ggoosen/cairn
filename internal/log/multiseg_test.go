package log_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// Sealing rolls to a new segment; recovery walks sealed + open segments as
// one contiguous chain and validates every seal header.
func TestMultiSegmentSealAndRecovery(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)

	c, genv, grec := newChain(t)
	lg, err := cairnlog.Create(m, "/p", origin(c), cairnlog.WithSealThresholds(1<<30, 3))
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Append(grec, genv); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ { // total 7 events → segments [1..3][4..6][7]
		env, rec := c.next(t)
		if err := lg.Append(rec, env); err != nil {
			t.Fatal(err)
		}
	}
	lg.Close()

	dir := cairnlog.SegmentDir("/p", c.deviceID, 1)
	for _, sealed := range []string{"seg_00000001.seal.json", "seg_00000004.seal.json"} {
		if _, err := m.ReadFile(filepath.Join(dir, sealed)); err != nil {
			t.Fatalf("missing seal sidecar %s: %v", sealed, err)
		}
	}

	var seqs []int64
	relg, report, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify,
		func(env *event.Envelope, _ []byte) error {
			seqs = append(seqs, env.OriginSequence)
			return nil
		}, cairnlog.WithSealThresholds(1<<30, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer relg.Close()
	if report.Segments != 3 || report.SealedSegments != 2 || report.Events != 7 || relg.NextSeq() != 8 {
		t.Fatalf("segments %d sealed %d events %d next %d", report.Segments, report.SealedSegments, report.Events, relg.NextSeq())
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("replay order: %v", seqs)
		}
	}

	// a tampered seal header must be a hard error
	sealPath := filepath.Join(dir, "seg_00000001.seal.json")
	blob, _ := m.ReadFile(sealPath)
	tampered := []byte(string(blob[:len(blob)-2]) + "9}") // mangle root hash tail
	f, _ := m.OpenFile(sealPath, os.O_WRONLY|os.O_TRUNC, 0o644)
	f.Write(tampered)
	f.Close()
	relg.Close()
	if _, _, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify, nil,
		cairnlog.WithSealThresholds(1<<30, 3)); err == nil {
		t.Fatal("tampered seal header accepted")
	}
	doc, err := cairnlog.Doctor(m, "/p", identity.NewChainVerifier().Verify)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Clean() {
		t.Fatal("doctor clean on tampered seal header")
	}
}
