// Package corpus is the versioned, checksummed corpus format and its loader.
//
// EVAL-PLAN §5-E3 exists to break the circularity described in §2.2: the
// existing golden corpus was written, queried and judged by the same person
// whose project it evaluates, so it validates configuration, not capability.
// The fix is to MINE ground truth that already exists — a maintainer marking
// issue #B a duplicate of #A is a relevance judgment made by someone with no
// stake in Cairn, and there are millions of them.
//
// Two properties of the format carry that intent:
//
//   - Every corpus declares WHERE its labels came from and whether it is
//     `independent` (neither authored nor judged by this project). A corpus
//     that cannot answer that question is not evidence, and the field makes
//     the answer impossible to omit.
//   - Every corpus is checksummed and versioned, and the loader REFUSES a
//     corpus whose bytes no longer match its manifest. A result whose corpus
//     drifted is not reproducible, and silent drift is worse than a hard
//     failure.
package corpus

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// File names inside a corpus directory.
const (
	ManifestFile = "manifest.json"
	ItemsFile    = "items.jsonl"
	QueriesFile  = "queries.jsonl"
)

// Split names. EVAL-PLAN §8 forbids tuning on the evaluation set: weights are
// calibrated on dev only, and holdout answers the claim.
const (
	SplitDev     = "dev"
	SplitHoldout = "holdout"
)

// Label kinds — the KIND of human judgment a query's relevance came from.
// Recorded per query, not per corpus, because a corpus may legitimately mix
// signals of different strength and a reader must be able to separate them.
const (
	LabelDuplicateIssueTimeline = "github_marked_as_duplicate"     // maintainer used the duplicate control
	LabelDuplicateIssueText     = "github_duplicate_text_ref"      // "Duplicate of #123" in a closing comment
	LabelStackOverflowDuplicate = "stackoverflow_closed_duplicate" // community closed as duplicate
	LabelDocCrossReference      = "doc_cross_reference"            // an author linked one page from another
	LabelSynthetic              = "SYNTHETIC_authored_by_project"  // plumbing fixtures only — NOT evidence
)

// Manifest describes a corpus and pins its bytes.
type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Version       string `json:"version"`
	CreatedAt     string `json:"created_at"`

	Source Source `json:"source"`
	Labels Labels `json:"labels"`

	Counts Counts    `json:"counts"`
	Files  []FileSum `json:"files"`
	// Checksum is the corpus identity: sha256 over the per-file digests. It
	// is what a run record cites, so "which corpus produced this number" has
	// exactly one answer.
	Checksum string   `json:"checksum"`
	Notes    []string `json:"notes,omitempty"`
}

// Source records where the material came from and how to get it again.
type Source struct {
	Kind string `json:"kind"` // github-duplicate-issues | stackoverflow-duplicates | doc-cross-references | synthetic
	// Origin identifies the specific body of material (a repo, a site, a
	// documentation checkout).
	Origin string `json:"origin"`
	// Command is the exact invocation that produced this corpus. E8 publishes
	// corpora for third-party replication; a corpus nobody can regenerate is
	// a snapshot, not an artifact.
	Command string `json:"command,omitempty"`
	// License of the source material. Mined corpora carry other people's
	// content and their terms travel with it.
	License   string `json:"license,omitempty"`
	Retrieved string `json:"retrieved_at,omitempty"`
}

// Labels records the provenance of the RELEVANCE JUDGMENTS, which is the
// whole point of E3 and the field a sceptical reader should check first.
type Labels struct {
	// Provenance in prose: who made these judgments, and how were they mined?
	Provenance string `json:"provenance"`
	// Independent is true only when neither the content nor the judgments
	// were produced by this project. A false value does not make a corpus
	// useless — it makes it a regression gate rather than evidence.
	Independent bool `json:"independent"`
	// Kinds counts queries by label kind.
	Kinds map[string]int `json:"kinds,omitempty"`
}

// Counts is a quick shape summary; the loader verifies it against the files.
type Counts struct {
	Items   int `json:"items"`
	Queries int `json:"queries"`
	Dev     int `json:"dev_queries"`
	Holdout int `json:"holdout_queries"`
}

// FileSum pins one file's bytes.
type FileSum struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Item is one retrievable document.
type Item struct {
	ID        string   `json:"id"`
	Title     string   `json:"title,omitempty"`
	Body      string   `json:"body"`
	Topics    []string `json:"topics,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"` // RFC 3339 UTC, as published by the source
	URL       string   `json:"url,omitempty"`
}

// Query is one information need with its human-made relevance judgment.
type Query struct {
	ID    string `json:"id"`
	Query string `json:"query"`
	// Relevant lists item ids a HUMAN judged relevant to this query.
	Relevant []string `json:"relevant"`
	// LabelKind and LabelURL make each judgment auditable on its own: a
	// reader can follow the link and disagree with it.
	LabelKind string `json:"label_kind"`
	LabelURL  string `json:"label_url,omitempty"`
	LabeledAt string `json:"labeled_at,omitempty"`
	Split     string `json:"split,omitempty"`
}

// Corpus is a loaded, verified corpus.
type Corpus struct {
	Dir      string
	Manifest Manifest
	Items    []Item
	Queries  []Query
}

// Load reads a corpus directory and verifies it against its manifest.
func Load(dir string) (*Corpus, error) {
	blob, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if m.SchemaVersion != tunables.CorpusSchemaVersion {
		return nil, fmt.Errorf("corpus %s has schema version %d, this harness reads %d",
			dir, m.SchemaVersion, tunables.CorpusSchemaVersion)
	}
	for _, f := range m.Files {
		sum, size, err := digestFile(filepath.Join(dir, f.Name))
		if err != nil {
			return nil, err
		}
		if sum != f.SHA256 || size != f.Bytes {
			return nil, fmt.Errorf("corpus %s: %s does not match the manifest (have %s/%d bytes, want %s/%d) — a drifted corpus makes every result citing it unreproducible",
				m.ID, f.Name, sum[:12], size, f.SHA256[:12], f.Bytes)
		}
	}
	if want := corpusChecksum(m.Files); want != m.Checksum {
		return nil, fmt.Errorf("corpus %s: checksum %s does not match its own file digests (%s)", m.ID, m.Checksum, want)
	}

	c := &Corpus{Dir: dir, Manifest: m}
	if err := readJSONL(filepath.Join(dir, ItemsFile), func(line []byte) error {
		var it Item
		if err := json.Unmarshal(line, &it); err != nil {
			return err
		}
		c.Items = append(c.Items, it)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := readJSONL(filepath.Join(dir, QueriesFile), func(line []byte) error {
		var q Query
		if err := json.Unmarshal(line, &q); err != nil {
			return err
		}
		c.Queries = append(c.Queries, q)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(c.Items) != m.Counts.Items || len(c.Queries) != m.Counts.Queries {
		return nil, fmt.Errorf("corpus %s: manifest says %d items / %d queries, files hold %d / %d",
			m.ID, m.Counts.Items, m.Counts.Queries, len(c.Items), len(c.Queries))
	}
	return c, c.validate()
}

// validate catches the mistakes that would silently corrupt a measurement:
// a duplicate item id, or a judgment pointing at an item that is not there
// (which would look exactly like a retrieval failure).
func (c *Corpus) validate() error {
	seen := make(map[string]bool, len(c.Items))
	for _, it := range c.Items {
		if it.ID == "" {
			return fmt.Errorf("corpus %s: an item has no id", c.Manifest.ID)
		}
		if seen[it.ID] {
			return fmt.Errorf("corpus %s: duplicate item id %q", c.Manifest.ID, it.ID)
		}
		seen[it.ID] = true
	}
	qids := make(map[string]bool, len(c.Queries))
	for _, q := range c.Queries {
		if q.ID == "" || q.Query == "" {
			return fmt.Errorf("corpus %s: a query is missing id or text", c.Manifest.ID)
		}
		if qids[q.ID] {
			return fmt.Errorf("corpus %s: duplicate query id %q", c.Manifest.ID, q.ID)
		}
		qids[q.ID] = true
		if len(q.Relevant) == 0 {
			return fmt.Errorf("corpus %s: query %q has no relevance judgment", c.Manifest.ID, q.ID)
		}
		if q.LabelKind == "" {
			return fmt.Errorf("corpus %s: query %q does not say where its judgment came from", c.Manifest.ID, q.ID)
		}
		for _, r := range q.Relevant {
			if !seen[r] {
				return fmt.Errorf("corpus %s: query %q judges %q relevant, but no such item exists — that would read as a retrieval failure",
					c.Manifest.ID, q.ID, r)
			}
		}
	}
	return nil
}

// Split returns the queries in one split.
func (c *Corpus) Split(name string) []Query {
	var out []Query
	for _, q := range c.Queries {
		if q.Split == name {
			out = append(out, q)
		}
	}
	return out
}

// parseTime accepts the RFC 3339 stamps the sources publish.
func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// BackendItems converts the corpus to the harness's uniform write unit.
func (c *Corpus) BackendItems() []backend.Item {
	out := make([]backend.Item, 0, len(c.Items))
	for _, it := range c.Items {
		bi := backend.Item{ID: it.ID, Title: it.Title, Body: it.Body, Topics: it.Topics}
		if it.CreatedAt != "" {
			if t, err := parseTime(it.CreatedAt); err == nil {
				bi.CreatedAt = t
			}
		}
		out = append(out, bi)
	}
	return out
}

// Write serializes a corpus, computing every checksum. Items and queries are
// sorted by id first: a corpus whose bytes depend on map iteration order
// would produce a different checksum on every regeneration, and the checksum
// is the thing a result cites.
func Write(dir string, m Manifest, items []Item, queries []Query) (*Manifest, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	sort.Slice(queries, func(i, j int) bool { return queries[i].ID < queries[j].ID })

	if err := writeJSONL(filepath.Join(dir, ItemsFile), len(items), func(i int) any { return items[i] }); err != nil {
		return nil, err
	}
	if err := writeJSONL(filepath.Join(dir, QueriesFile), len(queries), func(i int) any { return queries[i] }); err != nil {
		return nil, err
	}

	m.SchemaVersion = tunables.CorpusSchemaVersion
	m.Counts = Counts{Items: len(items), Queries: len(queries)}
	kinds := map[string]int{}
	for _, q := range queries {
		kinds[q.LabelKind]++
		switch q.Split {
		case SplitDev:
			m.Counts.Dev++
		case SplitHoldout:
			m.Counts.Holdout++
		}
	}
	m.Labels.Kinds = kinds

	m.Files = nil
	for _, name := range []string{ItemsFile, QueriesFile} {
		sum, size, err := digestFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, FileSum{Name: name, SHA256: sum, Bytes: size})
	}
	m.Checksum = corpusChecksum(m.Files)

	blob, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), append(blob, '\n'), 0o600); err != nil {
		return nil, err
	}
	return &m, nil
}

// AssignSplits deterministically partitions queries into dev and holdout by
// hashing the query id with a FIXED salt.
//
// Deterministic on purpose: a split that could be re-rolled is a split that
// can be re-rolled until the holdout flatters the system. Anyone can verify
// this assignment from the query ids alone.
func AssignSplits(queries []Query) {
	for i := range queries {
		sum := sha256.Sum256([]byte(tunables.SplitSalt + queries[i].ID))
		// first two bytes → [0,65535]; below the threshold is holdout
		v := int(sum[0])<<8 | int(sum[1])
		if v < tunables.HoldoutFractionPerMyriad*65536/10000 {
			queries[i].Split = SplitHoldout
		} else {
			queries[i].Split = SplitDev
		}
	}
}

func corpusChecksum(files []FileSum) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s:%s:%d\n", f.Name, f.SHA256, f.Bytes)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func digestFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func readJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), tunables.MaxCorpusLineBytes)
	line := 0
	for sc.Scan() {
		line++
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return fmt.Errorf("%s line %d: %w", filepath.Base(path), line, err)
		}
	}
	return sc.Err()
}

func writeJSONL(path string, n int, at func(int) any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i := 0; i < n; i++ {
		if err := enc.Encode(at(i)); err != nil {
			return err
		}
	}
	return w.Flush()
}
