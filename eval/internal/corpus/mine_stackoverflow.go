package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Stack Overflow duplicate mining (BUILD-PLAN §5-E3).
//
// A question closed as a duplicate carries the same kind of free human
// judgment as a GitHub duplicate link, made by people with no stake here.
//
// ACQUISITION IS AN OPERATOR STEP, and unlike GitHub there is no fetch path
// in this tool at all: api.stackexchange.com is unreachable from the
// environment this was built in, and shipping untested HTTP paging that
// nobody has ever run would be worse than shipping none. The operator
// fetches (or downloads the quarterly data dump) and this normalizes what
// they hand over — which is also the part where a mistake would be
// invisible, so it is the part with tests.
//
// Expected input: a Stack Exchange API response envelope, e.g.
//
//	https://api.stackexchange.com/2.3/questions?site=stackoverflow
//	    &filter=<one that includes body and closed_details>
//
// MODELLING CHOICES: the duplicate question's title is the query; the
// original question(s) named in closed_details are the relevant items;
// duplicates are excluded from the item set for the same reason as GitHub's.

// SEQuestion is the subset of a Stack Exchange question the miner uses.
type SEQuestion struct {
	QuestionID    int    `json:"question_id"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Link          string `json:"link"`
	CreationDate  int64  `json:"creation_date"` // unix seconds
	ClosedReason  string `json:"closed_reason"`
	ClosedDetails *struct {
		OriginalQuestions []struct {
			QuestionID int    `json:"question_id"`
			Title      string `json:"title"`
		} `json:"original_questions"`
	} `json:"closed_details"`
}

// SEResponse is the API envelope.
type SEResponse struct {
	Items []SEQuestion `json:"items"`
}

// LoadStackExchange reads one or more saved API responses.
func LoadStackExchange(paths []string) ([]SEQuestion, error) {
	var out []SEQuestion
	for _, p := range paths {
		blob, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var resp SEResponse
		if err := json.Unmarshal(blob, &resp); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, resp.Items...)
	}
	return out, nil
}

// NormalizeStackOverflow turns saved questions into items and queries. Pure.
func NormalizeStackOverflow(questions []SEQuestion) ([]Item, []Query, []string) {
	byID := map[int]SEQuestion{}
	dupOf := map[int][]int{}
	for _, q := range questions {
		byID[q.QuestionID] = q
		if q.ClosedDetails == nil {
			continue
		}
		for _, orig := range q.ClosedDetails.OriginalQuestions {
			if orig.QuestionID != 0 && orig.QuestionID != q.QuestionID {
				dupOf[q.QuestionID] = append(dupOf[q.QuestionID], orig.QuestionID)
			}
		}
	}

	itemID := func(id int) string { return fmt.Sprintf("so-%d", id) }

	var items []Item
	for id, q := range byID {
		if _, isDup := dupOf[id]; isDup {
			continue
		}
		created := ""
		if q.CreationDate > 0 {
			created = time.Unix(q.CreationDate, 0).UTC().Format(time.RFC3339)
		}
		items = append(items, Item{
			ID:        itemID(id),
			Title:     q.Title,
			Body:      q.Body,
			CreatedAt: created,
			URL:       q.Link,
			Topics:    []string{"corpus/stackoverflow"},
		})
	}

	var queries []Query
	missing := 0
	for id, originals := range dupOf {
		dup := byID[id]
		var relevant []string
		for _, o := range originals {
			if _, ok := byID[o]; !ok {
				// The original was not in the download. Keeping the judgment
				// would make retrieval look wrong for a document that was
				// never in the corpus.
				missing++
				continue
			}
			if _, alsoDup := dupOf[o]; alsoDup {
				missing++
				continue
			}
			relevant = append(relevant, itemID(o))
		}
		if len(relevant) == 0 {
			continue
		}
		sort.Strings(relevant)
		queries = append(queries, Query{
			ID:        "q-" + itemID(id),
			Query:     strings.TrimSpace(dup.Title),
			Relevant:  relevant,
			LabelKind: LabelStackOverflowDuplicate,
			LabelURL:  dup.Link,
		})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	sort.Slice(queries, func(i, j int) bool { return queries[i].ID < queries[j].ID })

	notes := []string{
		"query text = the duplicate question's title; the original question(s) are the relevant items",
		"duplicate questions are excluded from the item set",
	}
	if missing > 0 {
		notes = append(notes, fmt.Sprintf("%d duplicate→original links pointed outside the downloaded set and were dropped", missing))
	}
	return items, queries, notes
}
