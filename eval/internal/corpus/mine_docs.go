package corpus

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Documentation cross-reference mining (BUILD-PLAN §5-E3).
//
// When a technical writer links "see the retry policy" to retries.md, they
// have asserted that retries.md answers "retry policy". It is a weaker
// judgment than a duplicate marker — a link says relevant, not *most*
// relevant — and it is labelled as its own kind so a reader can weight it
// accordingly, or exclude it entirely.
//
// Acquisition is a local git clone, which keeps this miner offline and
// testable: the operator clones a documentation repository and points the
// tool at the checkout. Nothing here touches the network.
//
// MODELLING CHOICES:
//   - One markdown file is one item; its first H1 (or the filename) is its
//     title.
//   - A link's ANCHOR TEXT is the query, and the linked file is the relevant
//     item. Anchor text is what a person wrote to describe the destination,
//     which is as close to a natural query as documentation gets.
//   - Anchors that describe nothing ("here", "this", "docs", "README") are
//     dropped, along with anything shorter than two words. They are links,
//     not descriptions, and would inject noise a human labeller never
//     intended.
//   - Self-links are dropped.

var (
	mdLinkRE = regexp.MustCompile(`\[([^\]\n]{1,200})\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	mdH1RE   = regexp.MustCompile(`(?m)^#\s+(.+)$`)
)

// uselessAnchors are link texts that describe the destination not at all.
var uselessAnchors = map[string]bool{
	"here": true, "this": true, "that": true, "link": true, "docs": true,
	"documentation": true, "readme": true, "above": true, "below": true,
	"see": true, "more": true, "click here": true, "read more": true,
	"see here": true, "this page": true, "this document": true, "index": true,
}

// DocsMineOptions tunes documentation mining.
type DocsMineOptions struct {
	// Root is the documentation checkout.
	Root string
	// Origin identifies the source for the manifest (repo URL, say).
	Origin string
	// MinAnchorWords is the shortest anchor text accepted as a query.
	MinAnchorWords int
}

// MineDocs walks a markdown tree and derives items and cross-reference
// queries. Pure apart from reading the filesystem it was pointed at.
func MineDocs(opts DocsMineOptions) ([]Item, []Query, []string, error) {
	if opts.Root == "" {
		return nil, nil, nil, fmt.Errorf("docs root is required")
	}
	minWords := opts.MinAnchorWords
	if minWords <= 0 {
		minWords = 2
	}

	bodies := map[string]string{} // rel path → contents
	err := filepath.WalkDir(opts.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(p)); ext != ".md" && ext != ".markdown" {
			return nil
		}
		rel, err := filepath.Rel(opts.Root, p)
		if err != nil {
			return err
		}
		blob, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		bodies[filepath.ToSlash(rel)] = string(blob)
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}

	itemID := func(rel string) string {
		return "doc-" + strings.TrimSuffix(strings.ReplaceAll(rel, "/", "-"), filepath.Ext(rel))
	}

	var items []Item
	for rel, body := range bodies {
		title := filepath.Base(rel)
		if m := mdH1RE.FindStringSubmatch(body); m != nil {
			title = strings.TrimSpace(m[1])
		}
		items = append(items, Item{
			ID:     itemID(rel),
			Title:  title,
			Body:   body,
			Topics: []string{"corpus/docs"},
			URL:    rel,
		})
	}

	var queries []Query
	dropped := 0
	seen := map[string]bool{}
	for rel, body := range bodies {
		for _, m := range mdLinkRE.FindAllStringSubmatch(body, -1) {
			anchor, target := strings.TrimSpace(m[1]), m[2]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "#") {
				continue // external or in-page: no cross-document judgment
			}
			targetRel := filepath.ToSlash(path.Clean(path.Join(path.Dir(rel), stripFragment(target))))
			if _, ok := bodies[targetRel]; !ok || targetRel == rel {
				continue
			}
			if !usefulAnchor(anchor, minWords) {
				dropped++
				continue
			}
			id := fmt.Sprintf("q-%s-%s", itemID(rel), slug(anchor))
			if seen[id] {
				continue // the same phrase linked twice is one judgment
			}
			seen[id] = true
			queries = append(queries, Query{
				ID:        id,
				Query:     anchor,
				Relevant:  []string{itemID(targetRel)},
				LabelKind: LabelDocCrossReference,
				LabelURL:  rel,
			})
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	sort.Slice(queries, func(i, j int) bool { return queries[i].ID < queries[j].ID })

	notes := []string{
		"query = a link's anchor text; relevant = the linked document. A link asserts relevance, NOT best-answer — a weaker judgment than a duplicate marker, and labelled separately so it can be weighted or excluded",
		fmt.Sprintf("%d links were dropped for uninformative anchor text (\"here\", \"this\", single words)", dropped),
	}
	return items, queries, notes, nil
}

func usefulAnchor(anchor string, minWords int) bool {
	lower := strings.ToLower(strings.TrimSpace(anchor))
	if uselessAnchors[lower] {
		return false
	}
	if len(strings.Fields(lower)) < minWords {
		return false
	}
	// A bare filename is a path, not a description.
	return !strings.Contains(lower, ".md")
}

func stripFragment(target string) string {
	if i := strings.IndexByte(target, '#'); i >= 0 {
		return target[:i]
	}
	return target
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(truncate(b.String(), 48), "-")
}
