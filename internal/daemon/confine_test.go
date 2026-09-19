package daemon_test

// D3 acceptance: capability resource selectors (BUILD-PLAN §4 D3; spec §7.2).
//
// Everything here goes through the REAL IPC surface against a REAL daemon,
// under a REAL minted handle — the enforcement point is the dispatch boundary,
// so a test that called the daemon methods directly would be testing nothing.
//
// The load-bearing assertions:
//   - a grant of topic="a/*" admits the subtree and nothing else, including via
//     `thread`, which crosses topics by construction;
//   - every refusal is TYPED (Refused.Code) — an agent must be able to tell
//     "nothing matched" from "you may not ask";
//   - a budget cap is reported in the response, never applied silently;
//   - an unconfined session is unchanged.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ggoosen/cairn/internal/daemon"
)

type confineFixture struct {
	call    func(daemon.Request) (*daemon.Response, error)
	inScope string // message in a/b
	deep    string // message in a/b/c (subtree depth)
	parent  string // message in the bare parent topic "a"
	outside string // message in "z"
	bare    string // message with no topic at all
}

func setupConfineFixture(t *testing.T) confineFixture {
	t.Helper()
	dir := initCairn(t)
	callD := serveSession(t, dir, nil)

	pub := func(body string, topics []string, recipients []string) string {
		t.Helper()
		resp, err := callD(daemon.Request{Op: "publish", Publish: &daemon.PublishRequest{
			Actor: "operator", Body: body, Topics: topics,
			Recipients: recipients, AutoCreateTopics: true,
		}})
		if err != nil {
			t.Fatalf("publish %q: %v", body, err)
		}
		return resp.Publish.MessageID
	}
	f := confineFixture{call: callD}
	f.inScope = pub("scoped note about widgets", []string{"a/b"}, nil)
	f.deep = pub("deeper note about widgets", []string{"a/b/c"}, nil)
	f.parent = pub("parent note about widgets", []string{"a"}, nil)
	// the out-of-scope message is also addressed to the confined view, so the
	// digest's MANDATORY path (which is exempt from the view's own filters) is
	// exercised rather than assumed away
	f.outside = pub("forbidden note about widgets", []string{"z"}, []string{"narrow"})
	f.bare = pub("untopiced note about widgets", nil, nil)
	return f
}

func mkConfined(t *testing.T, callD func(daemon.Request) (*daemon.Response, error), sel daemon.Selectors) string {
	t.Helper()
	resp, err := callD(daemon.Request{
		Op: "session-create", SessionProfile: "agent-standard", SessionName: "narrow",
		SessionSelectors: &sel,
	})
	if err != nil {
		t.Fatalf("session-create with selectors %+v: %v", sel, err)
	}
	return resp.Status["session"].(string)
}

// wantRefusal asserts a TYPED refusal rather than an error-shaped anything.
func wantRefusal(t *testing.T, what string, resp *daemon.Response, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected a refusal, got success", what)
	}
	if resp == nil || resp.Refused == nil {
		t.Fatalf("%s: refused with an untyped error %q — an agent cannot tell this from a failure", what, err)
	}
	if resp.Refused.Code != daemon.RefusalOutOfScope {
		t.Fatalf("%s: refusal code %q, want %q", what, resp.Refused.Code, daemon.RefusalOutOfScope)
	}
	if len(resp.Refused.TopicGrant) == 0 {
		t.Fatalf("%s: refusal does not name the grant it was measured against", what)
	}
}

func searchIDs(t *testing.T, resp *daemon.Response) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	if resp.Search == nil {
		return out
	}
	for _, r := range resp.Search.Results {
		out[r.MessageID] = true
	}
	return out
}

func TestD3TopicGrantConfinesRetrieval(t *testing.T) {
	f := setupConfineFixture(t)
	token := mkConfined(t, f.call, daemon.Selectors{Topics: []string{"a/*"}})

	// --- search: the subtree, and only the subtree ------------------------
	resp, err := f.call(daemon.Request{Op: "search", Session: token,
		Search2: &daemon.SearchOptions{Query: "widgets", K: 50}})
	if err != nil {
		t.Fatalf("confined search: %v", err)
	}
	got := searchIDs(t, resp)
	if !got[f.inScope] || !got[f.deep] {
		t.Fatalf("grant a/* did not admit its own subtree (a/b=%v a/b/c=%v)", got[f.inScope], got[f.deep])
	}
	if got[f.outside] || got[f.bare] {
		t.Fatalf("grant a/* leaked outside itself (z=%v untopiced=%v)", got[f.outside], got[f.bare])
	}
	if got[f.parent] {
		t.Fatal("grant a/* admitted the BARE PARENT topic a — the glob does not match it, and widening a written grant is not the daemon's call")
	}
	if resp.Capability == nil || len(resp.Capability.TopicGrant) == 0 {
		t.Fatal("a confined search did not report its confinement — a scoped result must not look like the whole mesh")
	}

	// --- an out-of-scope scope request is REFUSED, not silently narrowed ---
	resp, err = f.call(daemon.Request{Op: "search", Session: token,
		Search2: &daemon.SearchOptions{Query: "widgets", Topics: []string{"z"}}})
	wantRefusal(t, "search --topic z", resp, err)

	// an in-scope scope request still works
	if _, err := f.call(daemon.Request{Op: "search", Session: token,
		Search2: &daemon.SearchOptions{Query: "widgets", Topics: []string{"a/b"}}}); err != nil {
		t.Fatalf("in-scope scoped search refused: %v", err)
	}

	// --- fetch / peek: one named message ----------------------------------
	if _, err := f.call(daemon.Request{Op: "fetch", Session: token, MessageID: f.inScope, AgentView: "narrow"}); err != nil {
		t.Fatalf("in-scope fetch refused: %v", err)
	}
	resp, err = f.call(daemon.Request{Op: "fetch", Session: token, MessageID: f.outside, AgentView: "narrow"})
	wantRefusal(t, "fetch out-of-scope", resp, err)
	resp, err = f.call(daemon.Request{Op: "peek", Session: token, MessageID: f.outside})
	wantRefusal(t, "peek out-of-scope", resp, err)
	resp, err = f.call(daemon.Request{Op: "peek", Session: token, MessageID: f.bare})
	wantRefusal(t, "peek untopiced", resp, err)

	// --- digest: mandatory items do NOT walk around the grant -------------
	resp, err = f.call(daemon.Request{Op: "digest", Session: token, AgentView: "narrow", BudgetChars: 4000})
	if err != nil {
		t.Fatalf("confined digest: %v", err)
	}
	if strings.Contains(resp.Digest.Payload, f.outside) {
		t.Fatalf("the digest included an out-of-scope message because it was MANDATORY (explicit recipient):\n%s", resp.Digest.Payload)
	}
	if !strings.Contains(resp.Digest.Payload, f.inScope) {
		t.Fatalf("the digest dropped an in-scope message:\n%s", resp.Digest.Payload)
	}

	// --- topic-list is filtered (a list, not a refusal) -------------------
	resp, err = f.call(daemon.Request{Op: "topic-list", Session: token})
	if err != nil {
		t.Fatalf("confined topic-list: %v", err)
	}
	for _, tp := range resp.Topics {
		if tp.Name == "z" || tp.Name == "a" {
			t.Fatalf("topic-list leaked %q past the grant", tp.Name)
		}
	}

	// --- an unclassified / whole-mesh op is refused, typed ----------------
	resp, err = f.call(daemon.Request{Op: "map", Session: token, AgentView: "narrow"})
	wantRefusal(t, "map under a topic grant", resp, err)
}

// TestD3ThreadCrossesTopicsAndIsStillConfined is the case the sprint singled
// out: a thread is assembled by thread_id, so it walks across topics by
// construction and would hand a confined session everything in the
// conversation if nothing filtered it per message.
func TestD3ThreadIsConfinedPerMessage(t *testing.T) {
	dir := initCairn(t)
	callD := serveSession(t, dir, nil)

	root, err := callD(daemon.Request{Op: "publish", Publish: &daemon.PublishRequest{
		Actor: "operator", Body: "thread root inside the grant", Topics: []string{"a/b"}, AutoCreateTopics: true}})
	if err != nil {
		t.Fatal(err)
	}
	// a reply filed under a DIFFERENT topic: same thread, outside the grant
	reply, err := callD(daemon.Request{Op: "publish", Publish: &daemon.PublishRequest{
		Actor: "operator", Body: "secret reply outside the grant", Topics: []string{"z"},
		ReplyToMessageID: root.Publish.MessageID, AutoCreateTopics: true}})
	if err != nil {
		t.Fatal(err)
	}
	// a thread wholly outside the grant
	lone, err := callD(daemon.Request{Op: "publish", Publish: &daemon.PublishRequest{
		Actor: "operator", Body: "wholly forbidden conversation", Topics: []string{"z"}, AutoCreateTopics: true}})
	if err != nil {
		t.Fatal(err)
	}
	token := mkConfined(t, callD, daemon.Selectors{Topics: []string{"a/*"}})

	resp, err := callD(daemon.Request{Op: "thread", Session: token, ThreadID: root.Publish.MessageID, BudgetChars: 4000})
	if err != nil {
		t.Fatalf("confined thread: %v", err)
	}
	if strings.Contains(resp.Thread.Payload, "secret reply") {
		t.Fatalf("thread leaked an out-of-scope message across topics:\n%s", resp.Thread.Payload)
	}
	if !strings.Contains(resp.Thread.Payload, "thread root") {
		t.Fatalf("thread dropped its in-scope message:\n%s", resp.Thread.Payload)
	}
	if resp.Thread.Withheld != 1 {
		t.Fatalf("thread withheld %d out-of-scope message(s), want 1 — silence about it is the same bug as leaking it", resp.Thread.Withheld)
	}
	if resp.Capability == nil || resp.Capability.Withheld != 1 {
		t.Fatalf("the withholding was not reported in the response: %+v", resp.Capability)
	}
	// the unconfined operator still sees the whole thread (no global change)
	full, err := callD(daemon.Request{Op: "thread", ThreadID: root.Publish.MessageID, BudgetChars: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full.Thread.Payload, "secret reply") || full.Thread.Withheld != 0 {
		t.Fatalf("an UNCONFINED thread lost content it should still see:\n%s", full.Thread.Payload)
	}
	_ = reply

	// a thread entirely outside the grant is a REFUSAL, not an empty render
	resp, err = callD(daemon.Request{Op: "thread", Session: token, ThreadID: lone.Publish.MessageID, BudgetChars: 4000})
	wantRefusal(t, "thread wholly out of scope", resp, err)
}

func TestD3BudgetCapIsClampedAndReported(t *testing.T) {
	f := setupConfineFixture(t)
	token := mkConfined(t, f.call, daemon.Selectors{MaxBudgetChars: 200})

	resp, err := f.call(daemon.Request{Op: "digest", Session: token, AgentView: "narrow", BudgetChars: 5000})
	if err != nil {
		t.Fatalf("confined digest: %v", err)
	}
	if n := utf8.RuneCountInString(resp.Digest.Payload); n > 200 {
		t.Fatalf("digest payload is %d chars, above the session cap of 200", n)
	}
	if resp.Capability == nil || !resp.Capability.BudgetClamped {
		t.Fatalf("the clamp was applied SILENTLY: %+v", resp.Capability)
	}
	if resp.Capability.BudgetRequested != 5000 || resp.Capability.BudgetGranted != 200 {
		t.Fatalf("clamp reported as %d → %d, want 5000 → 200", resp.Capability.BudgetRequested, resp.Capability.BudgetGranted)
	}

	// search's 0 means "unbudgeted", so it must be capped too — otherwise the
	// cap is bypassed by omitting the field.
	resp, err = f.call(daemon.Request{Op: "search", Session: token,
		Search2: &daemon.SearchOptions{Query: "widgets", K: 50}})
	if err != nil {
		t.Fatalf("confined search: %v", err)
	}
	if n := utf8.RuneCountInString(resp.Search.Payload); n > 200 {
		t.Fatalf("an omitted budget escaped the cap: payload is %d chars", n)
	}
	if resp.Capability == nil || !resp.Capability.BudgetClamped {
		t.Fatal("the omitted-budget clamp was not reported")
	}

	// a request already inside the cap is NOT reported as clamped
	resp, err = f.call(daemon.Request{Op: "digest", Session: token, AgentView: "narrow", BudgetChars: 150})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Capability != nil && resp.Capability.BudgetClamped {
		t.Fatal("a within-cap budget was reported as clamped")
	}
}

func TestD3WritesAreConfinedToo(t *testing.T) {
	f := setupConfineFixture(t)
	token := mkConfined(t, f.call, daemon.Selectors{Topics: []string{"a/*"}})

	if _, err := f.call(daemon.Request{Op: "publish", Session: token, Publish: &daemon.PublishRequest{
		Body: "a note filed inside the grant", Topics: []string{"a/b"}}}); err != nil {
		t.Fatalf("in-scope publish refused: %v", err)
	}
	resp, err := f.call(daemon.Request{Op: "publish", Session: token, Publish: &daemon.PublishRequest{
		Body: "a note filed outside the grant", Topics: []string{"z"}}})
	wantRefusal(t, "publish into z", resp, err)

	// an untopiced send would land outside the session's own scope
	resp, err = f.call(daemon.Request{Op: "publish", Session: token, Publish: &daemon.PublishRequest{
		Body: "a note filed nowhere"}})
	wantRefusal(t, "untopiced publish", resp, err)

	// replying to an out-of-scope message is refused (a reply inherits a thread)
	resp, err = f.call(daemon.Request{Op: "publish", Session: token, Publish: &daemon.PublishRequest{
		Body: "reply", Topics: []string{"a/b"}, ReplyToMessageID: f.outside}})
	wantRefusal(t, "reply to an out-of-scope message", resp, err)

	// signalling an out-of-scope message is refused
	resp, err = f.call(daemon.Request{Op: "signal", Session: token, MessageID: f.outside, Kind: "useful"})
	wantRefusal(t, "signal on an out-of-scope message", resp, err)
}

// TestD3EmptyGrantIsEmptyNotUnfiltered is the failure mode that would be worst
// and least visible: a grant matching NO existing topic must admit nothing. An
// absent filter admits everything, which is the opposite of the grant.
func TestD3GrantMatchingNoTopicAdmitsNothing(t *testing.T) {
	f := setupConfineFixture(t)
	token := mkConfined(t, f.call, daemon.Selectors{Topics: []string{"nosuchtree/*"}})

	resp, err := f.call(daemon.Request{Op: "search", Session: token,
		Search2: &daemon.SearchOptions{Query: "widgets", K: 50}})
	if err != nil {
		t.Fatalf("search under an empty grant errored instead of returning nothing: %v", err)
	}
	if n := len(resp.Search.Results); n != 0 {
		t.Fatalf("a grant matching no topic returned %d result(s) — the filter was dropped, not applied", n)
	}
	// This one IS legitimately an empty result: nothing is being withheld,
	// there is genuinely nothing there. The confinement is still reported.
	if resp.Capability == nil || len(resp.Capability.TopicGrant) == 0 {
		t.Fatal("an empty-grant search did not report that it was confined")
	}
	resp, err = f.call(daemon.Request{Op: "digest", Session: token, AgentView: "narrow", BudgetChars: 4000})
	if err != nil {
		t.Fatalf("digest under an empty grant: %v", err)
	}
	for _, id := range []string{f.inScope, f.outside, f.bare, f.parent} {
		if strings.Contains(resp.Digest.Payload, id) {
			t.Fatalf("digest under an empty grant still returned %s:\n%s", id, resp.Digest.Payload)
		}
	}
}

// TestD3UnconfinedSessionsAreUnchanged is the regression guard: selectors are
// OPTIONAL, and a handle without them must behave exactly as it did in P1.
func TestD3UnconfinedSessionUnchanged(t *testing.T) {
	f := setupConfineFixture(t)
	token := mkSession(t, f.call, "agent-standard", "wide")

	resp, err := f.call(daemon.Request{Op: "search", Session: token,
		Search2: &daemon.SearchOptions{Query: "widgets", K: 50}})
	if err != nil {
		t.Fatalf("unconfined search: %v", err)
	}
	got := searchIDs(t, resp)
	for _, id := range []string{f.inScope, f.deep, f.parent, f.outside, f.bare} {
		if !got[id] {
			t.Fatalf("an UNCONFINED session lost message %s — selectors must be opt-in", id)
		}
	}
	if resp.Capability != nil {
		t.Fatalf("an unconfined session was told it was confined: %+v", resp.Capability)
	}
	if _, err := f.call(daemon.Request{Op: "map", Session: token, AgentView: "wide"}); err != nil {
		t.Fatalf("an unconfined session was refused an op only confinement should block: %v", err)
	}
}

// TestD3GrantsStayPositive: there is no syntax for a negative selector, and
// there must not be — mutes are D7's open ruling (BUILD-PLAN §4/§8).
func TestD3GrantsArePositiveOnly(t *testing.T) {
	f := setupConfineFixture(t)
	for _, bad := range []string{"!a/*", "-a/*", "^a", "a/../b", "A/*", "a b"} {
		_, err := f.call(daemon.Request{
			Op: "session-create", SessionProfile: "agent-standard", SessionName: "narrow",
			SessionSelectors: &daemon.Selectors{Topics: []string{bad}},
		})
		if err == nil {
			t.Fatalf("selector %q was accepted — a negative or malformed grant must be refused at mint", bad)
		}
	}
	// the operator sees the grant when auditing handles
	token := mkConfined(t, f.call, daemon.Selectors{Topics: []string{"a/*"}, MaxBudgetChars: 500})
	_ = token
	resp, err := f.call(daemon.Request{Op: "session-list"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := resp.Status["sessions"].([]any)
	found := false
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["topic_grant"] != nil && row["max_budget_chars"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("`session list` does not show the grant: %+v", resp.Status["sessions"])
	}
}

// TestD3SessionOpsStayNonDelegable re-pins R23 against the new parameter: a
// confined session must not be able to mint itself a WIDER one.
func TestD3ConfinedSessionCannotMintAWiderOne(t *testing.T) {
	f := setupConfineFixture(t)
	token := mkConfined(t, f.call, daemon.Selectors{Topics: []string{"a/*"}})
	_, err := f.call(daemon.Request{Op: "session-create", Session: token,
		SessionProfile: "agent-standard", SessionName: "wider"})
	if err == nil || !strings.Contains(err.Error(), "non-delegable") {
		t.Fatalf("a confined session minted a handle: %v", err)
	}
}
