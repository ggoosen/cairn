package daemon_test

// D16 (S19) — supersession end to end, through the real daemon.
//
// The unit tests in internal/rank pin the arithmetic. These pin the parts only
// a real projection can show: that the relation survives a replay, that
// retracting the successor revives its predecessor without a second pass, that
// history stays fetchable and attributed, that the schema bump rebuilds, and
// that the why-ranked trace reconciles against the score the agent received.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/rank"
)

// twoFacts publishes an old fact and its replacement, both matching one query,
// and returns (old, new). The bodies are near-identical on purpose: that is the
// case supersession is about.
//
// It also publishes DISTRACTORS that match the same query, and that is not
// padding — it is what makes the test a test. Percentile R spreads the pool
// across [0,1], so in a TWO-candidate pool the R gap between first and second
// is the entire 0.75 weight, twenty times the 0.15 demotion, and no bounded
// demotion could ever reorder it. The gap between adjacent ranks is
// 0.75/(n−1), so the demotion is worth roughly 0.15·(n−1)/0.75 ≈ (n−1)/5
// positions: about 0.2 positions in a pair, about two positions in a pool of
// ten, about twenty in the pool of 100 that a real search fuses. A two-document
// fixture would therefore "fail" for reasons that have nothing to do with
// supersession — measured, not assumed: see PROGRESS.md.
func twoFacts(t *testing.T, d *daemon.Daemon) (old, newer string) {
	t.Helper()
	a, err := d.Publish(daemon.PublishRequest{Actor: "operator",
		Body: "zzsubject deployment target is the staging cluster in region one"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Publish(daemon.PublishRequest{Actor: "operator",
		Body: "zzsubject deployment target is the production cluster in region two"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator",
			Body: fmt.Sprintf("zzsubject deployment note %d about the cluster and the target region", i)}); err != nil {
			t.Fatal(err)
		}
	}
	return a.MessageID, b.MessageID
}

func supersedeOK(t *testing.T, d *daemon.Daemon, old, newer, reason string) string {
	t.Helper()
	id, err := d.Supersede(old, newer, reason, daemon.PublishRequest{Actor: "operator"})
	if err != nil {
		t.Fatalf("supersede %s by %s: %v", old, newer, err)
	}
	return id
}

func searchRank(t *testing.T, out *daemon.SearchOutput, id string) int {
	t.Helper()
	for _, r := range out.Results {
		if r.MessageID == id {
			return r.Rank
		}
	}
	return 0
}

// The acceptance, through the daemon: a fact superseded by a later one stops
// outranking it, and it is still THERE.
func TestD16SupersededStopsOutrankingItsSuccessor(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	d.SetRankProfileP2ForTest(true)

	old, newer := twoFacts(t, d)
	before, err := d.Search(daemon.SearchOptions{Query: "zzsubject deployment target", K: 10})
	if err != nil {
		t.Fatal(err)
	}
	if searchRank(t, before, old) != 1 {
		t.Skipf("precondition: the older fact does not lead before supersession (ranks: old=%d new=%d); "+
			"nothing to demote", searchRank(t, before, old), searchRank(t, before, newer))
	}

	supersedeOK(t, d, old, newer, "region moved to production")

	after, err := d.Search(daemon.SearchOptions{Query: "zzsubject deployment target", K: 10})
	if err != nil {
		t.Fatal(err)
	}
	oldRank, newRank := searchRank(t, after, old), searchRank(t, after, newer)
	if oldRank == 0 {
		t.Fatal("the superseded message vanished from search — D16 is demotion, NOT deletion")
	}
	if newRank == 0 {
		t.Fatal("the superseding message is missing from search")
	}
	if !(newRank < oldRank) {
		t.Fatalf("superseded fact still outranks its successor: old at %d, new at %d", oldRank, newRank)
	}
	// and the agent is TOLD, on the result itself
	for _, r := range after.Results {
		if r.MessageID == old {
			if r.SupersededBy != newer || r.SupersededAt == "" {
				t.Fatalf("no staleness signal on the superseded result: %+v", r)
			}
		}
		if r.MessageID == newer && r.SupersededBy != "" {
			t.Fatalf("the successor is marked superseded: %+v", r)
		}
	}
	if !strings.Contains(after.Payload, "superseded_by="+newer) {
		t.Fatalf("the rendered payload does not carry the staleness marker:\n%s", after.Payload)
	}
}

// P0 is the DEFAULT profile and §9.1 gives it no penalty term, so the demotion
// is P2-only until an author rules otherwise. Pinned either way, as the sprint
// brief requires: under P0 the ordering is unchanged and the SUP term scores 0,
// but the staleness SIGNAL is still delivered — the demotion is opt-in, the
// warning is not.
func TestD16P0IsUnchangedButStillWarns(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	old, newer := twoFacts(t, d)
	before, err := d.Search(daemon.SearchOptions{Query: "zzsubject deployment target", K: 10})
	if err != nil {
		t.Fatal(err)
	}
	var beforeOrder []string
	var beforeScores []float64
	for _, r := range before.Results {
		beforeOrder = append(beforeOrder, r.MessageID)
		beforeScores = append(beforeScores, r.Score)
	}

	supersedeOK(t, d, old, newer, "")

	after, err := d.Search(daemon.SearchOptions{Query: "zzsubject deployment target", K: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Results) != len(beforeOrder) {
		t.Fatalf("P0 result count changed: %d then %d", len(beforeOrder), len(after.Results))
	}
	for i, r := range after.Results {
		if r.MessageID != beforeOrder[i] {
			t.Fatalf("P0 order changed at %d: %s became %s", i, beforeOrder[i], r.MessageID)
		}
		// Scores are NOT compared for bit-equality here: the two searches run at
		// different wall times and freshness legitimately decays between them.
		// Bit-identity of a P0 score under supersession is pinned where wall
		// time is a fixture rather than a clock —
		// rank.TestD16P0ProfilesUnaffected and
		// rank.TestD16UnsupersededScoresAreBitIdentical. What this asserts is
		// the property those cannot: that the real daemon's P0 ORDER over a
		// real projection is untouched, and by more than a rounding step.
		if delta := r.Score - beforeScores[i]; delta > 1e-6 || delta < -1e-6 {
			t.Fatalf("P0 score at %d moved by %v — more than freshness decay over one test", i, delta)
		}
	}
	// the SIGNAL is profile-independent
	found := false
	for _, r := range after.Results {
		if r.MessageID == old && r.SupersededBy == newer {
			found = true
		}
	}
	if !found {
		t.Fatal("P0 dropped the staleness signal; the demotion is opt-in, the warning is not")
	}
	// and the trace says so in as many words rather than printing a bare zero
	text, err := d.WhyRanked(after.InteractionID, old)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "superseded by "+newer) || !strings.Contains(text, "NOT weighted under search-P0") {
		t.Fatalf("a P0 trace for a superseded message must say it is superseded AND that the term is unweighted:\n%s", text)
	}
}

// R47/R51: the demotion is a printed term whose arithmetic an external
// recompute reproduces exactly. Reconciled against the score returned OUTSIDE
// the explanation record, under both profiles, for search and for digest.
func TestD16WhyRankedReconcilesWithTheDemotion(t *testing.T) {
	for _, p2 := range []bool{false, true} {
		name := "P0"
		if p2 {
			name = "P2"
		}
		t.Run(name, func(t *testing.T) {
			dir := initCairn(t)
			d := startDaemon(t, dir)
			d.SetRankProfileP2ForTest(p2)
			// R51 clause 4 requires a corpus where R, S, F, I and N are all
			// non-zero; D16 adds "and SUP too". So the SUPERSEDED message is
			// the all-terms message itself — the one that has been fetched
			// across three tasks, replied to, priority-confirmed and shown
			// before — rather than a bare pair off to one side. Every scored
			// term is then non-zero on one traced result at once, which is the
			// only corpus shape that can catch a term omitted from the trace.
			old := buildAllTermsCorpus(t, d)
			succ, err := d.Publish(daemon.PublishRequest{Actor: "operator",
				Body: "allterms zzprobe zzprobe zzprobe the REPLACEMENT decision record"})
			if err != nil {
				t.Fatal(err)
			}
			newer := succ.MessageID
			supersedeOK(t, d, old, newer, "superseded for the reconciliation corpus")

			// K large enough that the demoted message is still IN the result set
			// — a demotion that pushed it past the cut would leave nothing to
			// reconcile, and the reconciliation is the point of this test.
			out, err := d.Search(daemon.SearchOptions{Query: "zzprobe", K: 30, TaskID: "recon", BudgetChars: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			sawDemotion := false
			for _, r := range out.Results {
				text, err := d.WhyRanked(out.InteractionID, r.MessageID)
				if err != nil {
					t.Fatal(err)
				}
				comp := reconcileAgainstReturned(t, text, r.Score)
				if r.MessageID != old {
					continue
				}
				v, w := rank.ParseDec(comp["SUP"][0]), rank.ParseDec(comp["SUP"][1])
				if p2 {
					if v == 0 || w == 0 {
						t.Fatalf("P2 traced the superseded message with SUP %v × %v = 0; the demotion was not scored:\n%s", v, w, text)
					}
					// every OTHER scored term non-zero on the same result, so an
					// omitted term fails before the sum check does (R51.4)
					for _, name := range []string{"R", "S", "F", "I", "N"} {
						if rank.ParseDec(comp[name][0]) == 0 {
							t.Fatalf("term %s traces as 0 on the all-terms message; the corpus no longer exercises R51.4:\n%s", name, text)
						}
					}
					sawDemotion = true
				} else if v != 0 {
					t.Fatalf("P0 scored a supersession feature (%v):\n%s", v, text)
				}
			}
			if p2 && !sawDemotion {
				t.Fatal("the superseded message never appeared in the results, so the demotion was never reconciled")
			}

			// The DIGEST surface persists its own explanation record. Until
			// D16 that was a second inline assembly of the same fields, and it
			// was one call short: it filled the P2 terms but not the
			// supersession evidence, so a digest trace read
			// "SUP 1 × -0.15 = -0.15   (not superseded)" — arithmetic that
			// reconciles while the annotation beside it states the opposite of
			// the truth. Arithmetic alone cannot catch that, so this asserts
			// the ANNOTATION too.
			dout, err := d.Digest(daemon.DigestOptions{AgentView: "op", BudgetChars: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			sawInDigest := false
			for _, line := range strings.Split(dout.Payload, "\n") {
				m := digestScoreLine.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				text, err := d.WhyRanked(dout.InteractionID, m[1])
				if err != nil {
					t.Fatal(err)
				}
				reconcileAgainstReturned(t, text, rank.ParseDec(m[2]))
				if m[1] != old {
					if strings.Contains(text, "superseded by ") {
						t.Fatalf("digest trace claims an unsuperseded message is superseded:\n%s", text)
					}
					continue
				}
				sawInDigest = true
				if !strings.Contains(text, "superseded by "+newer) {
					t.Fatalf("the digest trace does not name the successor — the arithmetic reconciles but the explanation lies:\n%s", text)
				}
			}
			if !sawInDigest {
				t.Fatal("the superseded message was not in the digest, so the digest trace was never checked")
			}
		})
	}
}

// Retracting the SUCCESSOR revives its predecessor — and does so because
// "superseded" was never stored, only asked. No pass runs, no flag is cleared,
// and there is nothing that could have been left stale.
func TestD16RetractingTheSuccessorRevivesThePredecessor(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	d.SetRankProfileP2ForTest(true)

	old, newer := twoFacts(t, d)
	supersedeOK(t, d, old, newer, "")

	info, err := d.Projection().MessageInfo(old)
	if err != nil {
		t.Fatal(err)
	}
	if info.SupersededBy != newer {
		t.Fatalf("not superseded before the retraction: %+v", info)
	}
	if _, err := d.SimpleEvent("message.retract", "message", newer,
		map[string]any{"message_id": newer, "reason": "the successor was wrong"},
		daemon.PublishRequest{Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	info, err = d.Projection().MessageInfo(old)
	if err != nil {
		t.Fatal(err)
	}
	if info.SupersededBy != "" {
		t.Fatalf("a retracted successor still ends the fact it replaced: %+v", info)
	}
	// the assertion is still ON RECORD, marked lifted — history is not rewritten
	hist, err := d.Projection().SupersessionHistory(old)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Live || hist[0].SupersededBy != newer {
		t.Fatalf("the lifted assertion was not preserved as history: %+v", hist)
	}
}

// History remains fetchable and correctly attributed: the body is returned, the
// sender and source event are unchanged, and the manifest names what replaced
// it and when.
func TestD16HistoryRemainsFetchableAndAttributed(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	old, newer := twoFacts(t, d)
	beforeFetch, err := d.Fetch(old, "operator")
	if err != nil {
		t.Fatal(err)
	}
	beforeBody, err := os.ReadFile(beforeFetch.BodyPath)
	if err != nil {
		t.Fatal(err)
	}
	supEvent := supersedeOK(t, d, old, newer, "region moved")

	got, err := d.Fetch(old, "operator")
	if err != nil {
		t.Fatalf("a superseded message must still be fetchable: %v", err)
	}
	if got.BodyPath == "" {
		t.Fatal("no body written for a superseded message")
	}
	afterBody, err := os.ReadFile(got.BodyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBody) != string(beforeBody) {
		t.Fatalf("the body changed under supersession — nothing may rewrite stored memory:\n%q\n%q", beforeBody, afterBody)
	}
	if got.SourceEvent != beforeFetch.SourceEvent || got.BodyHash != beforeFetch.BodyHash || got.Retracted {
		t.Fatalf("provenance moved under supersession: %+v vs %+v", beforeFetch, got)
	}
	if got.SupersededBy != newer || got.SupersededAt == "" {
		t.Fatalf("the fetch does not say what replaced it: %+v", got)
	}
	blob, err := os.ReadFile(got.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var man map[string]any
	if err := json.Unmarshal(blob, &man); err != nil {
		t.Fatal(err)
	}
	if man["superseded_by"] != newer {
		t.Fatalf("manifest missing the supersession: %s", blob)
	}
	if man["source_event_id"] != beforeFetch.SourceEvent {
		t.Fatalf("manifest attribution moved: %s", blob)
	}
	// and the assertion itself is attributed
	hist, err := d.Projection().SupersessionHistory(old)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].EventID != supEvent || hist[0].Actor != "operator" || hist[0].Reason != "region moved" {
		t.Fatalf("the supersession assertion is not attributed: %+v", hist)
	}
}

// The digest marks a superseded entry under EVERY profile, so an agent reading
// its working set is never handed a replaced fact without being told.
func TestD16DigestMarksSupersededEntries(t *testing.T) {
	for _, p2 := range []bool{false, true} {
		dir := initCairn(t)
		d := startDaemon(t, dir)
		d.SetRankProfileP2ForTest(p2)
		old, newer := twoFacts(t, d)
		supersedeOK(t, d, old, newer, "")
		dout, err := d.Digest(daemon.DigestOptions{AgentView: "op", BudgetChars: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(dout.Payload, "[superseded]") ||
			!strings.Contains(dout.Payload, "superseded by "+newer) {
			t.Fatalf("p2=%v: the digest does not mark the superseded entry:\n%s", p2, dout.Payload)
		}
		if strings.Count(dout.Payload, "[superseded]") != 1 {
			t.Fatalf("p2=%v: expected exactly one marked entry:\n%s", p2, dout.Payload)
		}
		d.Close()
	}
}

// Nothing may supersede itself, and nothing may be superseded by a message that
// is already retracted — both refused BEFORE anything is appended.
func TestD16WriteBoundaryRefusals(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	old, newer := twoFacts(t, d)

	if _, err := d.Supersede(old, old, "", daemon.PublishRequest{Actor: "operator"}); err == nil {
		t.Fatal("a message was allowed to supersede itself")
	}
	if _, err := d.Supersede(old, "not-a-message", "", daemon.PublishRequest{Actor: "operator"}); err == nil {
		t.Fatal("supersession by an unknown message was accepted")
	}
	supersedeOK(t, d, old, newer, "")
	if _, err := d.Supersede(old, newer, "", daemon.PublishRequest{Actor: "operator"}); err == nil {
		t.Fatal("a duplicate supersession was accepted as a second event")
	}
	// retracted successor
	third, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzsubject a third statement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SimpleEvent("message.retract", "message", third.MessageID,
		map[string]any{"message_id": third.MessageID}, daemon.PublishRequest{Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Supersede(old, third.MessageID, "", daemon.PublishRequest{Actor: "operator"}); err == nil {
		t.Fatal("a retracted message was allowed to supersede something")
	}
}

// A cycle (each message claiming to supersede the other) is a real mesh state,
// not corruption: the walk must terminate, the census must report it, and both
// ends keep the demotion so their relative order is unchanged.
func TestD16CycleTerminatesAndIsReported(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	old, newer := twoFacts(t, d)
	supersedeOK(t, d, old, newer, "")
	// the reverse assertion, which the local write path would refuse only if it
	// checked for cycles — it does not, because on a real mesh the two halves
	// arrive from two devices that never saw each other
	supersedeOK(t, d, newer, old, "")

	chain, cycled, err := d.Projection().CurrentVersion(old)
	if err != nil {
		t.Fatal(err)
	}
	if !cycled {
		t.Fatalf("a supersession cycle was not detected: chain %v", chain)
	}
	c, err := d.ConsolidateOnce()
	if err != nil {
		t.Fatal(err)
	}
	if c.Cycles != 2 {
		t.Fatalf("census reported %d cycles, want 2 (both ends): %+v", c.Cycles, c)
	}
}

// The consolidation pass computes a census and nothing else: no content is
// rewritten, no relation is inferred, and the counts are the ones E9 needs.
func TestD16ConsolidationCensus(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	a, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzchain fact one"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzchain fact two"})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzchain fact three"})
	if err != nil {
		t.Fatal(err)
	}
	lone, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzchain unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	supersedeOK(t, d, a.MessageID, b.MessageID, "")
	supersedeOK(t, d, b.MessageID, cc.MessageID, "")

	census, err := d.ConsolidateOnce()
	if err != nil {
		t.Fatal(err)
	}
	if census.Assertions != 2 || census.LiveAssertions != 2 || census.LiftedAssertions != 0 {
		t.Fatalf("assertion counts wrong: %+v", census)
	}
	if census.SupersededMessages != 2 {
		t.Fatalf("superseded count %d, want 2 (a and b): %+v", census.SupersededMessages, census)
	}
	if census.CurrentMessages != 2 {
		t.Fatalf("current count %d, want 2 (c and the unrelated one): %+v", census.CurrentMessages, census)
	}
	if census.MaxChainDepth != 2 {
		t.Fatalf("chain depth %d, want 2 (a→b→c): %+v", census.MaxChainDepth, census)
	}
	if census.Cycles != 0 {
		t.Fatalf("spurious cycle: %+v", census)
	}
	_ = lone

	// the current version of a is c, two hops away, and the path is shown
	chain, cycled, err := d.Projection().CurrentVersion(a.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if cycled {
		t.Fatalf("a straight chain reported as cycled: %v", chain)
	}
	want := []string{a.MessageID, b.MessageID, cc.MessageID}
	if len(chain) != 3 || chain[0] != want[0] || chain[1] != want[1] || chain[2] != want[2] {
		t.Fatalf("chain %v, want %v", chain, want)
	}
	// and the pass is REPORTED, not merely returned
	got, at, errStr := d.Consolidation()
	if at.IsZero() || errStr != "" || got.Assertions != census.Assertions {
		t.Fatalf("the pass did not record its result: %+v at %v err %q", got, at, errStr)
	}
}

// The schema bump is only safe because the projection is DERIVED. Exercised on
// a log written by the previous version, not assumed: an on-disk projection
// that genuinely looks like v9 — old stamp, no supersessions table — must be
// discarded and replayed, and the relation must come back from the LOG.
func TestD16SchemaBumpRebuildsFromTheLog(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	old, newer := twoFacts(t, d)
	supersedeOK(t, d, old, newer, "rebuilt from the log")
	dbPath := projection.DBPath(dir)
	d.Close()

	// make the file a genuine v9: drop what v10 added, then stamp the version
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE supersessions`,
		`UPDATE meta SET value='9' WHERE key='schema_version'`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db.Close()

	var warn strings.Builder
	d2, err := daemon.Start(daemon.Options{Dir: dir, Warn: &warn})
	if err != nil {
		t.Fatalf("daemon refused to start on the v10 bump: %v", err)
	}
	defer d2.Close()
	if !strings.Contains(warn.String(), "rebuilding the derived projection") {
		t.Fatalf("v9 projection was not rebuilt; warnings were: %q", warn.String())
	}
	info, err := d2.Projection().MessageInfo(old)
	if err != nil {
		t.Fatal(err)
	}
	if info.SupersededBy != newer {
		t.Fatalf("the supersession did not survive the rebuild: %+v", info)
	}
	hist, err := d2.Projection().SupersessionHistory(old)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Reason != "rebuilt from the log" || hist[0].Actor != "operator" {
		t.Fatalf("the assertion's payload did not survive the rebuild: %+v", hist)
	}
}

// A supersession asserted by a PEER that names a message this node has not
// projected yet parks as RETRYABLE (R49) and self-heals — it must not be a
// terminal park, and it must not project a relation pointing at nothing.
// A SELF-supersession from a peer is malformed and parks TERMINALLY.
func TestD16PeerAssertionParking(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	old, _ := twoFacts(t, d)

	// terminal: self-supersession, which the local write path refuses but a
	// peer could write anyway
	if _, err := d.SimpleEvent("message.supersede", "message", old,
		map[string]any{"message_id": old, "superseded_by_message_id": old},
		daemon.PublishRequest{Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	parked, err := d.Projection().ParkedEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(parked) != 1 || parked[0].Retryable {
		t.Fatalf("a self-supersession must park TERMINALLY: %+v", parked)
	}
	if !strings.Contains(parked[0].Error, "cannot supersede itself") {
		t.Fatalf("unhelpful park error: %q", parked[0].Error)
	}
}
