package daemon_test

// The acceptance harness for any change to the retrieval path (D14's criterion,
// made runnable by D15): it dumps the ordered top-10 corpus KEYS — not scores,
// which decay with wall time — for all 30 golden queries under P0/P2 x
// hybrid/lexical-only, plus each query's term plan. 120 lines. Run it on the
// parent commit and on the change and diff the two; identical output is the
// evidence that a candidate-set or ranking change moved nothing.
//
// Env-gated because it prints rather than asserts: there is no checked-in
// expectation to drift, and the comparison is always against a specific parent.
//
//	CAIRN_GOLDEN_DUMP=1 go test -tags sqlite_fts5,cairn_testhooks -run TestD15GoldenDump -v ./internal/daemon/

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/bench"
	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
)

func TestD15GoldenDump(t *testing.T) {
	if os.Getenv("CAIRN_GOLDEN_DUMP") == "" {
		t.Skip("set CAIRN_GOLDEN_DUMP=1")
	}
	msgs, queries, err := bench.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	idToKey := map[string]string{}
	for _, m := range msgs {
		res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: m.Body})
		if err != nil {
			t.Fatal(err)
		}
		idToKey[res.MessageID] = m.Key
	}
	for {
		n, err := d.EnrichOnce(config.EnrichBatch)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	for _, lexOnly := range []bool{false, true} {
		if lexOnly {
			d.SetEmbedderForTest(nil)
		}
		for _, p2 := range []bool{false, true} {
			d.SetRankProfileP2ForTest(p2)
			profile, mode := "P0", "hybrid"
			if p2 {
				profile = "P2"
			}
			if lexOnly {
				mode = "lexical-only"
			}
			for _, q := range queries {
				out, err := d.Search(daemon.SearchOptions{Query: q.Query, K: 10})
				if err != nil {
					t.Fatal(err)
				}
				var keys []string
				for _, r := range out.Results {
					keys = append(keys, idToKey[r.MessageID])
				}
				fmt.Printf("%s\t%s\t%s\tterms=%v\tcommon=%v\tunmatched=%v\tallcommon=%v\t%s\n",
					profile, mode, q.Query, out.LexicalQuery.Terms, out.LexicalQuery.Common,
					out.LexicalQuery.Unmatched, out.LexicalQuery.AllCommon, strings.Join(keys, ","))
			}
		}
	}
}
