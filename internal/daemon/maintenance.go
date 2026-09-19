package daemon

// P2-1: the async maintenance worker's live wiring. The pure ladder policy is
// internal/maintenance; here the daemon samples the debt signals (embedding
// backlog + disk free space) each enricher pass, records the degradation Level,
// and gates the enrichment rungs by it (spec §8.2). send() never consults this
// — it always accepts; only DERIVED work is shed.

import (
	"fmt"
	"syscall"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/maintenance"
)

// assessDegradation samples the live debt and stores the current ladder Level.
// Returns the level for the caller (the enricher loop) to gate on.
func (d *Daemon) assessDegradation() maintenance.Level {
	debt := maintenance.Debt{}
	// D10 — RULING-NEEDED (PROGRESS.md "Author rulings needed — D10"): a
	// backlog nothing can ever work off is not debt. With no embedder
	// configured, CountPendingEmbeddings is monotonic in CORPUS SIZE, not in
	// load: an idle laptop with no venv sat at rung 2 (delay-summaries) with
	// 1,242 unembeddable revisions and wrote 5 summaries for 1,242 messages.
	// Rungs 3–4 are harmless no-ops in that state (you cannot delay embeddings
	// that never run, and lexical-only is already true), but rungs 1–2 shed
	// real, ACHIEVABLE work forever, and the ladder exists to shed derived work
	// so the system can CATCH UP — where catching up is impossible, shedding
	// is pure loss with no recovery. Zeroing the axis is read as the ladder's
	// intent (spec §8.2) rather than a change to it; marked for confirmation.
	// The disk axis (rungs 5–7) is a separate signal and is untouched.
	embedderPresent := d.emb() != nil
	if embedderPresent {
		if pending, err := d.proj.CountPendingEmbeddings(); err == nil {
			debt.PendingEmbeddings = pending
		}
	}
	if free, ok := freeDiskBytes(d.dir); ok {
		debt.DiskPressure = free < config.LadderDiskPressureFreeBytes
		debt.DiskCritical = free < config.LadderDiskCriticalFreeBytes
		debt.QuotaExhausted = free < config.LadderDiskQuotaFreeBytes
	}
	d.mu.Lock()
	th := d.ladderThresholds
	d.mu.Unlock()
	lvl := maintenance.Assess(debt, th)
	d.mu.Lock()
	prev := d.degradeLevel
	d.degradeLevel = lvl
	d.mu.Unlock()
	if lvl != prev {
		// R45-style: a state change in a core subsystem is announced, not silent.
		backlog := fmt.Sprintf("pending embeddings %d", debt.PendingEmbeddings)
		if !embedderPresent {
			backlog = "no embedder configured — backlog axis disengaged"
		}
		fmt.Fprintf(d.warn, "maintenance: degradation level %s → %s (%s)\n", prev, lvl, backlog)
	}
	return lvl
}

// DegradationLevel reports the current ladder level (for `cairn status`).
func (d *Daemon) DegradationLevel() maintenance.Level {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.degradeLevel
}

// AssessDegradationForTest samples debt and updates the level synchronously
// (the enricher loop does this on its own cadence in production).
func (d *Daemon) AssessDegradationForTest() maintenance.Level { return d.assessDegradation() }

// SetLadderThresholdsForTest lowers the §8.2 ladder thresholds so a handful of
// unembedded revisions trips a rung deterministically.
func (d *Daemon) SetLadderThresholdsForTest(t maintenance.Thresholds) {
	d.mu.Lock()
	d.ladderThresholds = t
	d.mu.Unlock()
}

// SetDegradeLevelForTest pins the cached ladder level (the disk rungs 5–7 are
// otherwise driven by real free-space, which tests cannot inject).
func (d *Daemon) SetDegradeLevelForTest(lvl maintenance.Level) {
	d.mu.Lock()
	d.degradeLevel = lvl
	d.mu.Unlock()
}

// freeDiskBytes returns the free bytes on the filesystem holding dir. ok=false
// if the platform stat fails (disk rungs then stay disengaged — the backlog
// axis still governs). syscall.Statfs is available on macOS + Linux (the
// supported platforms); Bsize's type differs per-OS, so widen to uint64.
func freeDiskBytes(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
