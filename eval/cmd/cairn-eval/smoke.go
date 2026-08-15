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
	if err := fs.Parse(args); err != nil {
		return err
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
		path, err := smokeOne(ctx, id, dest, *seed)
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		fmt.Printf("%-3s plumbing OK — %s\n", id, path)
	}
	fmt.Printf("\nrun records in %s\n", dest)
	fmt.Println("These are PLUMBING-VERIFICATION records over a 3-document synthetic fixture.")
	fmt.Println("They are not evaluation results and no metric has been computed from them.")
	return nil
}

func smokeOne(ctx context.Context, id backend.ID, outDir string, seed int64) (string, error) {
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

	items := backend.PlumbingFixture()
	for _, it := range items {
		if _, err := b.Write(ctx, it); err != nil {
			return "", fmt.Errorf("write %s: %w", it.ID, err)
		}
	}

	run := result.NewRun(result.KindPlumbing, seed, meta, result.Corpus{
		ID:          "plumbing-fixture",
		Version:     "1",
		ItemCount:   len(items),
		QueryCount:  len(backend.PlumbingQueries()),
		LabelSource: "SYNTHETIC — authored by this project; apparatus check only, not evidence",
	})
	run.Note("plumbing verification: proves the harness can drive %s end to end. No metric computed.", id)

	for _, q := range backend.PlumbingQueries() {
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
