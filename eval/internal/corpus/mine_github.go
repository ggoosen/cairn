package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// GitHub duplicate-issue mining (EVAL-PLAN §5-E3).
//
// WHY THIS SOURCE. When a maintainer marks issue #B a duplicate of #A they
// have made a relevance judgment: "the answer to this question is over
// there". They made it in the course of their own work, about their own
// project, with no knowledge of Cairn and nothing at stake in it. That is
// precisely the property the authored golden corpus lacks, and it is
// available in bulk and for free.
//
// THERE IS NO FETCH PATH HERE, and that is deliberate twice over. The
// operator downloads with `gh api --paginate` (see corpora/ACQUISITION.md),
// which already handles auth, pagination and rate limits properly, and this
// package normalizes the saved responses. So the harness module stays
// entirely network-free — the T0 tier's offline property becomes structural
// rather than a habit — and the code that IS here is the code where a
// mistake would be invisible in the finished corpus, which is why it is the
// code with tests. Untested HTTP paging that nobody has run would have been
// the least trustworthy part of the pipeline.
//
// MODELLING CHOICES, to be read before believing any number derived from
// this corpus:
//
//   - The DUPLICATE issue's title is the query; the CANONICAL issue is the
//     single relevant item. Title-only is the honest shape of the ask ("has
//     anyone hit this?"), but it favours lexical matching, and that should
//     be remembered when a lexical arm scores well. -query-with-body exists
//     for the alternative.
//   - Duplicate issues are EXCLUDED from the item set. If a duplicate were
//     also a document, a search for its own title would return itself, which
//     is trivially correct and measures nothing.
//   - Only the timeline's `marked_as_duplicate` event counts by default:
//     someone used the duplicate control deliberately. Free-text "duplicate
//     of #123" comments are a weaker signal and are labelled differently
//     when included, never silently mixed.

// GitHubIssue is the subset of the REST issue object the miner uses.
type GitHubIssue struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	State       string `json:"state"`
	StateReason string `json:"state_reason"`
	CreatedAt   string `json:"created_at"`
	HTMLURL     string `json:"html_url"`
	// PullRequest is non-nil on pull requests, which the issues endpoint
	// returns and which are not issues.
	PullRequest map[string]any `json:"pull_request,omitempty"`
}

// GitHubTimelineEvent is the subset of a timeline entry the miner uses. The
// canonical issue has appeared under both `source.issue` and `issue`
// depending on the event and API version, so both are read; a corpus built
// from a shape we guessed wrong would be silently wrong, and the miner
// reports how many events it could not resolve rather than dropping them
// quietly.
type GitHubTimelineEvent struct {
	Event     string `json:"event"`
	CreatedAt string `json:"created_at"`
	Source    *struct {
		Issue *GitHubIssue `json:"issue"`
	} `json:"source"`
	Issue *GitHubIssue `json:"issue"`
}

// GitHubMineOptions tunes normalization.
type GitHubMineOptions struct {
	Repo          string // "owner/name", used in item ids and the manifest
	QueryWithBody bool   // include the duplicate's body in the query text
	BodyChars     int    // cap on included body characters (0 → tunables default)
}

// GitHubRaw is the input to normalization: the issues and, per issue number,
// its timeline.
type GitHubRaw struct {
	Issues    []GitHubIssue
	Timelines map[int][]GitHubTimelineEvent
}

// NormalizeGitHub turns fetched GitHub data into corpus items and queries.
// It is pure: no network, no clock, no randomness.
func NormalizeGitHub(raw GitHubRaw, opts GitHubMineOptions) ([]Item, []Query, []string, error) {
	if opts.Repo == "" {
		return nil, nil, nil, fmt.Errorf("repo is required (it namespaces item ids)")
	}
	bodyChars := opts.BodyChars
	if bodyChars <= 0 {
		bodyChars = tunables.MinedBodyChars
	}

	byNumber := map[int]GitHubIssue{}
	for _, is := range raw.Issues {
		if is.PullRequest != nil {
			continue
		}
		byNumber[is.Number] = is
	}

	// duplicate number → canonical number
	dupOf := map[int]int{}
	labeledAt := map[int]string{}
	unresolved := 0
	for num, events := range raw.Timelines {
		for _, ev := range events {
			if ev.Event != "marked_as_duplicate" {
				continue
			}
			canonical := 0
			switch {
			case ev.Source != nil && ev.Source.Issue != nil && ev.Source.Issue.Number != 0:
				canonical = ev.Source.Issue.Number
			case ev.Issue != nil && ev.Issue.Number != 0:
				canonical = ev.Issue.Number
			}
			if canonical == 0 || canonical == num {
				unresolved++
				continue
			}
			dupOf[num] = canonical
			labeledAt[num] = ev.CreatedAt
		}
	}

	itemID := func(n int) string { return fmt.Sprintf("gh-%s-%d", strings.ReplaceAll(opts.Repo, "/", "-"), n) }

	var items []Item
	for num, is := range byNumber {
		if _, isDup := dupOf[num]; isDup {
			continue // a query's source is not also a document
		}
		items = append(items, Item{
			ID:        itemID(num),
			Title:     is.Title,
			Body:      is.Body,
			CreatedAt: is.CreatedAt,
			URL:       is.HTMLURL,
			Topics:    []string{"corpus/github-issues"},
		})
	}

	var queries []Query
	skippedMissingCanonical := 0
	for num, canonical := range dupOf {
		dup, ok := byNumber[num]
		if !ok {
			continue
		}
		if _, ok := byNumber[canonical]; !ok {
			// the canonical was never fetched (closed long ago, or outside
			// the page window): dropping is right, but it is counted
			skippedMissingCanonical++
			continue
		}
		if _, isAlsoDup := dupOf[canonical]; isAlsoDup {
			// duplicate chains: the canonical is itself a query source and
			// therefore not an item. Dropping keeps every judgment pointing
			// at something that exists.
			skippedMissingCanonical++
			continue
		}
		text := dup.Title
		if opts.QueryWithBody {
			text = strings.TrimSpace(text + "\n" + truncate(dup.Body, bodyChars))
		}
		queries = append(queries, Query{
			ID:        fmt.Sprintf("q-%s", itemID(num)),
			Query:     text,
			Relevant:  []string{itemID(canonical)},
			LabelKind: LabelDuplicateIssueTimeline,
			LabelURL:  dup.HTMLURL,
			LabeledAt: labeledAt[num],
		})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	sort.Slice(queries, func(i, j int) bool { return queries[i].ID < queries[j].ID })

	var notes []string
	notes = append(notes, fmt.Sprintf("query text = the duplicate issue's title%s; the canonical issue is the single relevant item",
		map[bool]string{true: " and body", false: " only"}[opts.QueryWithBody]))
	notes = append(notes, "duplicate issues are excluded from the item set: a document that answers its own query measures nothing")
	if unresolved > 0 {
		notes = append(notes, fmt.Sprintf("%d marked_as_duplicate events did not name a resolvable canonical issue and were dropped", unresolved))
	}
	if skippedMissingCanonical > 0 {
		notes = append(notes, fmt.Sprintf("%d duplicates pointed at an issue outside the fetched set (or at another duplicate) and were dropped", skippedMissingCanonical))
	}
	return items, queries, notes, nil
}

// LoadGitHubRaw reads the API responses the operator downloaded (see
// corpora/ACQUISITION.md): files named issues-*.json holding issue arrays,
// and timeline-<number>.json holding one issue's timeline. Keeping the raw
// responses is what makes an acquisition auditable — normalization can be
// re-run, and argued with, long after the fetch.
func LoadGitHubRaw(rawDir string) (*GitHubRaw, error) {
	entries, err := os.ReadDir(rawDir)
	if err != nil {
		return nil, err
	}
	raw := &GitHubRaw{Timelines: map[int][]GitHubTimelineEvent{}}
	for _, e := range entries {
		name := e.Name()
		blob, err := os.ReadFile(filepath.Join(rawDir, name))
		if err != nil {
			return nil, err
		}
		switch {
		case strings.HasPrefix(name, "issues-"):
			var batch []GitHubIssue
			if err := json.Unmarshal(blob, &batch); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			raw.Issues = append(raw.Issues, batch...)
		case strings.HasPrefix(name, "timeline-"):
			var num int
			if _, err := fmt.Sscanf(name, "timeline-%d.json", &num); err != nil {
				return nil, fmt.Errorf("%s: unexpected name", name)
			}
			var events []GitHubTimelineEvent
			if err := json.Unmarshal(blob, &events); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			raw.Timelines[num] = events
		}
	}
	return raw, nil
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
