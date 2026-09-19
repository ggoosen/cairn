package daemon

// D5 — bulk corpus export, the one primitive the adopt-standalone procedure
// needs and nothing in the CLI provided.
//
// R34 rules that a standalone mesh's origin logs CANNOT be merged into
// another mesh: a different genesis and root is a different trust domain by
// design, and adoption means RE-PUBLISHING the knowledge with provenance,
// then retiring the old mesh. Re-publishing needs the corpus as ordinary
// files, because `cairn ingest scan/apply` — the existing, idempotent,
// provenance-carrying import path — consumes a directory of Markdown.
//
// Nothing else in the daemon enumerates live messages: `search` needs a
// query, `map`/`compact` render statistics, and `export` is per-message and
// emits round-trip front matter that would become body text in another mesh.
// Hence this: every live message's HEAD BODY, verbatim, under a directory
// tree mirroring its topics, with the message id as the filename.
//
// Deliberate properties:
//
//   - It writes INSIDE the portable dir (exports/corpus-<timestamp>/), never
//     to a caller-supplied path. The daemon does not gain the ability to
//     write anywhere on the operator's disk for a rare batch job.
//   - Bodies are VERBATIM. The content hash is what makes re-ingest a no-op,
//     so a single added byte of metadata would break idempotence and change
//     what the adopting mesh stores.
//   - Retracted messages are NOT exported. A retraction is a decision, and
//     carrying it into the new mesh as live content would silently undo it.
//   - A message with several topics is written ONCE, under the first by
//     sort order, and the manifest records every topic it had. Ingest derives
//     topics from the path, so secondary links cannot survive the round trip;
//     saying so in the manifest beats losing it silently.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// CorpusEntry is one exported message, as recorded in the manifest.
type CorpusEntry struct {
	MessageID string   `json:"message_id"` // the ORIGIN mesh's id, for provenance
	Path      string   `json:"path"`       // relative to the export root
	BodyHash  string   `json:"body_hash"`
	Sender    string   `json:"sender,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	TextClass string   `json:"text_class,omitempty"`
	Topics    []string `json:"topics,omitempty"` // ALL topics it had
	ThreadID  string   `json:"thread_id,omitempty"`
}

// CorpusExport is the manifest written beside the exported tree. It is the
// operator's record of what left the standalone mesh, and the test's oracle
// for "no event loss from either side".
type CorpusExport struct {
	CairnID    string `json:"cairn_id"` // the SOURCE mesh
	DeviceID   string `json:"device_id"`
	ExportedAt string `json:"exported_at"`
	Root       string `json:"root"`
	Messages   int    `json:"messages"`
	// Skipped counts live messages whose body could not be read — an expired
	// ephemeral or a missing object. Counted and named rather than silently
	// dropped: an adoption that quietly loses content is the failure this
	// whole procedure exists to avoid.
	Skipped []string      `json:"skipped_message_ids,omitempty"`
	Entries []CorpusEntry `json:"entries"`
}

// CorpusManifestName is the manifest's filename inside the export root. It is
// not Markdown, so `cairn ingest scan` ignores it.
const CorpusManifestName = "cairn-corpus-export.json"

// ExportCorpus writes every live message's head body to
// exports/corpus-<timestamp>/<topic-path>/<message-id>.md and returns the
// export root. Deterministic for a fixed corpus and timestamp.
func (d *Daemon) ExportCorpus() (string, *CorpusExport, error) {
	ids, err := d.proj.DigestCandidates(nil) // every non-retracted message
	if err != nil {
		return "", nil, err
	}
	meta, err := d.proj.ResultMeta(ids)
	if err != nil {
		return "", nil, err
	}
	now := d.now().UTC()
	// Each export gets its OWN tree. The timestamp is only second-resolution,
	// and two exports in the same second must not be merged into one
	// directory: stored objects are immutable and WriteFileAtomic refuses to
	// overwrite, so a collision used to fail the whole export halfway through.
	// (Found by the D5 live rehearsal re-running the procedure immediately —
	// which is exactly what an operator unsure it worked would do.)
	base := filepath.Join(d.dir, config.ExportsDirName,
		"corpus-"+now.Format("20060102T150405Z"))
	root := base
	for n := 2; ; n++ {
		if _, err := d.fs.Stat(root); err != nil {
			break
		}
		if n > config.CorpusExportMaxCollisions {
			return "", nil, fmt.Errorf("too many corpus exports in one second under %s", base)
		}
		root = fmt.Sprintf("%s-%d", base, n)
	}
	if err := d.fs.MkdirAll(root, config.DirPerm); err != nil {
		return "", nil, err
	}

	man := &CorpusExport{
		DeviceID:   d.originDeviceID(),
		ExportedAt: now.Format(config.WallTimeFormat),
		Root:       root,
	}
	if d.loaded != nil {
		man.CairnID = d.loaded.Portable.CairnID
	}

	sorted := append([]string(nil), ids...)
	sort.Strings(sorted) // stable output regardless of ranking order
	for _, id := range sorted {
		info, ierr := d.proj.MessageInfo(id)
		if ierr != nil || info.Retracted {
			man.Skipped = append(man.Skipped, id)
			continue
		}
		body, berr := d.store.Get(info.BodyHash)
		if berr != nil {
			// expired ephemeral or absent object: there is nothing to adopt
			man.Skipped = append(man.Skipped, id)
			continue
		}
		m := meta[id]
		topics := append([]string(nil), m.Topics...)
		sort.Strings(topics)
		rel := filepath.Join(corpusDir(topics), id+".md")
		abs := filepath.Join(root, rel)
		if err := d.fs.MkdirAll(filepath.Dir(abs), config.DirPerm); err != nil {
			return "", nil, err
		}
		if err := fsx.WriteFileAtomic(d.fs, abs, body, config.FilePerm); err != nil {
			return "", nil, err
		}
		man.Entries = append(man.Entries, CorpusEntry{
			MessageID: id, Path: filepath.ToSlash(rel), BodyHash: info.BodyHash,
			Sender: m.Sender, CreatedAt: info.CreatedAt, TextClass: info.TextClass,
			Topics: topics, ThreadID: m.ThreadID,
		})
	}
	man.Messages = len(man.Entries)

	blob, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return "", nil, err
	}
	if err := fsx.WriteFileAtomic(d.fs, filepath.Join(root, CorpusManifestName),
		append(blob, '\n'), config.FilePerm); err != nil {
		return "", nil, err
	}
	return root, man, nil
}

// corpusUntopiced is where messages with no topic land. `cairn ingest scan`
// turns the directory name into a topic under the repo label, so this reads
// as "<repo>/untopiced" in the adopting mesh — visible, not hidden.
const corpusUntopiced = "untopiced"

// corpusDir picks the directory for one message: its first topic by sort
// order, or corpusUntopiced. Topic names are already restricted to the topic
// charset (lowercase, digits, `/`, `_`, `-`), so they are safe path
// components — but a defensive check refuses anything that could climb out
// of the export root rather than trusting that invariant from a foreign log.
func corpusDir(topics []string) string {
	for _, t := range topics {
		if t == "" || strings.Contains(t, "..") || strings.HasPrefix(t, "/") ||
			filepath.IsAbs(t) || strings.ContainsAny(t, `\:`) {
			continue
		}
		return filepath.FromSlash(t)
	}
	return corpusUntopiced
}

// originDeviceID reports this node's own origin device, for the manifest.
func (d *Daemon) originDeviceID() string {
	if d.loaded == nil || d.loaded.Device == nil {
		return ""
	}
	return d.loaded.Device.DeviceID
}

// CorpusRepoLabel is the default ingest `--repo` label for an adopted mesh:
// stable, derived from the SOURCE mesh id, and visible in every resulting
// topic and source_ref so an adopted message never passes for native.
func CorpusRepoLabel(cairnID string) string {
	id := strings.ReplaceAll(cairnID, "-", "")
	if len(id) > 12 {
		id = id[:12]
	}
	if id == "" {
		return "standalone"
	}
	return fmt.Sprintf("standalone-%s", id)
}
