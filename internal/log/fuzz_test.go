package log_test

// CI-B3: the frame decoder consumes bytes straight off disk during recovery
// — it must never panic, whatever the segment contains: either a valid
// record round-trips or a typed error comes back.

import (
	"bytes"
	"io"
	"testing"

	cairnlog "github.com/ggoosen/cairn/internal/log"
)

func FuzzDecodeFrame(f *testing.F) {
	// seeds: a valid frame, a torn tail, corrupt magic, absurd length
	var valid bytes.Buffer
	cairnlog.EncodeFrame(&valid, []byte(`{"k":"v"}`))
	f.Add(valid.Bytes())
	f.Add(valid.Bytes()[:len(valid.Bytes())-3])
	f.Add([]byte("XXXX\x01\x00\x00\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte("CRNF\x01\xff\xff\xff\xff\xff\xff\xff\xff"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		for {
			rec, err := cairnlog.ReadFrame(r)
			if err != nil {
				if err != io.EOF && rec != nil && len(rec) > 0 {
					// errors and partial records must not both flow out
					t.Fatalf("ReadFrame returned bytes AND error %v", err)
				}
				return
			}
			// a decoded record must re-encode to a decodable frame
			var buf bytes.Buffer
			cairnlog.EncodeFrame(&buf, rec)
			if _, err := cairnlog.ReadFrame(bytes.NewReader(buf.Bytes())); err != nil {
				t.Fatalf("re-encoded frame does not decode: %v", err)
			}
		}
	})
}
