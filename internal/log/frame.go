// Package log implements the append-only event log: frame format, segments,
// durable append, seal, recovery, and doctor (rulings §3).
package log

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
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
	if n > config.MaxRecordBytes {
		return nil, fmt.Errorf("frame length %d exceeds MaxRecordBytes (corrupt length field)", n)
	}
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

// ReadSegment returns all complete records in a segment file (no
// verification — recovery and doctor verify; this is the raw reader).
func ReadSegment(fsys fsx.FS, path string) ([][]byte, error) {
	blob, err := fsys.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := bytes.NewReader(blob)
	var records [][]byte
	for {
		rec, err := ReadFrame(r)
		if err == io.EOF {
			return records, nil
		}
		if err != nil {
			return records, err
		}
		records = append(records, rec)
	}
}
