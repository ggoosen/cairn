package daemon

// P2-6: compaction views (spec §12; §7 "control state … compacted to current
// state"). A condensed snapshot of the corpus reduced to what is TRUE NOW —
// live head revisions, active links/pins/subscriptions — with a headline of how
// much event history collapsed away. Local derived state, regenerated on demand.
// Structural metadata only (no bodies), so the §7.3 quote sentinel is trivial.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/projection"
)

// CompactionResult reports where the view was written and the headline ratio.
type CompactionResult struct {
	Path         string  `json:"path"`
	Events       int     `json:"events"`
	LiveEntities int     `json:"live_entities"`
	Ratio        string  `json:"ratio"` // decimal string (events / live entities)
	RatioF       float64 `json:"-"`
}

// GenerateCompaction renders views/<agent>/compaction.md from the projection.
func (d *Daemon) GenerateCompaction(agentView string) (*CompactionResult, error) {
	if err := d.writable(); err != nil {
		return nil, err
	}
	if agentView == "" {
		agentView = "operator"
	}
	c, err := d.proj.Compaction()
	if err != nil {
		return nil, err
	}
	live := c.LiveMessages + c.ActiveTopicLinks + c.ActivePins + c.ActiveSubscriptions
	ratio := 0.0
	if live > 0 {
		ratio = float64(c.TotalEvents) / float64(live)
	}
	body := renderCompaction(c, live, ratio, d.now().UTC().Format(config.WallTimeFormat))

	dir := filepath.Join(d.dir, config.ViewsDirName, agentView)
	if err := d.fs.MkdirAll(dir, config.DirPerm); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, config.CompactionFileName)
	d.fs.Remove(path)
	if err := fsx.WriteFileAtomic(d.fs, path, []byte(body), config.FilePerm); err != nil {
		return nil, err
	}
	return &CompactionResult{Path: path, Events: c.TotalEvents, LiveEntities: live,
		Ratio: fmt.Sprintf("%.2f", ratio), RatioF: ratio}, nil
}

// renderCompaction builds the compacted current-state markdown (pure).
func renderCompaction(c projection.CompactionStats, live int, ratio float64, generatedAt string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# cairn compaction view\n\n")
	fmt.Fprintf(&b, "_generated %s (current effective state; regenerated)_\n\n", generatedAt)
	fmt.Fprintf(&b, "## headline\n\n")
	fmt.Fprintf(&b, "- %d events → %d live entities (compaction ratio %.2f×)\n\n", c.TotalEvents, live, ratio)
	fmt.Fprintf(&b, "## current state\n\n")
	fmt.Fprintf(&b, "- live messages: %d\n", c.LiveMessages)
	fmt.Fprintf(&b, "- active topic links: %d\n", c.ActiveTopicLinks)
	fmt.Fprintf(&b, "- active pins: %d\n", c.ActivePins)
	fmt.Fprintf(&b, "- active subscriptions: %d\n\n", c.ActiveSubscriptions)
	fmt.Fprintf(&b, "## compacted away\n\n")
	fmt.Fprintf(&b, "- superseded revisions: %d\n", c.SupersededRevisions)
	fmt.Fprintf(&b, "- retracted messages: %d\n", c.RetractedMessages)
	fmt.Fprintf(&b, "- removed topic links: %d\n", c.RemovedTopicLinks)
	fmt.Fprintf(&b, "- total revisions on record: %d\n", c.TotalRevisions)
	return b.String()
}
