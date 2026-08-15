package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ggoosen/cairn/eval/internal/corpus"
)

func runCorpus(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cairn-eval corpus <verify|info|mine> …")
	}
	switch args[0] {
	case "verify":
		return corpusVerify(args[1:])
	case "info":
		return corpusInfo(args[1:])
	case "mine":
		return corpusMine(args[1:])
	}
	return fmt.Errorf("unknown corpus command %q (verify|info|mine)", args[0])
}

// corpusVerify is the check a run should make before citing a corpus: the
// bytes still match the manifest, every judgment points at an item that
// exists, and the corpus says whose judgments they are.
func corpusVerify(args []string) error {
	fs := flag.NewFlagSet("corpus verify", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cairn-eval corpus verify <dir>")
	}
	c, err := corpus.Load(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("%s v%s OK — %d items, %d queries (%d dev / %d holdout), checksum %s\n",
		c.Manifest.ID, c.Manifest.Version, c.Manifest.Counts.Items, c.Manifest.Counts.Queries,
		c.Manifest.Counts.Dev, c.Manifest.Counts.Holdout, c.Manifest.Checksum[:16])
	if !c.Manifest.Labels.Independent {
		fmt.Println("NOTE: labels are NOT independent of this project — a regression gate, not evidence.")
	}
	return nil
}

func corpusInfo(args []string) error {
	fs := flag.NewFlagSet("corpus info", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cairn-eval corpus info <dir>")
	}
	c, err := corpus.Load(fs.Arg(0))
	if err != nil {
		return err
	}
	m := c.Manifest
	fmt.Printf("id:          %s v%s\n", m.ID, m.Version)
	fmt.Printf("source:      %s (%s)\n", m.Source.Kind, m.Source.Origin)
	fmt.Printf("acquired:    %s\n", m.Source.Command)
	fmt.Printf("license:     %s\n", m.Source.License)
	fmt.Printf("labels:      %s\n", m.Labels.Provenance)
	fmt.Printf("independent: %v\n", m.Labels.Independent)
	kinds := make([]string, 0, len(m.Labels.Kinds))
	for k := range m.Labels.Kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("  %-34s %d\n", k, m.Labels.Kinds[k])
	}
	fmt.Printf("counts:      %d items, %d queries (%d dev / %d holdout)\n",
		m.Counts.Items, m.Counts.Queries, m.Counts.Dev, m.Counts.Holdout)
	fmt.Printf("checksum:    %s\n", m.Checksum)
	for _, n := range m.Notes {
		fmt.Printf("note:        %s\n", n)
	}
	return nil
}

func corpusMine(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cairn-eval corpus mine <github|stackoverflow|docs> …")
	}
	switch args[0] {
	case "github":
		return mineGitHub(args[1:])
	case "stackoverflow":
		return mineStackOverflow(args[1:])
	case "docs":
		return mineDocs(args[1:])
	}
	return fmt.Errorf("unknown source %q (github|stackoverflow|docs)", args[0])
}

func mineGitHub(args []string) error {
	fs := flag.NewFlagSet("corpus mine github", flag.ContinueOnError)
	repo := fs.String("repo", "", "owner/name of the repository to mine")
	out := fs.String("out", "", "corpus output directory")
	rawDir := fs.String("raw", "", "directory of API responses downloaded by the operator (see corpora/ACQUISITION.md)")
	withBody := fs.Bool("query-with-body", false, "include the duplicate's body in the query text, not just its title")
	version := fs.String("version", "1", "corpus version string")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *out == "" || *rawDir == "" {
		// No fetch path by design: the harness module is network-free, and
		// `gh api --paginate` already does auth, pagination and rate limits
		// better than anything written here would. corpora/ACQUISITION.md
		// has the exact commands.
		return fmt.Errorf("-repo, -raw and -out are required\n" +
			"this normalizes API responses YOU downloaded; there is no fetch path — see corpora/ACQUISITION.md")
	}
	raw, err := corpus.LoadGitHubRaw(*rawDir)
	if err != nil {
		return err
	}

	items, queries, notes, err := corpus.NormalizeGitHub(*raw, corpus.GitHubMineOptions{
		Repo: *repo, QueryWithBody: *withBody,
	})
	if err != nil {
		return err
	}
	corpus.AssignSplits(queries)

	m, err := corpus.Write(*out, corpus.Manifest{
		ID:        "github-duplicates-" + strings.ReplaceAll(*repo, "/", "-"),
		Version:   *version,
		CreatedAt: nowString(),
		Source: corpus.Source{
			Kind:    "github-duplicate-issues",
			Origin:  "https://github.com/" + *repo,
			Command: "gh api --paginate … (corpora/ACQUISITION.md), then: cairn-eval corpus mine github -repo " + *repo + " -raw <dir>",
			License: "issue text belongs to its authors; check the repository's terms before redistributing",
		},
		Labels: corpus.Labels{
			Provenance:  "repository maintainers marking one issue a duplicate of another, in the course of their own work",
			Independent: true,
		},
		Notes: notes,
	}, items, queries)
	if err != nil {
		return err
	}
	reportCorpus(m)
	return nil
}

func mineStackOverflow(args []string) error {
	fs := flag.NewFlagSet("corpus mine stackoverflow", flag.ContinueOnError)
	out := fs.String("out", "", "corpus output directory")
	version := fs.String("version", "1", "corpus version string")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() == 0 {
		return fmt.Errorf("usage: cairn-eval corpus mine stackoverflow -out <dir> <response.json>…\n" +
			"there is no fetch path: api.stackexchange.com was unreachable where this tool was built,\n" +
			"and untested HTTP paging nobody has run is worse than none — download the responses (or\n" +
			"the quarterly data dump) yourself and normalize them here")
	}
	questions, err := corpus.LoadStackExchange(fs.Args())
	if err != nil {
		return err
	}
	items, queries, notes := corpus.NormalizeStackOverflow(questions)
	corpus.AssignSplits(queries)

	m, err := corpus.Write(*out, corpus.Manifest{
		ID:        "stackoverflow-duplicates",
		Version:   *version,
		CreatedAt: nowString(),
		Source: corpus.Source{
			Kind:    "stackoverflow-duplicates",
			Origin:  "api.stackexchange.com / Stack Exchange data dump",
			Command: "cairn-eval corpus mine stackoverflow -out … <operator-supplied responses>",
			License: "Stack Exchange content is CC BY-SA; attribution travels with it",
		},
		Labels: corpus.Labels{
			Provenance:  "Stack Overflow users and moderators closing a question as a duplicate of another",
			Independent: true,
		},
		Notes: notes,
	}, items, queries)
	if err != nil {
		return err
	}
	reportCorpus(m)
	return nil
}

func mineDocs(args []string) error {
	fs := flag.NewFlagSet("corpus mine docs", flag.ContinueOnError)
	root := fs.String("dir", "", "documentation checkout to mine")
	origin := fs.String("origin", "", "where the checkout came from (recorded in the manifest)")
	out := fs.String("out", "", "corpus output directory")
	id := fs.String("id", "docs-crossrefs", "corpus id")
	version := fs.String("version", "1", "corpus version string")
	independent := fs.Bool("independent", true, "false if this project authored the documentation being mined")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" || *out == "" {
		return fmt.Errorf("-dir and -out are required")
	}
	items, queries, notes, err := corpus.MineDocs(corpus.DocsMineOptions{Root: *root, Origin: *origin})
	if err != nil {
		return err
	}
	corpus.AssignSplits(queries)

	m, err := corpus.Write(*out, corpus.Manifest{
		ID:        *id,
		Version:   *version,
		CreatedAt: nowString(),
		Source: corpus.Source{
			Kind:    "doc-cross-references",
			Origin:  *origin,
			Command: "cairn-eval corpus mine docs -dir <checkout>",
			License: "documentation text belongs to its authors; check the project's licence before redistributing",
		},
		Labels: corpus.Labels{
			Provenance:  "documentation authors linking one page from another with descriptive anchor text",
			Independent: *independent,
		},
		Notes: notes,
	}, items, queries)
	if err != nil {
		return err
	}
	reportCorpus(m)
	return nil
}

func reportCorpus(m *corpus.Manifest) {
	fmt.Printf("wrote corpus %s v%s: %d items, %d queries (%d dev / %d holdout)\n",
		m.ID, m.Version, m.Counts.Items, m.Counts.Queries, m.Counts.Dev, m.Counts.Holdout)
	fmt.Printf("checksum %s\n", m.Checksum)
	for _, n := range m.Notes {
		fmt.Printf("note: %s\n", n)
	}
	if !m.Labels.Independent {
		fmt.Println("NOTE: labels are NOT independent of this project — a regression gate, not evidence.")
	}
}

// nowString stamps a manifest. Acquisition time is real time — a corpus is
// built by an operator, not inside a simulated-clock evaluation run.
func nowString() string { return time.Now().UTC().Format(time.RFC3339) }
