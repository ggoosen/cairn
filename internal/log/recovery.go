package log

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
)

// Mode selects recovery behavior: Repair may truncate a trailing invalid
// frame of the open segment and re-attempt a pending seal; VerifyOnly (used
// by doctor) mutates NOTHING and reports instead.
type Mode int

const (
	Repair Mode = iota
	VerifyOnly
)

// Problem is one defect found while scanning (doctor output).
type Problem struct {
	Segment string `json:"segment"`
	Detail  string `json:"detail"`
}

// RecoveryReport summarizes one origin scan.
type RecoveryReport struct {
	Origin         Origin
	Segments       int
	SealedSegments int
	Events         int64
	NextSeq        int64
	LastEventID    string
	TruncatedBytes int64 // bytes of trailing invalid frame removed (Repair) or removable (VerifyOnly)
	Problems       []Problem
}

// OnRecord is called for every verified record during a scan, in order
// (projection replay hooks in here in M3).
type OnRecord func(env *event.Envelope, recordBytes []byte) error

// Open recovers an origin's log per rulings §3.5–3.7 and returns an
// append-ready handle: scan segments in order, validate every frame, verify
// every record (hash + signature via verify), check chain contiguity,
// truncate only an invalid TRAILING frame of the open segment, derive
// next_sequence from the log (cache is never consulted here — the verified
// log is authoritative), and complete a threshold-crossing seal the crash
// interrupted.
func Open(fsys fsx.FS, portableDir string, origin Origin, verify VerifyFunc, onRecord OnRecord, opts ...Option) (*Log, *RecoveryReport, error) {
	st, report, err := scanOrigin(fsys, portableDir, origin, verify, Repair, onRecord)
	if err != nil {
		return nil, report, err
	}

	l := &Log{
		fs:          fsys,
		dir:         SegmentDir(portableDir, origin.DeviceID, origin.Generation),
		origin:      origin,
		verify:      verify,
		nextSeq:     st.nextSeq,
		lastEventID: st.lastEventID,
		sealBytes:   config.SegmentSealBytes,
		sealEvents:  config.SegmentSealEvents,
	}
	for _, o := range opts {
		o(l)
	}

	if st.openSegment != "" {
		f, err := fsys.OpenFile(st.openSegment, os.O_WRONLY|os.O_APPEND, config.FilePerm)
		if err != nil {
			return nil, report, fmt.Errorf("reopening segment: %w", err)
		}
		l.file = f
		l.segPath = st.openSegment
		l.segFirstSeq = st.openFirstSeq
		l.segEventIDs = st.openEventIDs
		l.segBytes = st.openBytes

		// A crash between threshold-crossing append and seal leaves an
		// over-threshold open segment; complete the seal now.
		if l.segBytes >= l.sealBytes || len(l.segEventIDs) >= l.sealEvents {
			if err := l.seal(); err != nil {
				return nil, report, fmt.Errorf("completing interrupted seal: %w", err)
			}
		}
	}
	return l, report, nil
}

type scanState struct {
	nextSeq      int64
	lastEventID  string
	openSegment  string // path of the unsealed last segment ("" if none/sealed)
	openFirstSeq int64
	openEventIDs []string
	openBytes    int64
}

// scanOrigin walks one (origin, generation) directory. Interior corruption
// (any defect not confined to the trailing frame of the unsealed last
// segment) is a hard error in Repair mode and a Problem in VerifyOnly mode —
// never a silent skip.
func scanOrigin(fsys fsx.FS, portableDir string, origin Origin, verify VerifyFunc, mode Mode, onRecord OnRecord) (*scanState, *RecoveryReport, error) {
	dir := SegmentDir(portableDir, origin.DeviceID, origin.Generation)
	report := &RecoveryReport{Origin: origin, NextSeq: config.FirstSequence}
	st := &scanState{nextSeq: config.FirstSequence}

	entries, err := fsys.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, report, nil // fresh origin
		}
		return nil, report, err
	}

	type segInfo struct {
		firstSeq int64
		path     string
		sealed   *SealHeader
	}
	var segs []segInfo
	for _, e := range entries {
		if fs, ok := parseSegmentName(e.Name()); ok {
			segs = append(segs, segInfo{firstSeq: fs, path: filepath.Join(dir, e.Name())})
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].firstSeq < segs[j].firstSeq })
	for i := range segs {
		blob, err := fsys.ReadFile(filepath.Join(dir, sealName(segs[i].firstSeq)))
		if err == nil {
			var h SealHeader
			if jerr := json.Unmarshal(blob, &h); jerr != nil {
				return nil, report, fail(report, mode, segs[i].path, fmt.Sprintf("unparseable seal header: %v", jerr))
			}
			segs[i].sealed = &h
		}
	}
	report.Segments = len(segs)

	hardErr := func(seg, detail string) error { return fail(report, mode, seg, detail) }

	for i, seg := range segs {
		last := i == len(segs)-1
		if seg.firstSeq != st.nextSeq {
			return nil, report, hardErr(seg.path, fmt.Sprintf("segment starts at seq %d, expected %d (gap or overlap)", seg.firstSeq, st.nextSeq))
		}

		blob, err := fsys.ReadFile(seg.path)
		if err != nil {
			return nil, report, hardErr(seg.path, fmt.Sprintf("unreadable: %v", err))
		}
		var (
			eventIDs []string
			offset   int64
			lastGood int64
			frameErr error
			r        = bytes.NewReader(blob)
		)
		for {
			rec, err := ReadFrame(r)
			if err == io.EOF {
				break
			}
			if err != nil {
				frameErr = err
				break
			}
			offset = int64(len(blob)) - int64(r.Len())

			env, verr := verify(rec)
			if verr != nil {
				return nil, report, hardErr(seg.path, fmt.Sprintf("record at seq %d failed verification: %v", st.nextSeq, verr))
			}
			if env.OriginDeviceID != origin.DeviceID || env.OriginGeneration != origin.Generation {
				return nil, report, hardErr(seg.path, fmt.Sprintf("event %s belongs to origin %s/%d, found in %s/%d", env.EventID, env.OriginDeviceID, env.OriginGeneration, origin.DeviceID, origin.Generation))
			}
			if env.OriginSequence != st.nextSeq {
				return nil, report, hardErr(seg.path, fmt.Sprintf("sequence %d where %d expected (chain gap)", env.OriginSequence, st.nextSeq))
			}
			if env.PreviousOriginEventID != st.lastEventID {
				return nil, report, hardErr(seg.path, fmt.Sprintf("broken chain at seq %d: previous_origin_event_id %q, chain head %q", env.OriginSequence, env.PreviousOriginEventID, st.lastEventID))
			}
			if onRecord != nil {
				if err := onRecord(env, rec); err != nil {
					return nil, report, err
				}
			}
			eventIDs = append(eventIDs, env.EventID)
			st.lastEventID = env.EventID
			st.nextSeq++
			report.Events++
			lastGood = offset
		}

		if frameErr != nil {
			// "Trailing" means nothing valid follows the bad frame. A corrupt
			// frame with a later valid frame after it is INTERIOR corruption:
			// truncating would silently drop acked events — hard error
			// (TESTING.md §2: never a silent skip).
			trailing := last && seg.sealed == nil && !hasLaterValidFrame(blob[lastGood:])
			if !trailing {
				return nil, report, hardErr(seg.path, fmt.Sprintf("interior corruption (%v) — refusing to truncate a sealed, non-final, or non-trailing region", frameErr))
			}
			report.TruncatedBytes = int64(len(blob)) - lastGood
			if mode == Repair {
				if err := truncateFile(fsys, seg.path, lastGood); err != nil {
					return nil, report, fmt.Errorf("truncating trailing frame: %w", err)
				}
			} else {
				report.Problems = append(report.Problems, Problem{Segment: seg.path,
					Detail: fmt.Sprintf("trailing invalid frame (%v); recovery would truncate %d bytes", frameErr, report.TruncatedBytes)})
			}
		}

		if seg.sealed != nil {
			report.SealedSegments++
			h := seg.sealed
			lastSeq := st.nextSeq - 1
			if h.FirstSeq != seg.firstSeq || h.LastSeq != lastSeq || h.EventCount != int64(len(eventIDs)) || h.RootHash != segmentRootHash(eventIDs) {
				return nil, report, hardErr(seg.path, fmt.Sprintf("seal header mismatch: header {first %d last %d count %d} vs actual {first %d last %d count %d} or root hash differs",
					h.FirstSeq, h.LastSeq, h.EventCount, seg.firstSeq, lastSeq, len(eventIDs)))
			}
		} else {
			if !last {
				return nil, report, hardErr(seg.path, "unsealed segment followed by later segments")
			}
			st.openSegment = seg.path
			st.openFirstSeq = seg.firstSeq
			st.openEventIDs = eventIDs
			st.openBytes = lastGood
			if frameErr == nil {
				st.openBytes = int64(len(blob))
			}
		}
	}

	report.NextSeq = st.nextSeq
	report.LastEventID = st.lastEventID
	return st, report, nil
}

// fail: in Repair mode a defect aborts recovery with an error; in VerifyOnly
// it is recorded and scanning of this origin stops (the chain state past a
// defect is meaningless).
func fail(report *RecoveryReport, mode Mode, seg, detail string) error {
	report.Problems = append(report.Problems, Problem{Segment: seg, Detail: detail})
	if mode == Repair {
		return fmt.Errorf("%s: %s", seg, detail)
	}
	return errStopScan
}

var errStopScan = errors.New("scan stopped at first defect")

// hasLaterValidFrame reports whether any complete, CRC-valid frame exists
// after the start of the failed frame (resync by magic search). Used to
// distinguish a torn tail (truncatable) from interior corruption (hard error).
func hasLaterValidFrame(data []byte) bool {
	for i := 1; i < len(data); {
		j := bytes.Index(data[i:], []byte(config.FrameMagic))
		if j < 0 {
			return false
		}
		start := i + j
		if _, err := ReadFrame(bytes.NewReader(data[start:])); err == nil {
			return true
		}
		i = start + 1
	}
	return false
}

func truncateFile(fsys fsx.FS, path string, size int64) error {
	f, err := fsys.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
