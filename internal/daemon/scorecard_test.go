package daemon_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/projection"
)

// TestScorecard is the TESTING.md §5 engineering scorecard. Env-gated (it
// writes N real fsynced events):
//
//	CAIRN_SCORECARD=10000 go test -tags sqlite_fts5 -run TestScorecard -v -timeout 120m ./internal/daemon/
//
// Numbers are printed for PROGRESS.md.
func TestScorecard(t *testing.T) {
	nStr := os.Getenv("CAIRN_SCORECARD")
	if nStr == "" {
		t.Skip("set CAIRN_SCORECARD=<events> to run the scorecard")
	}
	n, err := strconv.Atoi(nStr)
	if err != nil || n < 100 {
		t.Fatalf("bad CAIRN_SCORECARD %q", nStr)
	}

	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}

	// --- append: send-ack latency over n publishes ---------------------------
	ackLat := make([]time.Duration, 0, n)
	appendStart := time.Now()
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("scorecard message %d: synthetic corpus entry about topic-%d with routine detail", i, i%97)
		t0 := time.Now()
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: body}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
		ackLat = append(ackLat, time.Since(t0))
		if i > 0 && i%10000 == 0 {
			fmt.Printf("  … %d appended (%v elapsed)\n", i, time.Since(appendStart).Round(time.Second))
		}
	}
	appendTotal := time.Since(appendStart)

	// ack→lexical-visible P95 from telemetry (the <200 ms gate at 100k)
	g, err := d.Telemetry().Gates()
	if err != nil {
		t.Fatal(err)
	}
	visP95 := time.Duration(g.LatencyP95Micros) * time.Microsecond

	// --- search latency -------------------------------------------------------
	searchLat := make([]time.Duration, 0, 50)
	for i := 0; i < 50; i++ {
		t0 := time.Now()
		if _, err := d.Search(daemon.SearchOptions{Query: fmt.Sprintf("topic-%d routine", i%97), K: 10}); err != nil {
			t.Fatal(err)
		}
		searchLat = append(searchLat, time.Since(t0))
	}
	d.Close()

	// --- cold recovery --------------------------------------------------------
	t0 := time.Now()
	d2, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	coldRecovery := time.Since(t0)
	d2.Close()

	// --- reindex --lexical (side-build + swap) --------------------------------
	t0 = time.Now()
	if _, err := projection.ReindexLexical(fsx.OS{}, dir, projection.DBPath(dir),
		func() cairnlog.VerifyFunc { return identity.NewChainVerifier().Verify },
		nil); err != nil {
		t.Fatal(err)
	}
	reindexLexical := time.Since(t0)

	// --- reindex --semantic ---------------------------------------------------
	// NOTE: this measures the MECHANISM, not a real model. The embedder here is
	// BagOfWords (deterministic, in-process); a sentence-transformers venv would
	// be dominated by subprocess and model time. Read it as "what the pipeline
	// costs around the embedder", never as an embedding benchmark.
	d3, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t0 = time.Now()
	nSem, err := d3.ReindexSemantic()
	if err != nil {
		t.Fatal(err)
	}
	reindexSemantic := time.Since(t0)
	d3.Close()

	// --- backup size + restore time -------------------------------------------
	// Cairn has no backup verb: the PORTABLE DIRECTORY is the backup unit, so a
	// backup is a copy of it and a restore is opening that copy. Restore time is
	// therefore copy + verified open, and both halves are reported.
	backupBytes, err := dirBytes(dir)
	if err != nil {
		t.Fatal(err)
	}
	restoreDir := filepath.Join(t.TempDir(), "restored")
	t0 = time.Now()
	copyTree(t, dir, restoreDir)
	restoreCopy := time.Since(t0)
	t0 = time.Now()
	d4, err := daemon.Start(daemon.Options{Dir: restoreDir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatalf("restored copy did not open: %v", err)
	}
	restoreOpen := time.Since(t0)
	d4.Close()

	// --- daemon RSS -----------------------------------------------------------
	// The daemon runs IN-PROCESS in this test, so process RSS is the honest
	// figure available; it includes the test harness itself. VmRSS is read where
	// the platform exposes it, with the Go runtime's own reservation beside it.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	rss, rssOK := processRSSBytes()

	fmt.Printf("SCORECARD n=%d\n", n)
	fmt.Printf("  append total          %v  (%.2f ms/event)\n", appendTotal.Round(time.Millisecond), float64(appendTotal.Microseconds())/float64(n)/1000)
	fmt.Printf("  send-ack P50 / P95    %v / %v\n", pct(ackLat, 50), pct(ackLat, 95))
	fmt.Printf("  ack→lexical-visible P95  %v  (gate < 200ms)\n", visP95)
	fmt.Printf("  search P50 / P95      %v / %v\n", pct(searchLat, 50), pct(searchLat, 95))
	fmt.Printf("  cold recovery         %v\n", coldRecovery.Round(time.Millisecond))
	fmt.Printf("  reindex --lexical     %v\n", reindexLexical.Round(time.Millisecond))
	fmt.Printf("  reindex --semantic    %v  (%d revisions; BagOfWords, NOT a model benchmark)\n", reindexSemantic.Round(time.Millisecond), nSem)
	fmt.Printf("  backup size           %.1f MB  (portable dir; %.2f KB/event)\n", float64(backupBytes)/(1<<20), float64(backupBytes)/float64(n)/1024)
	fmt.Printf("  restore time          %v  (copy %v + verified open %v)\n",
		(restoreCopy + restoreOpen).Round(time.Millisecond), restoreCopy.Round(time.Millisecond), restoreOpen.Round(time.Millisecond))
	if rssOK {
		fmt.Printf("  process RSS           %.1f MB  (in-process daemon + harness; Go runtime reserved %.1f MB)\n",
			float64(rss)/(1<<20), float64(ms.Sys)/(1<<20))
	} else {
		fmt.Printf("  process RSS           unavailable on this platform (Go runtime reserved %.1f MB)\n", float64(ms.Sys)/(1<<20))
	}

	if visP95 >= 200*time.Millisecond {
		t.Fatalf("GATE FAIL: ack→lexical-visible P95 %v ≥ 200ms", visP95)
	}
}

func pct(d []time.Duration, p int) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(a, b int) bool { return s[a] < s[b] })
	i := (len(s) * p) / 100
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i].Round(time.Microsecond)
}

// dirBytes sums the apparent size of every regular file under root.
func dirBytes(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			total += fi.Size()
		}
		return nil
	})
	return total, err
}

// processRSSBytes reads resident set size where the platform exposes it
// (Linux /proc). ok=false elsewhere — reported as unavailable rather than
// guessed, since a wrong RSS is worse than no RSS.
func processRSSBytes() (int64, bool) {
	blob, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(blob), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
