package log

import (
	"bytes"
	"github.com/ggoosen/cairn/internal/fsx"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	records := [][]byte{[]byte(`{"a":1}`), []byte(`{"b":"two"}`), {}}
	for _, r := range records {
		EncodeFrame(&buf, r)
	}
	r := bytes.NewReader(buf.Bytes())
	for i, want := range records {
		got, err := ReadFrame(r)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("record %d: got %q want %q", i, got, want)
		}
	}
	if _, err := ReadFrame(r); err != io.EOF {
		t.Fatalf("want clean EOF, got %v", err)
	}
}

func TestFrameDetectsCorruption(t *testing.T) {
	var buf bytes.Buffer
	EncodeFrame(&buf, []byte(`{"payload":"content"}`))
	raw := buf.Bytes()

	// bit-flip inside record_bytes → CRC mismatch
	flipped := append([]byte(nil), raw...)
	flipped[len(flipped)-10] ^= 0x01
	if _, err := ReadFrame(bytes.NewReader(flipped)); err == nil {
		t.Fatal("bit flip not detected")
	}

	// truncated mid-record → error, not EOF
	if _, err := ReadFrame(bytes.NewReader(raw[:len(raw)-6])); err == nil || err == io.EOF {
		t.Fatalf("truncation not detected: %v", err)
	}

	// bad magic
	bad := append([]byte(nil), raw...)
	bad[0] = 'X'
	if _, err := ReadFrame(bytes.NewReader(bad)); err == nil {
		t.Fatal("bad magic not detected")
	}
}

func TestReadSegment(t *testing.T) {
	dir := t.TempDir()
	recs := [][]byte{[]byte(`{"a":1}`), []byte(`{"b":2}`)}
	var buf bytes.Buffer
	for _, r := range recs {
		EncodeFrame(&buf, r)
	}
	path := filepath.Join(dir, "seg_00000001.seg")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSegment(fsx.OS{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !bytes.Equal(got[0], recs[0]) || !bytes.Equal(got[1], recs[1]) {
		t.Fatalf("segment contents wrong: %q", got)
	}
}
