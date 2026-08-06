package daemon_test

// RETR-D functional tests: results carry content (D1), digest entries are
// attributable (D2), search scopes are hard pre-filters (D3), threads
// expand (D4), topics are browsable (D5).

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
)

func retrDaemon(t *testing.T) *daemon.Daemon {
	t.Helper()
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// D1: search results carry sender/created/topics/snippet — triageable
// without a fetch — and the payload quotes body content per line.
func TestSearchResultsCarryContent(t *testing.T) {
	d := retrDaemon(t)
	if _, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "the espresso machine descaling procedure uses citric acid",
		Topics: []string{"workshop/coffee"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := d.Search(daemon.SearchOptions{Query: "descaling procedure", K: 5})
	if err != nil || len(out.Results) != 1 {
		t.Fatalf("search: %v (%d results)", err, len(out.Results))
	}
	r := out.Results[0]
	if r.Sender != "operator" || r.CreatedAt == "" {
		t.Fatalf("result missing sender/created_at: %+v", r)
	}
	if len(r.Topics) != 1 || r.Topics[0] != "workshop/coffee" {
		t.Fatalf("result missing topics: %+v", r.Topics)
	}
	if !strings.Contains(r.Snippet, "citric acid") {
		t.Fatalf("result missing snippet: %q", r.Snippet)
	}
	if !strings.Contains(out.Payload, config.QuotePrefix) ||
		!strings.Contains(out.Payload, "from=operator") ||
		!strings.Contains(out.Payload, "topics=workshop/coffee") {
		t.Fatalf("payload not enriched/quoted:\n%s", out.Payload)
	}
}

// D2: digest entries carry an attribution line.
func TestDigestEntriesAttributable(t *testing.T) {
	d := retrDaemon(t)
	if _, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "digest attribution check body",
		Topics: []string{"proj/alpha"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := d.Digest(daemon.DigestOptions{AgentView: "operator", BudgetChars: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Payload, "from operator") || !strings.Contains(out.Payload, "proj/alpha") {
		t.Fatalf("digest entry not attributable:\n%s", out.Payload)
	}
}

// D3: topic/sender/thread scopes are hard filters, and a nonexistent
// scope topic refuses pre-ack instead of silently returning nothing.
func TestSearchScopeFilters(t *testing.T) {
	d := retrDaemon(t)
	if _, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "shared keyword in topic one",
		Topics: []string{"scope/one"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{
		Actor: "agent-b", Body: "shared keyword in topic two",
		Topics: []string{"scope/two"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatal(err)
	}

	out, err := d.Search(daemon.SearchOptions{Query: "shared keyword", K: 10})
	if err != nil || len(out.Results) != 2 {
		t.Fatalf("unscoped: %v (%d)", err, len(out.Results))
	}
	out, err = d.Search(daemon.SearchOptions{Query: "shared keyword", K: 10, Topics: []string{"scope/one"}})
	if err != nil || len(out.Results) != 1 || out.Results[0].Topics[0] != "scope/one" {
		t.Fatalf("topic scope: %v %+v", err, out.Results)
	}
	out, err = d.Search(daemon.SearchOptions{Query: "shared keyword", K: 10, Sender: "agent-b"})
	if err != nil || len(out.Results) != 1 || out.Results[0].Sender != "agent-b" {
		t.Fatalf("sender scope: %v %+v", err, out.Results)
	}
	if _, err := d.Search(daemon.SearchOptions{Query: "x", Topics: []string{"never-created"}}); err == nil ||
		!strings.Contains(err.Error(), "never-created") {
		t.Fatalf("nonexistent scope topic should refuse pre-ack, got %v", err)
	}
}

// D4: a whole conversation expands from the thread id (= root message id),
// root included, in order.
func TestThreadExpansion(t *testing.T) {
	d := retrDaemon(t)
	root, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "thread root question"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"first answer", "second answer"} {
		if _, err := d.Publish(daemon.PublishRequest{
			Actor: "agent-b", Body: body, ReplyToMessageID: root.MessageID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := d.Thread(root.MessageID, 4000, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Included != 3 || out.Omitted != 0 {
		t.Fatalf("thread included %d omitted %d, want 3/0", out.Included, out.Omitted)
	}
	pl := out.Payload
	iRoot := strings.Index(pl, "thread root question")
	iFirst := strings.Index(pl, "first answer")
	iSecond := strings.Index(pl, "second answer")
	if iRoot < 0 || iFirst < 0 || iSecond < 0 || !(iRoot < iFirst && iFirst < iSecond) {
		t.Fatalf("thread order/content wrong:\n%s", pl)
	}
	if !strings.Contains(pl, config.QuotePrefix) {
		t.Fatal("thread bodies must be quoted (untrusted content)")
	}

	if _, err := d.Thread("0190a1b2-c3d4-7e5f-8901-000000000000", 1000, ""); err == nil {
		t.Fatal("unknown thread should error")
	}
}

// D5: the taxonomy is browsable with live counts.
func TestTopicList(t *testing.T) {
	d := retrDaemon(t)
	for i, topic := range []string{"list/alpha", "list/beta", "list/alpha"} {
		if _, err := d.Publish(daemon.PublishRequest{
			Actor: "operator", Body: strings.Repeat("body ", i+1),
			Topics: []string{topic}, AutoCreateTopics: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	topics, err := d.Projection().TopicList()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, ti := range topics {
		got[ti.Name] = ti.Messages
	}
	if got["list/alpha"] != 2 || got["list/beta"] != 1 {
		t.Fatalf("topic counts wrong: %v", got)
	}
}
