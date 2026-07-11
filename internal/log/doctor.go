package log

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// DoctorReport is the result of `cairn doctor` over the whole events tree:
// every origin walked, every frame validated, every record verified
// (hash + signature + chain), seal headers checked. Doctor NEVER repairs —
// it reports; recovery (daemon startup) repairs the one thing it may:
// a trailing invalid frame.
type DoctorReport struct {
	Origins []RecoveryReport
}

// Clean reports whether nothing at all is wrong.
func (r *DoctorReport) Clean() bool {
	for _, o := range r.Origins {
		if len(o.Problems) > 0 {
			return false
		}
	}
	return true
}

// Summary renders a human-readable report.
func (r *DoctorReport) Summary(w io.Writer) {
	for _, o := range r.Origins {
		fmt.Fprintf(w, "origin %s/%d: %d segments (%d sealed), %d events, next seq %d\n",
			o.Origin.DeviceID, o.Origin.Generation, o.Segments, o.SealedSegments, o.Events, o.NextSeq)
		for _, p := range o.Problems {
			fmt.Fprintf(w, "  PROBLEM %s: %s\n", p.Segment, p.Detail)
		}
	}
	if r.Clean() {
		fmt.Fprintln(w, "doctor: clean")
	} else {
		fmt.Fprintln(w, "doctor: PROBLEMS FOUND (see above)")
	}
}

// Doctor walks all (origin, generation) directories under the portable dir
// in VerifyOnly mode. verifyFor supplies a fresh verifier per walk so key
// learning starts from genesis each time.
func Doctor(fsys fsx.FS, portableDir string, verify VerifyFunc) (*DoctorReport, error) {
	report := &DoctorReport{}
	origins, err := Origins(fsys, portableDir)
	if err != nil {
		return nil, err
	}
	for _, origin := range origins {
		_, oreport, err := scanOrigin(fsys, portableDir, origin, verify, VerifyOnly, nil)
		if err != nil && !errors.Is(err, errStopScan) {
			return nil, err
		}
		report.Origins = append(report.Origins, *oreport)
	}
	return report, nil
}

// Origins enumerates every (origin_device_id, generation) directory under
// the portable dir, sorted deterministically (device id, then generation).
func Origins(fsys fsx.FS, portableDir string) ([]Origin, error) {
	eventsDir := filepath.Join(portableDir, config.EventsDirName)
	deviceDirs, err := fsys.ReadDir(eventsDir)
	if err != nil {
		return nil, fmt.Errorf("reading events dir: %w", err)
	}
	var out []Origin
	for _, dd := range deviceDirs {
		if !dd.IsDir() {
			continue
		}
		genDirs, err := fsys.ReadDir(filepath.Join(eventsDir, dd.Name()))
		if err != nil {
			return nil, err
		}
		for _, gd := range genDirs {
			if !gd.IsDir() {
				continue
			}
			gen, err := strconv.Atoi(gd.Name())
			if err != nil {
				continue
			}
			out = append(out, Origin{DeviceID: dd.Name(), Generation: gen})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceID != out[j].DeviceID {
			return out[i].DeviceID < out[j].DeviceID
		}
		return out[i].Generation < out[j].Generation
	})
	return out, nil
}
