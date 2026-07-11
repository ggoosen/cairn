package object

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// Text classes (spec §5.4). The sender declares; daemon policy may downgrade.
const (
	ClassCanonical = "canonical"
	ClassEager     = "eager-searchable"
	ClassEphemeral = "ephemeral"
)

// ValidClass reports whether a declared text class is known.
func ValidClass(c string) bool {
	return c == ClassCanonical || c == ClassEager || c == ClassEphemeral
}

// PolicyDecision records what the text-class policy did (the decision is
// audit-relevant: a downgrade must be visible in the receipt, M4).
type PolicyDecision struct {
	Effective  string
	Downgraded bool
	Reason     string
}

// ApplyPolicy enforces rulings §5 BEFORE the event is built (the effective
// class is what gets signed):
//   - bodies > 1 MiB declared canonical/eager auto-downgrade to ephemeral
//   - per-message canonical ceiling: oversized canonical downgrades
//   - daily canonical-byte ceiling: once exhausted, canonical downgrades
//   - ONLY the explicit operator CLI override (never outbox callers, M4
//     wires the flag) bypasses the downgrades
//   - never a rejection, never a review queue — downgrade and proceed
//
// usage is the tracker for the daily ceiling; callers pass nil to skip
// daily accounting (e.g. non-canonical bodies). perMsgLimit is the
// configurable per-message canonical ceiling (portable config;
// 0 → DefaultPerMessageCanonicalBytes) — it only bites when configured
// below the fixed 1 MiB auto-downgrade threshold.
func ApplyPolicy(declared string, bodyLen int64, operatorOverride bool, usage *UsageTracker, perMsgLimit int64, now time.Time) (PolicyDecision, error) {
	if !ValidClass(declared) {
		return PolicyDecision{}, fmt.Errorf("unknown text_class %q", declared)
	}
	d := PolicyDecision{Effective: declared}
	if declared == ClassEphemeral || operatorOverride {
		if declared == ClassCanonical && usage != nil {
			usage.Add(bodyLen, now)
		}
		return d, nil
	}

	if bodyLen > config.AutoDowngradeBytes {
		return PolicyDecision{Effective: ClassEphemeral, Downgraded: true,
			Reason: fmt.Sprintf("body %d bytes exceeds %d-byte %s limit; auto-downgraded to ephemeral (operator CLI override required to force)", bodyLen, int64(config.AutoDowngradeBytes), declared)}, nil
	}
	if declared == ClassCanonical {
		perMsg := perMsgLimit
		if perMsg == 0 {
			perMsg = config.DefaultPerMessageCanonicalBytes
		}
		if bodyLen > perMsg {
			return PolicyDecision{Effective: ClassEphemeral, Downgraded: true,
				Reason: fmt.Sprintf("body %d bytes exceeds per-message canonical ceiling %d; downgraded to ephemeral", bodyLen, perMsg)}, nil
		}
		if usage != nil {
			if !usage.Allow(bodyLen, now) {
				return PolicyDecision{Effective: ClassEphemeral, Downgraded: true,
					Reason: fmt.Sprintf("daily canonical-byte ceiling %d exhausted (%d used today); downgraded to ephemeral", usage.Limit, usage.usedToday(now))}, nil
			}
			usage.Add(bodyLen, now)
		}
	}
	return d, nil
}

// UsageTracker accounts canonical bytes per UTC day (derived state — fully
// rebuildable from the log — so it lives under .cairn/ and needs no fsync).
type UsageTracker struct {
	fs    fsx.FS
	path  string
	Limit int64

	Day   string `json:"day"` // "2006-01-02" UTC
	Bytes int64  `json:"bytes"`
}

// LoadUsage opens (or initializes) the tracker at
// <portable>/.cairn/canonical-usage.json.
func LoadUsage(fsys fsx.FS, portableDir string, limit int64) (*UsageTracker, error) {
	if limit == 0 {
		limit = config.DefaultDailyCanonicalBytes
	}
	u := &UsageTracker{
		fs:    fsys,
		path:  filepath.Join(portableDir, config.DerivedDirName, "canonical-usage.json"),
		Limit: limit,
	}
	if blob, err := fsys.ReadFile(u.path); err == nil {
		_ = json.Unmarshal(blob, u) // garbled derived state resets cleanly
	}
	return u, nil
}

func (u *UsageTracker) usedToday(now time.Time) int64 {
	if u.Day != now.UTC().Format("2006-01-02") {
		return 0
	}
	return u.Bytes
}

// Allow reports whether adding n canonical bytes stays within today's ceiling.
func (u *UsageTracker) Allow(n int64, now time.Time) bool {
	return u.usedToday(now)+n <= u.Limit
}

// Add records n canonical bytes against today and persists the counter.
func (u *UsageTracker) Add(n int64, now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if u.Day != day {
		u.Day, u.Bytes = day, 0
	}
	u.Bytes += n
	if blob, err := json.Marshal(u); err == nil {
		if err := u.fs.MkdirAll(filepath.Dir(u.path), config.DirPerm); err == nil {
			if f, err := u.fs.OpenFile(u.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, config.FilePerm); err == nil {
				f.Write(blob)
				f.Close()
			}
		}
	}
}
