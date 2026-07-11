package log

import (
	"bytes"
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

func TestWriteInitialSegment(t *testing.T) {
	dir := t.TempDir()
	rec := []byte(`{"event":"genesis"}`)
	path, err := WriteInitialSegment(dir, "device-1", 1, 1, [][]byte{rec})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "events", "device-1", "1", "seg_00000001.seg")
	if path != want {
		t.Fatalf("segment path %s, want %s", path, want)
	}
	got, err := ReadSegment(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !bytes.Equal(got[0], rec) {
		t.Fatalf("segment contents wrong: %q", got)
	}
	// never overwrite
	if _, err := WriteInitialSegment(dir, "device-1", 1, 1, [][]byte{rec}); err == nil {
		t.Fatal("initial segment overwritten")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
