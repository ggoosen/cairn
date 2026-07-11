package identity

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// ReconcileSeqState enforces rulings §3.5: sequence-state files are CACHES;
// the verified log is authoritative. Called after recovery with the
// log-derived next sequence:
//   - cache missing/garbled → rewritten from the log (crash row 8)
//   - cache BEHIND the log  → updated silently (normal after crash)
//   - cache AHEAD of the log → the log wins; cache reset, warning logged
//
// Returns the reconciled state. The cache write itself needs no fsync — its
// loss is always recoverable from the log.
func ReconcileSeqState(fsys fsx.FS, deviceDir, originDeviceID string, generation int, logNextSeq int64, warn io.Writer) (SeqState, error) {
	path := filepath.Join(deviceDir, config.SeqStateName)
	truth := SeqState{
		OriginDeviceID:   originDeviceID,
		OriginGeneration: generation,
		NextSequence:     logNextSeq,
	}

	blob, err := fsys.ReadFile(path)
	if err == nil {
		var cached SeqState
		if jerr := json.Unmarshal(blob, &cached); jerr != nil {
			fmt.Fprintf(warn, "WARNING: sequence-state cache unreadable (%v); rebuilt from log\n", jerr)
		} else if cached.OriginDeviceID == originDeviceID && cached.OriginGeneration == generation {
			if cached.NextSequence > logNextSeq {
				fmt.Fprintf(warn, "WARNING: sequence-state cache ahead of log (cache %d, log %d); the verified log is authoritative — cache reset\n",
					cached.NextSequence, logNextSeq)
			}
			// behind or equal: nothing to report; log still wins below
		}
	}

	out, err := json.Marshal(truth)
	if err != nil {
		return truth, err
	}
	f, err := fsys.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, config.KeyFilePerm)
	if err != nil {
		return truth, err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return truth, err
	}
	if err := f.Close(); err != nil {
		return truth, err
	}
	if err := fsys.Rename(path+".tmp", path); err != nil {
		return truth, err
	}
	return truth, nil
}
