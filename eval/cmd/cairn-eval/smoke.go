package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ggoosen/cairn/eval/internal/backend"
	"github.com/ggoosen/cairn/eval/internal/cairnctl"
	"github.com/ggoosen/cairn/eval/internal/corpus"
	"github.com/ggoosen/cairn/eval/internal/result"
	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// runSmoke exercises the whole apparatus against the three-document plumbing
// fixture and writes a result file. It answers "does the harness work", not
// "does Cairn work" — see backend.PlumbingFixture for why the distinction is
// not negotiable. The output says so on every line that could be misread.
func runSmoke(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	backends := fs.String("backends", "B0,B1,B2,B5", "comma-separated memory conditions to exercise")
	outDir := fs.String("out", "", "directory for run records (default: a temp dir, printed)")
	seed := fs.Int64("seed", 1, "deterministic seed recorded with the run")
	corpusDir := fs.String("corpus", "", "exercise the loader against a corpus directory instead of the built-in fixture (non-independent corpora only)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fixture := plumbingFixture()
	if *corpusDir != "" {
		loaded, err := loadPlumbingCorpus(*corpusDir)
		if err != nil {
			return err
		}
		fixture = loaded
	}

	dest := *outDir
	if dest == "" {
		d, err := os.MkdirTemp("", "cairn-eval-results-")
		if err != nil {
			return err
		}
		dest = d
	}

	for _, name := range strings.Split(*backends, ",") {
		id := backend.ID(strings.TrimSpace(name))
		if id == "" {
			continue
		}
		path, err := smokeOne(ctx, id, dest, *seed, fixture)
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		fmt.Printf("%-3s plumbing OK — %s\n", id, path)
	}
	fmt.Printf("\nrun records in %s\n", dest)
	fmt.Printf("These are PLUMBING-VERIFICATION records over %s (%d items, %d queries).\n",
		fixture.corpus.ID, fixture.corpus.ItemCount, fixture.corpus.QueryCount)
	fmt.Printf("Label source: %s\n", fixture.corpus.LabelSource)
	fmt.Println("They are not evaluation results and no metric has been computed from them.")
	return nil
}

// fixtureSet is the material a plumbing run pushes through a backend: items,
// questions with their known answers, and the provenance of both. It is
// deliberately the SAME shape whether it came from the built-in fixture or
// from a corpus directory, so the loader path is exercised by the same code
// the fixture path uses.
type fixtureSet struct {
	items   []backend.Item
	queries []backend.PlumbingQuery
	corpus  result.Corpus
}

func plumbingFixture() fixtureSet {
	items := backend.PlumbingFixture()
	return fixtureSet{
		items:   items,
		queries: backend.PlumbingQueries(),
		corpus: result.Corpus{
			ID: "plumbing-fixture", Version: "1",
			ItemCount: len(items), QueryCount: len(backend.PlumbingQueries()),
			LabelSource: "SYNTHETIC — authored by this project; apparatus check only, not evidence",
		},
	}
}

// loadPlumbingCorpus exercises the E3 loader through the harness.
//
// It REFUSES an independent corpus, on purpose. Pushing mined human-labelled
// ground truth through the backends and recording per-query outcomes is the
// first half of a measurement, and measurement does not begin until the kill
// criteria in eval/claims.yaml carry an operator signoff (BUILD-PLAN §5-E1).
// The guard is here rather than in a comment because a flag that quietly did
// the thing would be used.
func loadPlumbingCorpus(dir string) (fixtureSet, error) {
	c, err := corpus.Load(dir)
	if err != nil {
		return fixtureSet{}, err
	}
	if c.Manifest.Labels.Independent {
		return fixtureSet{}, fmt.Errorf(
			"corpus %s carries INDEPENDENT human labels: running it here would be the first half of a measurement.\n"+
				"Measurement verbs arrive with E4, after eval/claims.yaml carries operator signoffs on the kill criteria.\n"+
				"Use `cairn-eval corpus verify` to check this corpus, or the sample corpus to exercise the plumbing",
			c.Manifest.ID)
	}
	set := fixtureSet{
		items: c.BackendItems(),
		corpus: result.Corpus{
			ID: c.Manifest.ID, Version: c.Manifest.Version, Checksum: c.Manifest.Checksum,
			ItemCount: c.Manifest.Counts.Items, QueryCount: c.Manifest.Counts.Queries,
			LabelSource: c.Manifest.Labels.Provenance,
		},
	}
	for _, q := range c.Queries {
		set.queries = append(set.queries, backend.PlumbingQuery{ID: q.ID, Query: q.Query, Expected: q.Relevant[0]})
	}
	return set, nil
}

func smokeOne(ctx context.Context, id backend.ID, outDir string, seed int64, fixture fixtureSet) (string, error) {
	b, err := backend.New(id)
	if err != nil {
		return "", err
	}
	if backend.IsStub(b) {
		return "", fmt.Errorf("%w: %s is a declared stub", backend.ErrNotImplemented, id)
	}

	work, err := os.MkdirTemp("", "cairn-eval-"+string(id)+"-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(work) }()

	cfg := backend.Config{WorkDir: work, Seed: seed}
	meta := result.Backend{ID: string(id), Notes: b.Capabilities().Notes}
	if id == backend.B5Cairn {
		bin, err := cairnctl.FindBinary(ctx)
		if err != nil {
			return "", err
		}
		cfg.Binary = bin
		meta.CairnVersion, meta.CairnBuildTags = bin.Version, bin.Tags
	}
	if err := b.Open(ctx, cfg); err != nil {
		return "", err
	}
	defer func() { _ = b.Close(ctx) }()

	for _, it := range fixture.items {
		if _, err := b.Write(ctx, it); err != nil {
			return "", fmt.Errorf("write %s: %w", it.ID, err)
		}
	}

	run := result.NewRun(result.KindPlumbing, seed, meta, fixture.corpus)
	run.Note("plumbing verification: proves the harness can drive %s end to end. No metric computed.", id)

	for _, q := range fixture.queries {
		for _, surface := range []backend.Surface{backend.SurfaceSearch, backend.SurfaceDigest} {
			out := result.Outcome{QueryID: q.ID + "/" + string(surface), Query: q.Query, Surface: string(surface)}
			resp, err := b.Retrieve(ctx, backend.Request{
				Surface: surface, Query: q.Query,
				K: tunables.DefaultK, BudgetChars: tunables.DefaultBudgetChars,
			})
			switch {
			case errors.Is(err, backend.ErrUnsupportedSurface):
				out.Error = "surface not implemented by this backend"
				run.Add(out)
				continue
			case err != nil:
				return "", fmt.Errorf("retrieve %s/%s: %w", q.ID, surface, err)
			}
			out.Relevant = []string{q.Expected}
			for _, h := range resp.Hits {
				out.Returned = append(out.Returned, h.ItemID)
				out.ReturnedNative = append(out.ReturnedNative, h.NativeID)
			}
			out.PayloadChars = len([]rune(resp.Payload))
			out.BudgetChars = tunables.DefaultBudgetChars
			out.ElapsedMS = resp.Elapsed.Milliseconds()
			out.Partial, out.PartialReason = resp.Partial, resp.PartialReason
			out.InteractionID = resp.InteractionID
			out.Raw = resp.Raw
			if resp.RetrievalMode != "" {
				meta.RetrievalMode = resp.RetrievalMode
				run.Backend.RetrievalMode = resp.RetrievalMode
			}
			run.Add(out)
		}
	}

	path := filepath.Join(outDir, fmt.Sprintf("plumbing-%s-%s.json", id, time.Now().UTC().Format("20060102T150405Z")))
	if err := run.WriteFile(path); err != nil {
		return "", err
	}
	return path, nil
}
