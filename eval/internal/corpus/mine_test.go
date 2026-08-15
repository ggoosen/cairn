package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Miner tests cover NORMALIZATION — the part where a mistake would be
// invisible in the finished corpus. Acquisition (HTTP) is an operator step
// and is not tested here; see FetchGitHub's comment for why pretending
// otherwise would be worse.

func githubFixture() GitHubRaw {
	return GitHubRaw{
		Issues: []GitHubIssue{
			{Number: 1, Title: "server crashes on empty config", Body: "panic on startup", State: "closed",
				CreatedAt: "2026-01-01T00:00:00Z", HTMLURL: "https://github.com/o/r/issues/1"},
			{Number: 2, Title: "crash when config file is empty", Body: "same panic", State: "closed",
				StateReason: "duplicate", CreatedAt: "2026-01-02T00:00:00Z", HTMLURL: "https://github.com/o/r/issues/2"},
			{Number: 3, Title: "empty config causes a panic", Body: "also this", State: "closed",
				StateReason: "duplicate", CreatedAt: "2026-01-03T00:00:00Z", HTMLURL: "https://github.com/o/r/issues/3"},
			{Number: 4, Title: "add a --version flag", Body: "would be handy", State: "closed",
				CreatedAt: "2026-01-04T00:00:00Z", HTMLURL: "https://github.com/o/r/issues/4"},
			{Number: 5, Title: "a pull request", Body: "not an issue", State: "closed",
				PullRequest: map[string]any{"url": "…"}},
		},
		Timelines: map[int][]GitHubTimelineEvent{
			// shape A: the canonical arrives under source.issue
			2: {{Event: "marked_as_duplicate", CreatedAt: "2026-01-02T01:00:00Z",
				Source: &struct {
					Issue *GitHubIssue `json:"issue"`
				}{Issue: &GitHubIssue{Number: 1}}}},
			// shape B: the canonical arrives under issue
			3: {{Event: "marked_as_duplicate", CreatedAt: "2026-01-03T01:00:00Z",
				Issue: &GitHubIssue{Number: 1}}},
		},
	}
}

func TestNormalizeGitHubUsesMaintainerJudgments(t *testing.T) {
	items, queries, notes, err := NormalizeGitHub(githubFixture(), GitHubMineOptions{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}

	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
	}
	// #1 and #4 are documents; #2 and #3 are queries, not documents; #5 is a
	// pull request and not an issue at all.
	for _, want := range []string{"gh-o-r-1", "gh-o-r-4"} {
		if !ids[want] {
			t.Fatalf("item %s missing; have %v", want, ids)
		}
	}
	for _, unwanted := range []string{"gh-o-r-2", "gh-o-r-3", "gh-o-r-5"} {
		if ids[unwanted] {
			t.Fatalf("%s should not be an item (duplicate source or pull request)", unwanted)
		}
	}

	if len(queries) != 2 {
		t.Fatalf("got %d queries, want 2 (both timeline shapes must resolve)", len(queries))
	}
	for _, q := range queries {
		if len(q.Relevant) != 1 || q.Relevant[0] != "gh-o-r-1" {
			t.Fatalf("query %s judged %v relevant, want [gh-o-r-1]", q.ID, q.Relevant)
		}
		if q.LabelKind != LabelDuplicateIssueTimeline {
			t.Fatalf("query %s has label kind %q", q.ID, q.LabelKind)
		}
		if q.LabelURL == "" || q.LabeledAt == "" {
			t.Fatalf("query %s is not auditable: %+v", q.ID, q)
		}
		if strings.Contains(q.Query, "panic on startup") {
			t.Fatalf("query %s carries body text without -query-with-body", q.ID)
		}
	}
	if len(notes) == 0 {
		t.Fatal("normalization recorded no modelling notes")
	}
}

func TestNormalizeGitHubDropsUnresolvableAndChained(t *testing.T) {
	raw := githubFixture()
	// #2 now points at an issue that was never fetched.
	raw.Timelines[2] = []GitHubTimelineEvent{{Event: "marked_as_duplicate",
		Source: &struct {
			Issue *GitHubIssue `json:"issue"`
		}{Issue: &GitHubIssue{Number: 999}}}}
	// #4 is marked a duplicate of #3, which is itself a duplicate (a chain).
	raw.Timelines[4] = []GitHubTimelineEvent{{Event: "marked_as_duplicate",
		Issue: &GitHubIssue{Number: 3}}}

	_, queries, notes, err := NormalizeGitHub(raw, GitHubMineOptions{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queries {
		for _, r := range q.Relevant {
			if r == "gh-o-r-999" || r == "gh-o-r-3" {
				t.Fatalf("query %s kept a judgment pointing at a non-item (%s)", q.ID, r)
			}
		}
	}
	joined := strings.Join(notes, " ")
	if !strings.Contains(joined, "dropped") {
		t.Fatalf("dropped judgments were not reported: %v", notes)
	}
}

func TestGitHubRawRoundTripsThroughDisk(t *testing.T) {
	dir := t.TempDir()
	raw := githubFixture()
	blob, _ := json.Marshal(raw.Issues)
	if err := os.WriteFile(filepath.Join(dir, "issues-001.json"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	for num, events := range raw.Timelines {
		blob, _ := json.Marshal(events)
		name := filepath.Join(dir, "timeline-"+itoa(num)+".json")
		if err := os.WriteFile(name, blob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	back, err := LoadGitHubRaw(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Issues) != len(raw.Issues) || len(back.Timelines) != len(raw.Timelines) {
		t.Fatalf("round trip lost data: %d issues / %d timelines", len(back.Issues), len(back.Timelines))
	}
	// Offline re-normalization must produce the same corpus as the fetch did:
	// that is what makes acquisition auditable after the fact.
	wantItems, wantQueries, _, _ := NormalizeGitHub(raw, GitHubMineOptions{Repo: "o/r"})
	gotItems, gotQueries, _, _ := NormalizeGitHub(*back, GitHubMineOptions{Repo: "o/r"})
	if len(gotItems) != len(wantItems) || len(gotQueries) != len(wantQueries) {
		t.Fatal("offline normalization differs from the in-memory one")
	}
}

func TestNormalizeStackOverflow(t *testing.T) {
	questions := []SEQuestion{
		{QuestionID: 100, Title: "how do I parse a date in Go", Body: "…", Link: "https://stackoverflow.com/q/100", CreationDate: 1700000000},
		{QuestionID: 200, Title: "golang parse date string", Body: "…", Link: "https://stackoverflow.com/q/200",
			ClosedReason: "Duplicate", ClosedDetails: &struct {
				OriginalQuestions []struct {
					QuestionID int    `json:"question_id"`
					Title      string `json:"title"`
				} `json:"original_questions"`
			}{OriginalQuestions: []struct {
				QuestionID int    `json:"question_id"`
				Title      string `json:"title"`
			}{{QuestionID: 100}}}},
		{QuestionID: 300, Title: "unrelated", Body: "…", Link: "https://stackoverflow.com/q/300",
			ClosedDetails: &struct {
				OriginalQuestions []struct {
					QuestionID int    `json:"question_id"`
					Title      string `json:"title"`
				} `json:"original_questions"`
			}{OriginalQuestions: []struct {
				QuestionID int    `json:"question_id"`
				Title      string `json:"title"`
			}{{QuestionID: 999}}}}, // original not downloaded
	}
	items, queries, notes := NormalizeStackOverflow(questions)
	if len(items) != 1 || items[0].ID != "so-100" {
		t.Fatalf("items = %+v, want just so-100 (duplicates are not documents)", items)
	}
	if len(queries) != 1 || queries[0].Relevant[0] != "so-100" {
		t.Fatalf("queries = %+v", queries)
	}
	if queries[0].LabelKind != LabelStackOverflowDuplicate {
		t.Fatalf("label kind %q", queries[0].LabelKind)
	}
	if !strings.Contains(strings.Join(notes, " "), "dropped") {
		t.Fatalf("the judgment pointing outside the download was not reported: %v", notes)
	}
}

func TestMineDocsUsesAnchorTextAsQuery(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("index.md", "# Index\n\nSee the [retry and backoff policy](guides/retries.md) for details.\n"+
		"Also [here](guides/retries.md) and [retries.md](guides/retries.md).\n"+
		"External [docs](https://example.com/x) are ignored, and so is [an anchor](#section).\n")
	write("guides/retries.md", "# Retries\n\nThree attempts with exponential backoff.\n")

	items, queries, notes, err := MineDocs(DocsMineOptions{Root: root, Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if len(queries) != 1 {
		t.Fatalf("queries = %+v, want exactly one (uninformative anchors dropped)", queries)
	}
	q := queries[0]
	if q.Query != "retry and backoff policy" || q.Relevant[0] != "doc-guides-retries" {
		t.Fatalf("query = %+v", q)
	}
	if q.LabelKind != LabelDocCrossReference {
		t.Fatalf("label kind %q", q.LabelKind)
	}
	if !strings.Contains(strings.Join(notes, " "), "weaker judgment") {
		t.Fatalf("the weaker-signal caveat is missing from the notes: %v", notes)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
