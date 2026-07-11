package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// TESTING.md: seq cache BEHIND log (fine, reconstructed) and AHEAD of log
// (log wins, cache reset, warning logged).
func TestReconcileSeqState(t *testing.T) {
	fsys := fsx.OS{}
	dev := t.TempDir()
	path := filepath.Join(dev, config.SeqStateName)
	writeCache := func(next int64) {
		blob, _ := json.Marshal(SeqState{OriginDeviceID: "dev-1", OriginGeneration: 1, NextSequence: next})
		if err := os.WriteFile(path, blob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() SeqState {
		var st SeqState
		blob, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(blob, &st)
		return st
	}

	// behind: silently reconstructed
	writeCache(3)
	var warn strings.Builder
	st, err := ReconcileSeqState(fsys, dev, "dev-1", 1, 10, &warn)
	if err != nil || st.NextSequence != 10 || read().NextSequence != 10 {
		t.Fatalf("behind: %+v %v", st, err)
	}
	if strings.Contains(warn.String(), "WARNING") {
		t.Fatalf("behind cache should not warn: %s", warn.String())
	}

	// ahead: log wins, warning logged
	writeCache(99)
	warn.Reset()
	st, err = ReconcileSeqState(fsys, dev, "dev-1", 1, 10, &warn)
	if err != nil || st.NextSequence != 10 || read().NextSequence != 10 {
		t.Fatalf("ahead: %+v %v", st, err)
	}
	if !strings.Contains(warn.String(), "ahead of log") {
		t.Fatalf("no ahead warning: %q", warn.String())
	}

	// garbled: rebuilt with warning
	os.WriteFile(path, []byte("{not json"), 0o600)
	warn.Reset()
	st, err = ReconcileSeqState(fsys, dev, "dev-1", 1, 7, &warn)
	if err != nil || st.NextSequence != 7 || read().NextSequence != 7 {
		t.Fatalf("garbled: %+v %v", st, err)
	}
	if !strings.Contains(warn.String(), "unreadable") {
		t.Fatalf("no garbled warning: %q", warn.String())
	}

	// missing: created silently
	os.Remove(path)
	st, err = ReconcileSeqState(fsys, dev, "dev-1", 1, 5, &warn)
	if err != nil || read().NextSequence != 5 {
		t.Fatalf("missing: %+v %v", st, err)
	}
}
