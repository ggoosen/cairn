package log

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"lukechampine.com/blake3"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
)

// Origin identifies one append chain: sequences are scoped to
// (origin_device_id, origin_generation); ordering is never wall time.
type Origin struct {
	DeviceID   string
	Generation int
}

// VerifyFunc fully verifies one record (canonical form, event_id hash,
// signature via the key chain) and returns its envelope. Implemented by
// identity.ChainVerifier; the log package stays crypto-policy-free.
type VerifyFunc func(recordBytes []byte) (*event.Envelope, error)

// SealHeader is the sidecar written when a segment seals (rulings §3.7).
// The .seg file itself is immutable from creation; the sidecar's existence
// IS the sealed state, so sealing is a single atomic publish.
type SealHeader struct {
	FirstSeq   int64  `json:"first_seq"`
	LastSeq    int64  `json:"last_seq"`
	EventCount int64  `json:"event_count"`
	RootHash   string `json:"root_hash"` // BLAKE3 over ordered raw event-id bytes
}

// Log is the single-writer append handle for one origin.
type Log struct {
	fs     fsx.FS
	dir    string // segment directory for (origin, generation)
	origin Origin
	verify VerifyFunc

	file        fsx.File // open segment handle (nil until first append needs it)
	segPath     string
	segFirstSeq int64
	segEventIDs []string
	segBytes    int64

	nextSeq     int64
	lastEventID string
	closed      bool
}

// SegmentDir returns events/<origin_device_id>/<generation> under the portable dir.
func SegmentDir(portableDir, originDeviceID string, generation int) string {
	return filepath.Join(portableDir, config.EventsDirName, originDeviceID, strconv.Itoa(generation))
}

// SegmentName names a segment by its first sequence number.
func SegmentName(firstSeq int64) string {
	return fmt.Sprintf("seg_%08d.seg", firstSeq)
}

func sealName(firstSeq int64) string {
	return fmt.Sprintf("seg_%08d.seal.json", firstSeq)
}

func parseSegmentName(name string) (int64, bool) {
	if !strings.HasPrefix(name, "seg_") || !strings.HasSuffix(name, ".seg") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, "seg_"), ".seg"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Create initializes an empty log for a brand-new origin (used by init).
func Create(fsys fsx.FS, portableDir string, origin Origin) (*Log, error) {
	dir := SegmentDir(portableDir, origin.DeviceID, origin.Generation)
	if err := fsys.MkdirAll(dir, config.DirPerm); err != nil {
		return nil, err
	}
	if entries, err := fsys.ReadDir(dir); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("origin %s/%d already has segments", origin.DeviceID, origin.Generation)
	}
	return &Log{
		fs:      fsys,
		dir:     dir,
		origin:  origin,
		nextSeq: config.FirstSequence,
	}, nil
}

// Append durably appends one signed record (rulings §3 ordering — the caller
// has already persisted any referenced objects):
//
//	frame → write to open segment → fdatasync → (dir-fsync if the segment
//	file is new) → return. The CALLER acknowledges only after Append returns.
//
// Append validates chain position: the envelope must carry exactly the next
// sequence and the previous event id this log knows.
func (l *Log) Append(recordBytes []byte, env *event.Envelope) error {
	if l.closed {
		return errors.New("log is closed")
	}
	if env.OriginDeviceID != l.origin.DeviceID || env.OriginGeneration != l.origin.Generation {
		return fmt.Errorf("event origin %s/%d does not match log origin %s/%d",
			env.OriginDeviceID, env.OriginGeneration, l.origin.DeviceID, l.origin.Generation)
	}
	if env.OriginSequence != l.nextSeq {
		return fmt.Errorf("sequence %d out of order (next is %d)", env.OriginSequence, l.nextSeq)
	}
	if env.PreviousOriginEventID != l.lastEventID {
		return fmt.Errorf("previous_origin_event_id %q does not match chain head %q",
			env.PreviousOriginEventID, l.lastEventID)
	}
	if env.EventID == "" {
		return errors.New("record is unsigned")
	}

	created := false
	if l.file == nil {
		l.segPath = filepath.Join(l.dir, SegmentName(l.nextSeq))
		f, err := l.fs.OpenFile(l.segPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, config.FilePerm)
		if err != nil {
			return fmt.Errorf("creating segment: %w", err)
		}
		l.file = f
		l.segFirstSeq = l.nextSeq
		l.segEventIDs = nil
		l.segBytes = 0
		created = true
	}

	var buf bytes.Buffer
	EncodeFrame(&buf, recordBytes)
	if _, err := l.file.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("appending frame: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("fdatasync segment: %w", err)
	}
	if created {
		if err := l.fs.SyncDir(l.dir); err != nil {
			return fmt.Errorf("fsync segment dir: %w", err)
		}
	}

	l.segBytes += int64(buf.Len())
	l.segEventIDs = append(l.segEventIDs, env.EventID)
	l.lastEventID = env.EventID
	l.nextSeq++

	if l.segBytes >= config.SegmentSealBytes || len(l.segEventIDs) >= config.SegmentSealEvents {
		if err := l.seal(); err != nil {
			// The append itself is durable; a failed seal leaves the segment
			// open and valid. Recovery re-attempts the seal.
			return fmt.Errorf("append durable but seal failed (recovery will retry): %w", err)
		}
	}
	return nil
}

// seal freezes the open segment: sidecar header via atomic publish, then the
// next append starts a new segment. Crash-safe: either the sidecar exists
// (sealed) or it does not (still open) — the .seg bytes never change.
func (l *Log) seal() error {
	if l.file == nil {
		return errors.New("no open segment to seal")
	}
	header := SealHeader{
		FirstSeq:   l.segFirstSeq,
		LastSeq:    l.nextSeq - 1,
		EventCount: int64(len(l.segEventIDs)),
		RootHash:   segmentRootHash(l.segEventIDs),
	}
	blob, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if err := fsx.WriteFileAtomic(l.fs, filepath.Join(l.dir, sealName(l.segFirstSeq)), blob, config.FilePerm); err != nil {
		return err
	}
	if err := l.file.Close(); err != nil {
		return err
	}
	l.file = nil
	l.segPath = ""
	l.segEventIDs = nil
	l.segBytes = 0
	return nil
}

// segmentRootHash: BLAKE3 over the ordered raw event-id bytes (rulings §3.7).
func segmentRootHash(eventIDs []string) string {
	h := blake3.New(32, nil)
	for _, id := range eventIDs {
		raw, err := hex.DecodeString(id)
		if err != nil {
			// event IDs are produced by our own hasher; non-hex is impossible
			// for verified records, and unverified records never reach seal.
			raw = []byte(id)
		}
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NextSeq and LastEventID expose chain state for event builders.
func (l *Log) NextSeq() int64      { return l.nextSeq }
func (l *Log) LastEventID() string { return l.lastEventID }

// Close releases the open segment handle (does not seal).
func (l *Log) Close() error {
	l.closed = true
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}
