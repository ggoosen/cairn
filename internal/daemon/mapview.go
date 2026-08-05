package daemon

// P2-5: local maps + rollups (spec §7.3 map.md, §12). A per-agent navigation
// view rendered from the projection — a rollup of topics, threads, and pins
// over the corpus. Local derived state, regenerated on demand (and on the
// maintenance cadence).
//
// This is the STRUCTURAL v1 map (P2H6, Option A): deterministic topic/thread/pin
// rollups, embedding-free. The embedding-clustered SEMANTIC map (self-organizing
// neighborhoods over content vectors) is deferred to P4, where it can be trained
// on real P2 usage/salience data — don't build the self-folding map on zero data.
//
// The only free-form, author-controlled field this view renders is the topic
// NAME (thread roots are UUIDs, everything else is integer counts). Topic names
// are untrusted, peer-authorable data (R53), so they are BOTH validated at every
// write/ingest boundary (see validateTopicName / projection topic.create) AND
// escaped here at render — sanitizeMapField neutralizes any embedded "##",
// backtick, or newline so a historical or boundary-slipped bad name renders as
// inert text in its cell, never as a heading or instruction.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/projection"
)

// mapFieldUnsafe matches any rune outside the schema topic-name charset
// (^[a-z0-9][a-z0-9/_-]*$). A conforming name contains none, so it passes
// through unchanged; an out-of-charset name — including one durable in the log
// before the write-boundary validation existed — has every offending rune
// (newline, '#', backtick, space, uppercase, …) collapsed to '_', rendering it
// inert and single-line inside its list cell (R53 render leg).
var mapFieldUnsafe = regexp.MustCompile(`[^a-z0-9/_-]`)

func sanitizeMapField(s string) string {
	return mapFieldUnsafe.ReplaceAllString(s, "_")
}

// MapResult reports where the map was written and its rollup headline.
type MapResult struct {
	Path          string `json:"path"`
	TotalMessages int    `json:"total_messages"`
	TotalTopics   int    `json:"total_topics"`
	PinnedObjects int    `json:"pinned_objects"`
}

// GenerateMap renders views/<agent>/map.md from the current projection.
func (d *Daemon) GenerateMap(agentView string) (*MapResult, error) {
	if err := d.writable(); err != nil {
		return nil, err
	}
	if agentView == "" {
		agentView = "operator"
	}
	nav, err := d.proj.NavMap(config.MapTopThreads)
	if err != nil {
		return nil, err
	}
	body := renderMap(nav, d.now().UTC().Format(config.WallTimeFormat))

	dir, err := d.viewDir(agentView)
	if err != nil {
		return nil, err
	}
	if err := d.fs.MkdirAll(dir, config.DirPerm); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, config.MapFileName)
	d.fs.Remove(path) // regenerated view: overwrite-by-remove then atomic write
	if err := fsx.WriteFileAtomic(d.fs, path, []byte(body), config.FilePerm); err != nil {
		return nil, err
	}
	return &MapResult{
		Path: path, TotalMessages: nav.TotalMessages,
		TotalTopics: nav.TotalTopics, PinnedObjects: nav.PinnedObjects,
	}, nil
}

// renderMap builds the markdown navigation view (pure — unit-tested).
func renderMap(nav projection.NavMap, generatedAt string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# cairn map\n\n")
	fmt.Fprintf(&b, "_generated %s (local navigation view; regenerated)_\n\n", generatedAt)
	fmt.Fprintf(&b, "## rollup\n\n")
	fmt.Fprintf(&b, "- messages: %d\n- topics: %d\n- pinned objects: %d\n\n",
		nav.TotalMessages, nav.TotalTopics, nav.PinnedObjects)

	fmt.Fprintf(&b, "## topics\n\n")
	if len(nav.Topics) == 0 {
		fmt.Fprintf(&b, "_none_\n\n")
	}
	for _, t := range nav.Topics {
		fmt.Fprintf(&b, "- %s — %d message(s)\n", sanitizeMapField(t.Name), t.Count)
	}
	if len(nav.Topics) > 0 {
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "## top threads\n\n")
	if len(nav.Threads) == 0 {
		fmt.Fprintf(&b, "_none_\n")
	}
	for _, th := range nav.Threads {
		fmt.Fprintf(&b, "- thread %s — %d repl%s\n", th.RootID, th.ReplyCount, plural(th.ReplyCount))
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
