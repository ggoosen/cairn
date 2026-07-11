// Package ingest implements `cairn ingest` (BUILD-PLAN M9): the
// scan → manifest → apply pipeline for importing existing knowledge bases
// (llm-wiki style repos, docs trees) into a LIVE cairn. It uses the P0
// hooks: source_ref/relates_to on message.publish and the source_refs
// projection table. All mutations go through the daemon (rulings §6);
// ingest can never use the operator class override (text-class policy
// applies as for any sender).
package ingest

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/merge"
	"github.com/ggoosen/cairn/internal/object"
)

// Caller sends one IPC request to the daemon (injectable for tests).
type Caller func(daemon.Request) (*daemon.Response, error)

// Action classifies one scanned file.
type Action string

const (
	ActionPublish Action = "publish" // never imported before
	ActionRevise  Action = "revise"  // imported, content changed
	ActionSkip    Action = "skip"    // imported, content identical
)

// Entry is one file in the manifest.
type Entry struct {
	Path        string   `json:"path"` // repo-relative, "/"-separated
	ContentHash string   `json:"content_hash"`
	Action      Action   `json:"action"`
	MessageID   string   `json:"message_id"` // existing, or pre-assigned for publishes
	Topics      []string `json:"topics,omitempty"`
	RelatesTo   []string `json:"relates_to,omitempty"` // message_ids ([[wiki links]] resolved)
	Unresolved  []string `json:"unresolved_links,omitempty"`
}

// Manifest is the reviewable plan `cairn ingest scan` produces and
// `cairn ingest apply` executes. Deterministic for identical inputs.
type Manifest struct {
	Repo      string  `json:"repo"`
	Root      string  `json:"root"`
	ScannedAt string  `json:"scanned_at"`
	TextClass string  `json:"text_class"`
	Entries   []Entry `json:"entries"`
}

var wikiLink = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|[^\[\]]*)?\]\]`)

// Scan walks root for .md files, classifies each against the live cairn
// (source_refs + head body hash), proposes topics from directory structure,
// and resolves [[wiki links]] to message IDs (pre-assigning IDs for files
// that will be newly published, so cross-links among new files work).
func Scan(call Caller, root, repo, textClass string, newID func() string, now time.Time) (*Manifest, error) {
	if repo == "" {
		repo = filepath.Base(root)
	}
	if textClass == "" {
		textClass = object.ClassCanonical
	}
	m := &Manifest{
		Repo:      repo,
		Root:      root,
		ScannedAt: now.UTC().Format(config.WallTimeFormat),
		TextClass: textClass,
	}

	type fileInfo struct {
		relPath string
		body    []byte
		hash    string
	}
	var files []fileInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		body, err := merge.NormalizeLF(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, fileInfo{
			relPath: filepath.ToSlash(rel),
			body:    body,
			hash:    object.Hash(body),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(a, b int) bool { return files[a].relPath < files[b].relPath })

	// pass 1: classify + assign message IDs; build the wiki-name index
	nameToPath := map[string]string{}
	pathToMsg := map[string]string{}
	entries := make([]Entry, 0, len(files))
	for _, f := range files {
		key := repo + "/" + f.relPath
		resp, err := call(daemon.Request{Op: "source-ref", SourcePath: key})
		if err != nil {
			return nil, fmt.Errorf("source-ref lookup %s: %w", key, err)
		}
		e := Entry{Path: f.relPath, ContentHash: f.hash}
		if resp.MessageID2 == "" {
			e.Action = ActionPublish
			e.MessageID = newID() // pre-assigned: cross-links to new files resolve
		} else {
			e.MessageID = resp.MessageID2
			peek, err := call(daemon.Request{Op: "peek", MessageID: e.MessageID})
			if err != nil {
				return nil, fmt.Errorf("peek %s: %w", e.MessageID, err)
			}
			if peek.Message.BodyHash == f.hash {
				e.Action = ActionSkip
			} else {
				e.Action = ActionRevise
			}
		}
		e.Topics = proposeTopics(repo, f.relPath)
		entries = append(entries, e)
		nameToPath[normalizeWikiName(strings.TrimSuffix(filepath.Base(f.relPath), ".md"))] = f.relPath
		pathToMsg[f.relPath] = e.MessageID
	}

	// pass 2: resolve wiki links per file
	for i, f := range files {
		seen := map[string]bool{}
		for _, match := range wikiLink.FindAllStringSubmatch(string(f.body), -1) {
			target := normalizeWikiName(strings.TrimSpace(match[1]))
			if p, ok := nameToPath[target]; ok && p != f.relPath {
				id := pathToMsg[p]
				if !seen[id] {
					entries[i].RelatesTo = append(entries[i].RelatesTo, id)
					seen[id] = true
				}
			} else if !seen["?"+target] {
				entries[i].Unresolved = append(entries[i].Unresolved, match[1])
				seen["?"+target] = true
			}
		}
		sort.Strings(entries[i].RelatesTo)
	}
	m.Entries = entries
	return m, nil
}

// proposeTopics: the repo label plus each ancestor directory as a
// topic path (schema pattern ^[a-z0-9][a-z0-9/_-]*$).
func proposeTopics(repo, relPath string) []string {
	topics := []string{sanitizeTopic(repo)}
	dir := filepath.ToSlash(filepath.Dir(relPath))
	if dir != "." {
		parts := strings.Split(dir, "/")
		for i := range parts {
			topics = append(topics, sanitizeTopic(repo+"/"+strings.Join(parts[:i+1], "/")))
		}
	}
	return dedupe(topics)
}

var topicClean = regexp.MustCompile(`[^a-z0-9/_-]+`)

func sanitizeTopic(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, " ", "-"))
	s = topicClean.ReplaceAllString(s, "-")
	s = strings.Trim(s, "/-_")
	if s == "" || !(s[0] >= 'a' && s[0] <= 'z' || s[0] >= '0' && s[0] <= '9') {
		s = "x" + s
	}
	return s
}

func normalizeWikiName(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "-"))
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Report summarizes an Apply run.
type Report struct {
	Published  int `json:"published"`
	Revised    int `json:"revised"`
	Skipped    int `json:"skipped"`
	Topics     int `json:"topics_created"`
	Links      int `json:"links_added"`
	Downgraded int `json:"downgraded"`
}

// Apply executes a manifest against the live daemon. Idempotent: entries
// whose source files changed since the scan are re-verified and refused
// (rescan); skips are skipped; publishes carry source_ref + relates_to;
// revisions go through the daemon's revise path.
func Apply(call Caller, m *Manifest, now time.Time) (*Report, error) {
	rep := &Report{}

	topicIDs := map[string]string{}
	ensureTopic := func(name string) (string, error) {
		if id, ok := topicIDs[name]; ok {
			return id, nil
		}
		resp, err := call(daemon.Request{Op: "topic-ensure", TopicName: name})
		if err != nil {
			return "", err
		}
		topicIDs[name] = resp.TopicID
		return resp.TopicID, nil
	}

	for _, e := range m.Entries {
		abs := filepath.Join(m.Root, filepath.FromSlash(e.Path))
		raw, err := os.ReadFile(abs)
		if err != nil {
			return rep, fmt.Errorf("%s: source file vanished since scan: %w", e.Path, err)
		}
		body, err := merge.NormalizeLF(raw)
		if err != nil {
			return rep, err
		}
		if object.Hash(body) != e.ContentHash {
			return rep, fmt.Errorf("%s changed since the scan — rerun `cairn ingest scan`", e.Path)
		}

		switch e.Action {
		case ActionSkip:
			rep.Skipped++
			continue

		case ActionPublish:
			var ids []string
			for _, t := range e.Topics {
				id, err := ensureTopic(t)
				if err != nil {
					return rep, err
				}
				ids = append(ids, id)
			}
			resp, err := call(daemon.Request{Op: "publish", Publish: &daemon.PublishRequest{
				Actor:     "ingest",
				Body:      string(body),
				TextClass: m.TextClass,
				MessageID: e.MessageID,
				TopicIDs:  ids,
				SourceRef: &daemon.SourceRef{
					Path:        m.Repo + "/" + e.Path,
					Repo:        m.Repo,
					ContentHash: e.ContentHash,
					ImportedAt:  now.UTC().Format(config.WallTimeFormat),
				},
				RelatesTo: e.RelatesTo,
			}})
			if err != nil {
				return rep, fmt.Errorf("publish %s: %w", e.Path, err)
			}
			rep.Published++
			rep.Links += len(ids)
			rep.Topics = len(topicIDs)
			if resp.Publish.Downgraded {
				rep.Downgraded++
			}

		case ActionRevise:
			if _, err := call(daemon.Request{Op: "revise", MessageID: e.MessageID, Body: string(body)}); err != nil {
				return rep, fmt.Errorf("revise %s: %w", e.Path, err)
			}
			rep.Revised++

		default:
			return rep, fmt.Errorf("%s: unknown action %q", e.Path, e.Action)
		}
	}
	return rep, nil
}

// WriteManifest / ReadManifest persist the plan between scan and apply.
func WriteManifest(path string, m *Manifest) error {
	blob, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, blob, 0o644)
}

func ReadManifest(path string) (*Manifest, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
