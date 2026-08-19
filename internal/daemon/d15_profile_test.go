package daemon_test

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/projection"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/identity"
)

// buildProfileCorpus creates (once) a persistent scorecard-shaped corpus of n
// events under root, reusing it if it is already there.
func buildProfileCorpus(t *testing.T, root string, n int) string {
	t.Helper()
	dir := filepath.Join(root, "cairn")
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAIRN_DEVICE_STATE_DIR", state)
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	marker := filepath.Join(root, "d15-profile-corpus")
	if _, err := os.Stat(filepath.Join(dir, "cairn.toml")); err == nil {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("%s was not built by this harness (no %s) — the variant bake-off writes scratch tables into the projection, so it refuses to touch a mesh it did not create", dir, marker)
		}
		fmt.Printf("  reusing corpus at %s\n", dir)
		return dir
	}
	if _, err := identity.Initialize(identity.InitOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("scorecard message %d: synthetic corpus entry about topic-%d with routine detail", i, i%97)
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: body}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
		if i > 0 && i%10000 == 0 {
			fmt.Printf("  … %d appended (%v)\n", i, time.Since(start).Round(time.Second))
		}
	}
	fmt.Printf("  built %d events in %v\n", n, time.Since(start).Round(time.Second))
	d.Close()
	if err := os.WriteFile(marker, []byte("scratch corpus for the D15 profile harness\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestD15BuildCorpus builds the persistent profiling corpus and stops.
//
//	CAIRN_PROFILE_DIR=/var/tmp/c100k CAIRN_PROFILE_N=100000 \
//	  go test -tags sqlite_fts5 -run TestD15BuildCorpus -v -timeout 120m ./internal/daemon/
func TestD15BuildCorpus(t *testing.T) {
	root := os.Getenv("CAIRN_PROFILE_DIR")
	if root == "" {
		t.Skip("set CAIRN_PROFILE_DIR and CAIRN_PROFILE_N")
	}
	n, err := strconv.Atoi(os.Getenv("CAIRN_PROFILE_N"))
	if err != nil || n < 100 {
		t.Fatalf("bad CAIRN_PROFILE_N")
	}
	buildProfileCorpus(t, root, n)
}

// ---- D15 profile ---------------------------------------------------------
//
// Where a search spends its milliseconds, measured rather than reasoned about.
// Reuses (or builds) the persistent corpus:
//
//	CAIRN_PROFILE_DIR=/var/tmp/d15/c100k CAIRN_PROFILE_N=100000 \
//	  go test -tags sqlite_fts5,cairn_testhooks -run TestD15Profile -v -timeout 120m ./internal/daemon/

// med returns the median of a duration sample.
func med(d []time.Duration) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(a, b int) bool { return s[a] < s[b] })
	return s[len(s)/2].Round(time.Microsecond)
}

// timeEach runs fn once per query and returns the median.
func timeEach(t *testing.T, queries []string, fn func(q string) error) time.Duration {
	t.Helper()
	var out []time.Duration
	for _, q := range queries {
		t0 := time.Now()
		if err := fn(q); err != nil {
			t.Fatal(err)
		}
		out = append(out, time.Since(t0))
	}
	return med(out)
}

func TestD15Profile(t *testing.T) {
	root := os.Getenv("CAIRN_PROFILE_DIR")
	if root == "" {
		t.Skip("set CAIRN_PROFILE_DIR and CAIRN_PROFILE_N")
	}
	n, err := strconv.Atoi(os.Getenv("CAIRN_PROFILE_N"))
	if err != nil || n < 100 {
		t.Fatalf("bad CAIRN_PROFILE_N")
	}
	dir := buildProfileCorpus(t, root, n)

	// The scorecard's 50 queries, verbatim.
	var queries []string
	for i := 0; i < 50; i++ {
		queries = append(queries, fmt.Sprintf("topic-%d routine", i%97))
	}

	// --- whole-search, through the daemon, exactly as the scorecard runs it --
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queries[:5] { // warm caches so the first query is not the median
		if _, err := d.Search(daemon.SearchOptions{Query: q, K: 10}); err != nil {
			t.Fatal(err)
		}
	}
	full := timeEach(t, queries, func(q string) error {
		_, err := d.Search(daemon.SearchOptions{Query: q, K: 10})
		return err
	})
	d.Close()

	// --- component timings against the projection directly ------------------
	p, err := projection.Open(projection.DBPath(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	db := p.DBForTest()
	k := config.FusionCandidatesFTS

	// Candidate 1 simulated: head/retracted carried beside the FTS rowid, so
	// the query needs ONE seek per match instead of three. Built here as a
	// scratch table rather than as a schema change, so the saving can be
	// measured before deciding whether the duplicated state is worth it.
	if _, err := db.Exec(`
		DROP TABLE IF EXISTS d15_flags;
		CREATE TABLE d15_flags (
		  rowid INTEGER PRIMARY KEY, message_id TEXT NOT NULL,
		  head INTEGER NOT NULL, retracted INTEGER NOT NULL);
		INSERT INTO d15_flags(rowid, message_id, head, retracted)
		SELECT map.rowid, m.message_id,
		       CASE WHEN m.head_revision_id = r.revision_id THEN 1 ELSE 0 END, m.retracted
		FROM fts_map map JOIN revisions r ON r.revision_id = map.revision_id
		JOIN messages m ON m.message_id = r.message_id;`); err != nil {
		t.Fatal(err)
	}

	// matched documents per query (the quantity D15 says the cost scales with)
	var matches []time.Duration
	matchN := 0
	for _, q := range queries[:1] {
		expr, _, err := p.LexicalMatchForTest(q, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM fts_revisions WHERE fts_revisions MATCH ?`, expr).Scan(&matchN); err != nil {
			t.Fatal(err)
		}
		_ = matches
	}

	// expressions, precomputed so the SQL variants time only the SQL
	expr := map[string]string{}
	for _, q := range queries {
		e, _, err := p.LexicalMatchForTest(q, false)
		if err != nil {
			t.Fatal(err)
		}
		expr[q] = e
	}

	drain := func(rows *sql.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
		}
		return rows.Err()
	}

	// The projection was just reopened, so its page cache is cold; the first
	// timed loop would otherwise be charged for misses the later ones never pay.
	for _, q := range queries {
		if _, _, err := p.LexicalTopKPlan(q, k, false); err != nil {
			t.Fatal(err)
		}
	}
	planT := timeEach(t, queries, func(q string) error {
		_, _, err := p.LexicalMatchForTest(q, false)
		return err
	})
	topkT := timeEach(t, queries, func(q string) error {
		_, _, err := p.LexicalTopKPlan(q, k, false)
		return err
	})
	prodT := timeEach(t, queries, func(q string) error {
		return drain(db.Query(`
			SELECT m.message_id
			FROM fts_revisions
			JOIN fts_map map ON fts_revisions.rowid = map.rowid
			JOIN revisions r ON r.revision_id = map.revision_id
			JOIN messages m ON m.message_id = r.message_id AND m.head_revision_id = r.revision_id
			WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
			ORDER BY bm25(fts_revisions), m.message_id
			LIMIT ?`, expr[q], 0, k))
	})
	headJoinT := timeEach(t, queries, func(q string) error {
		return drain(db.Query(`
			SELECT m.message_id
			FROM fts_revisions
			JOIN fts_map map ON fts_revisions.rowid = map.rowid
			JOIN messages m ON m.head_revision_id = map.revision_id
			WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
			ORDER BY bm25(fts_revisions), m.message_id
			LIMIT ?`, expr[q], 0, k))
	})
	noJoinT := timeEach(t, queries, func(q string) error {
		return drain(db.Query(`
			SELECT rowid FROM fts_revisions
			WHERE fts_revisions MATCH ?
			ORDER BY bm25(fts_revisions) LIMIT ?`, expr[q], k))
	})
	mapOnlyT := timeEach(t, queries, func(q string) error {
		return drain(db.Query(`
			SELECT map.revision_id FROM fts_revisions
			JOIN fts_map map ON fts_revisions.rowid = map.rowid
			WHERE fts_revisions MATCH ?
			ORDER BY bm25(fts_revisions), map.revision_id LIMIT ?`, expr[q], k))
	})
	twoJoinT := timeEach(t, queries, func(q string) error {
		return drain(db.Query(`
			SELECT r.message_id FROM fts_revisions
			JOIN fts_map map ON fts_revisions.rowid = map.rowid
			JOIN revisions r ON r.revision_id = map.revision_id
			WHERE fts_revisions MATCH ?
			ORDER BY bm25(fts_revisions), r.message_id LIMIT ?`, expr[q], k))
	})
	flagT := timeEach(t, queries, func(q string) error {
		return drain(db.Query(`
			SELECT f.message_id FROM fts_revisions
			JOIN d15_flags f ON fts_revisions.rowid = f.rowid
			WHERE fts_revisions MATCH ? AND f.head = 1 AND (f.retracted = 0 OR ?)
			ORDER BY bm25(fts_revisions), f.message_id LIMIT ?`, expr[q], 0, k))
	})
	countT := timeEach(t, queries, func(q string) error {
		var c int
		return db.QueryRow(`SELECT count(*) FROM fts_revisions WHERE fts_revisions MATCH ?`, expr[q]).Scan(&c)
	})
	bm25OnlyT := timeEach(t, queries, func(q string) error {
		return drain(db.Query(`SELECT bm25(fts_revisions) FROM fts_revisions WHERE fts_revisions MATCH ?`, expr[q]))
	})
	trigramT := timeEach(t, queries, func(q string) error {
		_, err := p.TrigramMessageHits(q, k, false)
		return err
	})

	// rank inputs over the candidate pool the search actually produced
	ids, _, err := p.LexicalTopKPlan(queries[0], k, false)
	if err != nil {
		t.Fatal(err)
	}
	rankT := timeEach(t, queries, func(q string) error {
		_, err := p.RankRows(ids, "")
		return err
	})
	metaT := timeEach(t, queries, func(q string) error {
		_, err := p.ResultMeta(ids[:10])
		return err
	})

	// Every search records its why-ranked arithmetic (R47/R51) — one
	// synchronous=FULL commit per search, and part of the fixed cost that does
	// not scale with the corpus.
	explRows := make([]projection.ExplanationRow, 0, 10)
	for i, id := range ids[:10] {
		explRows = append(explRows, projection.ExplanationRow{
			MessageID: id, ComponentsJSON: `{"R":"0.5","F":"0.5","P_eff":"0.5","RRF":"0.016393"}`, FinalRank: i + 1})
	}
	n2 := 0
	explT := timeEach(t, queries, func(q string) error {
		n2++
		return p.SaveExplanations(fmt.Sprintf("d15-profile-%d", n2), "p0_search", explRows)
	})

	fmt.Printf("D15 PROFILE n=%d  (median of %d scorecard queries; matches/query=%d, pool=%d)\n",
		n, len(queries), matchN, len(ids))
	fmt.Printf("  full d.Search                       %v\n", full)
	fmt.Printf("  LexicalTopKPlan                     %v\n", topkT)
	fmt.Printf("    term plan (D11/D14 df)            %v\n", planT)
	fmt.Printf("    word query, head-join (D15)       %v\n", headJoinT)
	fmt.Printf("      same, three-join filter (pre-D15) %v\n", prodT)
	fmt.Printf("      same, no joins at all           %v\n", noJoinT)
	fmt.Printf("      same, 1 join (fts_map)          %v\n", mapOnlyT)
	fmt.Printf("      same, 2 joins (+revisions)      %v\n", twoJoinT)
	fmt.Printf("      candidate 1: flags beside rowid %v\n", flagT)
	fmt.Printf("      match enumeration only (count)  %v\n", countT)
	fmt.Printf("      match + bm25, no sort/join      %v\n", bm25OnlyT)
	fmt.Printf("  trigram companion (skipped in prod) %v\n", trigramT)
	fmt.Printf("  RankRows(%d)                       %v\n", len(ids), rankT)
	fmt.Printf("  ResultMeta(10)                      %v\n", metaT)
	fmt.Printf("  SaveExplanations(10) [1 fsync]      %v\n", explT)
}

// TestD15Variants is the bake-off: candidate shapes for the word-index query,
// interleaved query by query so a drifting machine cannot flatter one of them,
// and each asserted to return the SAME ordered message ids as production.
func TestD15Variants(t *testing.T) {
	root := os.Getenv("CAIRN_PROFILE_DIR")
	if root == "" {
		t.Skip("set CAIRN_PROFILE_DIR and CAIRN_PROFILE_N")
	}
	n, err := strconv.Atoi(os.Getenv("CAIRN_PROFILE_N"))
	if err != nil || n < 100 {
		t.Fatalf("bad CAIRN_PROFILE_N")
	}
	dir := buildProfileCorpus(t, root, n)
	p, err := projection.Open(projection.DBPath(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	db := p.DBForTest()
	k := config.FusionCandidatesFTS

	var queries []string
	for i := 0; i < 97; i++ {
		queries = append(queries, fmt.Sprintf("topic-%d routine", i))
	}
	expr := map[string]string{}
	for _, q := range queries {
		e, _, err := p.LexicalMatchForTest(q, false)
		if err != nil {
			t.Fatal(err)
		}
		expr[q] = e
	}

	// scratch structures for the candidate shapes (dropped at the end)
	exec := func(sqlText string) {
		t.Helper()
		if _, err := db.Exec(sqlText); err != nil {
			t.Fatalf("%s: %v", sqlText, err)
		}
	}
	exec(`DROP TABLE IF EXISTS d15_flags`)
	exec(`CREATE TABLE d15_flags (rowid INTEGER PRIMARY KEY, message_id TEXT NOT NULL,
	        head INTEGER NOT NULL, retracted INTEGER NOT NULL)`)
	exec(`INSERT INTO d15_flags(rowid, message_id, head, retracted)
	        SELECT map.rowid, m.message_id,
	               CASE WHEN m.head_revision_id = r.revision_id THEN 1 ELSE 0 END, m.retracted
	        FROM fts_map map JOIN revisions r ON r.revision_id = map.revision_id
	        JOIN messages m ON m.message_id = r.message_id`)
	exec(`DROP TABLE IF EXISTS d15_map2`)
	exec(`CREATE TABLE d15_map2 (rowid INTEGER PRIMARY KEY, revision_id TEXT NOT NULL, message_id TEXT NOT NULL)`)
	exec(`INSERT INTO d15_map2(rowid, revision_id, message_id)
	        SELECT map.rowid, map.revision_id, r.message_id
	        FROM fts_map map JOIN revisions r ON r.revision_id = map.revision_id`)
	exec(`CREATE INDEX IF NOT EXISTS d15_msg_cov ON messages(message_id, head_revision_id, retracted)`)
	exec(`CREATE INDEX IF NOT EXISTS d15_rev_cov ON revisions(revision_id, message_id)`)
	exec(`CREATE INDEX IF NOT EXISTS d15_head_cov ON messages(head_revision_id, message_id, retracted)`)

	scan := func(rows *sql.Rows, err error) ([]string, error) {
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	}

	type variant struct {
		name string
		run  func(q string) ([]string, error)
	}
	variants := []variant{
		{"production (3 joins)", func(q string) ([]string, error) {
			return scan(db.Query(`
				SELECT m.message_id FROM fts_revisions
				JOIN fts_map map ON fts_revisions.rowid = map.rowid
				JOIN revisions r ON r.revision_id = map.revision_id
				JOIN messages m ON m.message_id = r.message_id AND m.head_revision_id = r.revision_id
				WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
				ORDER BY bm25(fts_revisions), m.message_id LIMIT ?`, expr[q], 0, k))
		}},
		{"+ covering idx on messages", func(q string) ([]string, error) {
			return scan(db.Query(`
				SELECT m.message_id FROM fts_revisions
				JOIN fts_map map ON fts_revisions.rowid = map.rowid
				JOIN revisions r ON r.revision_id = map.revision_id
				JOIN messages m INDEXED BY d15_msg_cov ON m.message_id = r.message_id AND m.head_revision_id = r.revision_id
				WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
				ORDER BY bm25(fts_revisions), m.message_id LIMIT ?`, expr[q], 0, k))
		}},
		{"+ covering idx on both", func(q string) ([]string, error) {
			return scan(db.Query(`
				SELECT m.message_id FROM fts_revisions
				JOIN fts_map map ON fts_revisions.rowid = map.rowid
				JOIN revisions r INDEXED BY d15_rev_cov ON r.revision_id = map.revision_id
				JOIN messages m INDEXED BY d15_msg_cov ON m.message_id = r.message_id AND m.head_revision_id = r.revision_id
				WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
				ORDER BY bm25(fts_revisions), m.message_id LIMIT ?`, expr[q], 0, k))
		}},
		{"message_id in map + covering", func(q string) ([]string, error) {
			return scan(db.Query(`
				SELECT m2.message_id FROM fts_revisions
				JOIN d15_map2 m2 ON fts_revisions.rowid = m2.rowid
				JOIN messages m INDEXED BY d15_msg_cov ON m.message_id = m2.message_id AND m.head_revision_id = m2.revision_id
				WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
				ORDER BY bm25(fts_revisions), m2.message_id LIMIT ?`, expr[q], 0, k))
		}},
		{"message_id in map, plain join", func(q string) ([]string, error) {
			return scan(db.Query(`
				SELECT m2.message_id FROM fts_revisions
				JOIN d15_map2 m2 ON fts_revisions.rowid = m2.rowid
				JOIN messages m ON m.message_id = m2.message_id AND m.head_revision_id = m2.revision_id
				WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
				ORDER BY bm25(fts_revisions), m2.message_id LIMIT ?`, expr[q], 0, k))
		}},
		{"head-join (no revisions join)", func(q string) ([]string, error) {
			return scan(db.Query(`
				SELECT m.message_id FROM fts_revisions
				JOIN fts_map map ON fts_revisions.rowid = map.rowid
				JOIN messages m ON m.head_revision_id = map.revision_id
				WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
				ORDER BY bm25(fts_revisions), m.message_id LIMIT ?`, expr[q], 0, k))
		}},
		{"head-join + covering idx", func(q string) ([]string, error) {
			return scan(db.Query(`
				SELECT m.message_id FROM fts_revisions
				JOIN fts_map map ON fts_revisions.rowid = map.rowid
				JOIN messages m INDEXED BY d15_head_cov ON m.head_revision_id = map.revision_id
				WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
				ORDER BY bm25(fts_revisions), m.message_id LIMIT ?`, expr[q], 0, k))
		}},
		{"flags beside the rowid", func(q string) ([]string, error) {
			return scan(db.Query(`
				SELECT f.message_id FROM fts_revisions
				JOIN d15_flags f ON fts_revisions.rowid = f.rowid
				WHERE fts_revisions MATCH ? AND f.head = 1 AND (f.retracted = 0 OR ?)
				ORDER BY bm25(fts_revisions), f.message_id LIMIT ?`, expr[q], 0, k))
		}},
	}

	samples := make([][]time.Duration, len(variants))
	var want []string
	for r := 0; r < 3; r++ { // three passes; variant order reversed on odd passes
		for _, q := range queries {
			order := make([]int, len(variants))
			for i := range order {
				order[i] = i
			}
			if r%2 == 1 {
				for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
					order[i], order[j] = order[j], order[i]
				}
			}
			got := make([][]string, len(variants))
			for _, i := range order {
				t0 := time.Now()
				ids, err := variants[i].run(q)
				el := time.Since(t0)
				if err != nil {
					t.Fatalf("%s: %v", variants[i].name, err)
				}
				samples[i] = append(samples[i], el)
				got[i] = ids
			}
			// every variant must return production's ids, in production's order
			want = got[0]
			for i := 1; i < len(variants); i++ {
				if len(got[i]) != len(want) {
					t.Fatalf("%s on %q: %d ids, production %d", variants[i].name, q, len(got[i]), len(want))
				}
				for j := range want {
					if got[i][j] != want[j] {
						t.Fatalf("%s on %q: id %d = %s, production %s", variants[i].name, q, j, got[i][j], want[j])
					}
				}
			}
		}
	}
	fmt.Printf("D15 VARIANTS n=%d  (median over %d queries x 3 interleaved passes; all return production's ids)\n", n, len(queries))
	for i, v := range variants {
		fmt.Printf("  %-34s %v\n", v.name, med(samples[i]))
	}
	exec(`DROP TABLE d15_flags`)
	exec(`DROP TABLE d15_map2`)
	exec(`DROP INDEX d15_msg_cov`)
	exec(`DROP INDEX d15_rev_cov`)
	exec(`DROP INDEX d15_head_cov`)
}
