package backend

// PlumbingFixture is a three-document, obviously-synthetic set used to prove
// the APPARATUS works: that a backend can be opened, written to, queried,
// and can map a hit back to the corpus item that produced it.
//
// IT IS NOT A CORPUS AND MUST NEVER BE USED AS ONE. It is tiny, it was
// written by this project, and its "relevance judgments" are its author's —
// exactly the circularity BUILD-PLAN §3.2 and §5-E3 exist to break. Any
// number computed over it describes the plumbing, not Cairn. Runs against it
// are recorded with Kind=plumbing-verification for that reason.
func PlumbingFixture() []Item {
	return []Item{
		{
			ID:     "plumb-001",
			Title:  "purple widget calibration",
			Body:   "The purple widget is calibrated with a torque of forty-two newton metres. Zephyr fixtures require the long spanner.",
			Topics: []string{"eval/plumbing"},
		},
		{
			ID:     "plumb-002",
			Title:  "quokka migration schedule",
			Body:   "Quokka migration runs every third Tuesday. The convoy assembles at the harbour before dawn.",
			Topics: []string{"eval/plumbing"},
		},
		{
			ID:     "plumb-003",
			Title:  "lighthouse keeper rota",
			Body:   "The lighthouse keeper rota rotates weekly. Bring the brass key and the logbook.",
			Topics: []string{"eval/plumbing"},
		},
	}
}

// PlumbingQuery is a query whose only correct answer in the fixture is
// obvious by construction — a distinctive nonsense phrase that appears in
// exactly one item.
type PlumbingQuery struct {
	ID       string
	Query    string
	Expected string // the fixture item id that contains the phrase
}

// PlumbingQueries returns the fixture's queries.
func PlumbingQueries() []PlumbingQuery {
	return []PlumbingQuery{
		{ID: "q1", Query: "zephyr spanner", Expected: "plumb-001"},
		{ID: "q2", Query: "quokka convoy harbour", Expected: "plumb-002"},
		{ID: "q3", Query: "lighthouse brass logbook", Expected: "plumb-003"},
	}
}
