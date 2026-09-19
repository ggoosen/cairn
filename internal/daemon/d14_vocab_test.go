package daemon_test

// D14 at the daemon layer: the two things the projection package cannot reach
// on its own — the derivative index (which needs a real attachment and the
// derive pipeline behind it) and the v8→v9 schema bump, whose rebuild path is
// what actually creates the new fts5vocab companions on an existing mesh.

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/projection"
)

// The derivatives index has its own document population, its own vocabulary,
// and its own D11 plan — a term common in message bodies may be rare in
// extracted attachment text. D14 moved both indexes; this is the second one.
func TestD14DerivativeIndexDecidesIdentically(t *testing.T) {
	dir := initCairn(t)
	d := startSubDaemon(t, dir)

	for _, text := range []string{
		"the quarterly fire safety compliance audit for the roastery premises",
		"the annual drainage levy invoice, the total payable and the due date",
		"the routine inspection checklist, the ordinary items and the sign-off",
		"the café façade repair quotation, itemised",
	} {
		if _, err := d.Publish(daemon.PublishRequest{
			Actor:       "operator",
			Body:        "attached paperwork",
			Attachments: []daemon.AttachmentIn{{Data: pdfFixture(text), Filename: "doc.pdf"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	deriveAll(t, d)

	p := d.Projection()
	var withCommon int
	for _, q := range []string{
		"the invoice total",
		"the drainage levy",
		"the routine the ordinary",
		"café invoice",
		"zzz the invoice",
		"the",
	} {
		vocab, probe, err := p.LexicalPlansForTest(q, true)
		if err != nil {
			t.Fatalf("derivative plans for %q: %v", q, err)
		}
		if !reflect.DeepEqual(vocab, probe) {
			t.Fatalf("D14 moved a D11 decision on the DERIVATIVE index for %q:\n  vocab = %+v\n  probe = %+v", q, vocab, probe)
		}
		if len(vocab.Common) > 0 || vocab.AllCommon {
			withCommon++
		}
	}
	if withCommon == 0 {
		t.Fatal("no derivative query judged a term common — the differential never reached the branch D14 changes")
	}
}

// The schema bump is only safe because the projection is DERIVED and the
// daemon rebuilds it from the log on drift. Exercised, not assumed: an
// on-disk projection that genuinely looks like v8 — old version stamp, no
// vocab companions — must be discarded and replayed, and the term probe must
// then answer from the companions the rebuild created.
func TestD14SchemaBumpRebuildsTheVocabCompanions(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	var bodies []string
	for i := 0; i < 12; i++ {
		bodies = append(bodies, "the drainage levy canary note about the routine business of the day")
	}
	bodies = append(bodies, "the council approved the drainage levy in March")
	for _, b := range bodies {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := projection.DBPath(dir)
	d.Close()

	// make the file a genuine v8: drop what v9 added, then stamp the version
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE fts_revisions_vocab`,
		`DROP TABLE fts_derivatives_vocab`,
		`UPDATE meta SET value='8' WHERE key='schema_version'`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db.Close()

	var warn strings.Builder
	d2, err := daemon.Start(daemon.Options{Dir: dir, Warn: &warn})
	if err != nil {
		t.Fatalf("daemon refused to start on the v9 bump: %v", err)
	}
	defer d2.Close()
	if !strings.Contains(warn.String(), "rebuilding the derived projection") {
		t.Fatalf("v8 projection was not rebuilt; warnings were: %q", warn.String())
	}

	// the probe now answers from a companion that only the rebuild could have
	// created, and it decides what the old probe decides
	p := d2.Projection()
	vocab, probe, err := p.LexicalPlansForTest("the council drainage levy", false)
	if err != nil {
		t.Fatalf("term probe after rebuild: %v", err)
	}
	if !reflect.DeepEqual(vocab, probe) {
		t.Fatalf("after rebuild the plans differ:\n  vocab = %+v\n  probe = %+v", vocab, probe)
	}
	if len(vocab.Common) == 0 {
		t.Fatalf("after rebuild no term was judged common on a corpus that says %q everywhere: %+v", "the", vocab)
	}
	out, err := d2.Search(daemon.SearchOptions{Query: "council drainage", K: 10, BudgetChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) == 0 {
		t.Fatal("search returned nothing after the rebuild")
	}
}
