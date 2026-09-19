package daemon_test

// D16 profiling — what the supersession lookup costs on the retrieval path,
// measured rather than argued about.
//
// The lookup rides RankRows, which D15 spent an entire sprint reducing: it now
// runs once per search over the ~100 fused candidates. D16 adds two correlated
// subqueries to that one statement (the live successor and its end date), each
// an indexed lookup on supersessions.message_id joined to messages.retracted.
// The question this harness answers is whether that is a rounding error against
// a ~12 ms search or a regression worth redesigning around.
//
// Env-gated and skipped by default; reuses the D15 corpus builder so the two
// sprints profile the same corpus rather than two hand-built approximations.
//
//	CAIRN_PROFILE_DIR=/var/tmp/d16/c20k CAIRN_PROFILE_N=20000 \
//	  go test -tags sqlite_fts5,cairn_testhooks -run TestD16Profile -v -timeout 120m ./internal/daemon/

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
)

func TestD16Profile(t *testing.T) {
	root := os.Getenv("CAIRN_PROFILE_DIR")
	if root == "" {
		t.Skip("set CAIRN_PROFILE_DIR and CAIRN_PROFILE_N")
	}
	n, err := strconv.Atoi(os.Getenv("CAIRN_PROFILE_N"))
	if err != nil || n < 100 {
		t.Fatalf("bad CAIRN_PROFILE_N")
	}
	dir := buildProfileCorpus(t, root, n)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p := d.Projection()

	// A pool of candidate ids the size a real search fuses.
	out, err := d.Search(daemon.SearchOptions{Query: "scorecard synthetic corpus", K: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) == 0 {
		t.Fatal("no results — the corpus does not match the probe query")
	}
	ids, err := p.DigestCandidates(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) > 100 {
		ids = ids[:100]
	}
	fmt.Printf("  corpus %d events, RankRows over %d candidates\n", n, len(ids))

	// warm every page the statement touches before timing anything (the D15
	// lesson: the first call pays for cache misses the later loops never do)
	for i := 0; i < 20; i++ {
		if _, err := p.RankRows(ids, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	// INTERLEAVED, not two runs an hour apart: run-to-run variance on this
	// container is ~10%, so the two shapes alternate inside one loop and the
	// medians come from the same conditions (D15's discipline).
	bakeOff := func(label string) {
		var with, without []time.Duration
		for i := 0; i < 200; i++ {
			t0 := time.Now()
			if _, err := p.RankRows(ids, "operator"); err != nil {
				t.Fatal(err)
			}
			with = append(with, time.Since(t0))
			t1 := time.Now()
			if _, err := p.RankRowsPreD16ForTest(ids, "operator"); err != nil {
				t.Fatal(err)
			}
			without = append(without, time.Since(t1))
		}
		fmt.Printf("  RankRows(%d) %-22s pre-D16 %v -> D16 %v\n", len(ids), label, med(without), med(with))
	}
	bakeOff("supersessions EMPTY")

	// now populate the relation over part of the pool and re-measure, so the
	// number is the cost of a lookup that FINDS something rather than the cost
	// of an index that is always empty
	// idempotent: the corpus is persistent and reused across runs, so a
	// duplicate assertion is expected and is not a failure here
	for i := 0; i+1 < len(ids) && i < 20; i += 2 {
		if _, err := d.Supersede(ids[i], ids[i+1], "profile", daemon.PublishRequest{Actor: "operator"}); err != nil &&
			!strings.Contains(err.Error(), "already recorded") {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ {
		if _, err := p.RankRows(ids, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	bakeOff("10 supersessions")

	// and the consolidation pass, which is the only whole-corpus scan D16 adds
	var s []time.Duration
	for i := 0; i < 20; i++ {
		t0 := time.Now()
		if _, err := d.ConsolidateOnce(); err != nil {
			t.Fatal(err)
		}
		s = append(s, time.Since(t0))
	}
	fmt.Printf("  ConsolidateOnce over %d messages    %v\n", n, med(s))

	full := timeEach(t, []string{"scorecard synthetic", "corpus entry", "routine detail topic-3"},
		func(q string) error {
			_, err := d.Search(daemon.SearchOptions{Query: q, K: 10})
			return err
		})
	fmt.Printf("  full d.Search                       %v\n", full)
}
