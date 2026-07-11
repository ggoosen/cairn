// Package log implements the append-only event log: frame format, segments,
// durability ordering, seal, recovery, and doctor (rulings §3).
//
// M0 scope (pulled forward so `cairn init` persists genesis in the final
// on-disk format, not a throwaway one): frame encode/decode and the initial
// segment write with full fsync ordering. M1 adds append, recovery with
// trailing-frame truncation, sealing, and doctor.
package log

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"github.com/ggoosen/cairn/internal/config"
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Frame layout (rulings §3.2):
//
//	magic "CRNF" (4) | version (1) | u64 LE length of record_bytes (8) |
//	record_bytes | CRC32C over record_bytes (u32 LE)
//
// Framing detects corruption; signatures provide cryptographic integrity.

// EncodeFrame appends one framed record to buf.
func EncodeFrame(buf *bytes.Buffer, recordBytes []byte) {
	buf.WriteString(config.FrameMagic)
	buf.WriteByte(config.FrameVersion)
	var lenb [8]byte
	binary.LittleEndian.PutUint64(lenb[:], uint64(len(recordBytes)))
	buf.Write(lenb[:])
	buf.Write(recordBytes)
	var crcb [4]byte
	binary.LittleEndian.PutUint32(crcb[:], crc32.Checksum(recordBytes, castagnoli))
	buf.Write(crcb[:])
}

// ReadFrame reads one frame from r. io.EOF at a frame boundary means a clean
// end; any other error indicates a truncated or corrupt frame.
func ReadFrame(r io.Reader) ([]byte, error) {
	head := make([]byte, len(config.FrameMagic)+1+8)
	if _, err := io.ReadFull(r, head); err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("truncated frame header: %w", err)
	}
	if string(head[:4]) != config.FrameMagic {
		return nil, fmt.Errorf("bad frame magic %q", head[:4])
	}
	if head[4] != config.FrameVersion {
		return nil, fmt.Errorf("unsupported frame version %d", head[4])
	}
	n := binary.LittleEndian.Uint64(head[5:13])
	record := make([]byte, n)
	if _, err := io.ReadFull(r, record); err != nil {
		return nil, fmt.Errorf("truncated record: %w", err)
	}
	var crcb [4]byte
	if _, err := io.ReadFull(r, crcb[:]); err != nil {
		return nil, fmt.Errorf("truncated checksum: %w", err)
	}
	if got := binary.LittleEndian.Uint32(crcb[:]); got != crc32.Checksum(record, castagnoli) {
		return nil, fmt.Errorf("CRC32C mismatch")
	}
	return record, nil
}

// SegmentDir returns events/<origin_device_id>/<generation> under the
// portable dir. One directory per (origin, generation) — rulings §3.7.
func SegmentDir(portableDir, originDeviceID string, generation int) string {
	return filepath.Join(portableDir, config.EventsDirName, originDeviceID, fmt.Sprintf("%d", generation))
}

// SegmentName names a segment by its first sequence number.
func SegmentName(firstSeq int64) string {
	return fmt.Sprintf("seg_%08d.seg", firstSeq)
}

// WriteInitialSegment creates the first open segment for an origin and
// appends the given records with the durability ordering of rulings §3:
// write → fdatasync file → fsync the directory chain. Fails if the segment
// already exists (never overwrite).
func WriteInitialSegment(portableDir, originDeviceID string, generation int, firstSeq int64, records [][]byte) (string, error) {
	dir := SegmentDir(portableDir, originDeviceID, generation)
	if err := os.MkdirAll(dir, config.DirPerm); err != nil {
		return "", err
	}
	path := filepath.Join(dir, SegmentName(firstSeq))

	var buf bytes.Buffer
	for _, rec := range records {
		EncodeFrame(&buf, rec)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, config.FilePerm)
	if err != nil {
		return "", fmt.Errorf("creating segment: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	// fsync the directory chain (new dirs + new segment entry) so the
	// entries themselves are durable: <gen>/ → <origin>/ → events/ → root.
	for d := dir; ; d = filepath.Dir(d) {
		if err := syncDir(d); err != nil {
			return "", err
		}
		if d == portableDir {
			break
		}
	}
	return path, nil
}

// ReadSegment returns all complete records in a segment file.
func ReadSegment(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var records [][]byte
	for {
		rec, err := ReadFrame(f)
		if err == io.EOF {
			return records, nil
		}
		if err != nil {
			return records, err
		}
		records = append(records, rec)
	}
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	cerr := d.Close()
	if err != nil {
		return fmt.Errorf("fsync dir %s: %w", dir, err)
	}
	return cerr
}
