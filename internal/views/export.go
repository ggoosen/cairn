// Package views generates and parses agent-facing files: exports (M5),
// digest / fetched / conflicts (M5/M6). Exports carry READ-ONLY YAML
// front-matter (rulings §8): {message_id, revision_id, base_revision_id,
// body_hash, exported_at}; any mutation of these fields is rejected at
// ingest by validating them against the projection.
package views

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ExportFrontMatter is the strict whitelist (rulings §9: strict YAML).
type ExportFrontMatter struct {
	MessageID      string `yaml:"message_id"`
	RevisionID     string `yaml:"revision_id"`
	BaseRevisionID string `yaml:"base_revision_id"`
	BodyHash       string `yaml:"body_hash"`
	ExportedAt     string `yaml:"exported_at"`
}

// RenderExport produces the export file content: front-matter + body.
func RenderExport(fm ExportFrontMatter, body []byte) ([]byte, error) {
	head, err := yaml.Marshal(fm)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(head)
	b.WriteString("---\n")
	b.Write(body)
	return []byte(b.String()), nil
}

// ParseExport splits an (edited) export into front-matter and body. The
// front-matter must parse strictly — unknown fields are a mutation and are
// rejected here; semantic validation against the projection happens in the
// daemon.
func ParseExport(content []byte) (ExportFrontMatter, []byte, error) {
	var fm ExportFrontMatter
	s := string(content)
	if !strings.HasPrefix(s, "---\n") {
		return fm, nil, errors.New("export front-matter missing (was it deleted?) — front-matter is read-only")
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return fm, nil, errors.New("export front-matter unterminated — front-matter is read-only")
	}
	dec := yaml.NewDecoder(strings.NewReader(rest[:end]))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil {
		return fm, nil, fmt.Errorf("export front-matter unparseable or mutated (%v) — front-matter is read-only", err)
	}
	if fm.MessageID == "" || fm.BaseRevisionID == "" || fm.BodyHash == "" {
		return fm, nil, errors.New("export front-matter incomplete — front-matter is read-only")
	}
	body := rest[end+5:]
	return fm, []byte(body), nil
}
