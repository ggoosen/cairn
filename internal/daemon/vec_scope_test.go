package daemon_test

// D1 acceptance, daemon side: a performance change to the vector path must not
// become a capability leak.
//
// The hazard is specific. Before D1 the vector candidates were the whole
// corpus, filtered in Go after the fact; now the top-K is computed by SQL. If
// the scope did not travel INTO that query, a confined session — or a plain
// `search --topic` — would rank against messages it may not see. So these
// tests drive a REAL daemon with a REAL embedder (so the vector half actually
// runs), through the REAL IPC surface for the confined case, and assert the
// grant holds on a corpus where the semantic signal points straight at the
// forbidden message.

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
)

// TestD1VectorPathRespectsTopicScope: `search --topic` narrows the vector
// candidates too, not just the lexical ones.
func TestD1VectorPathRespectsTopicScope(t *testing.T) {
	d := retrDaemon(t)
	mk := func(body string, topic string) string {
		t.Helper()
		out, err := d.Publish(daemon.PublishRequest{
			Actor: "operator", Body: body, Topics: []string{topic}, AutoCreateTopics: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out.MessageID
	}
	// Near-identical bodies in two topics: lexically and semantically the two
	// are all but indistinguishable, so only the SCOPE can separate them.
	inScope := mk("the reticulation controller wakes the pump at dawn", "garden/water")
	outScope := mk("the reticulation controller wakes the pump at dusk", "office/admin")
	if _, err := d.EnrichOnce(64); err != nil {
		t.Fatal(err)
	}
	if rs := d.RetrievalStatus(); rs.Mode != "hybrid" {
		t.Fatalf("the embedder did not engage — this test would prove nothing (%+v)", rs)
	}

	out, err := d.Search(daemon.SearchOptions{Query: "reticulation controller pump", K: 20,
		Topics: []string{"garden/water"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.RetrievalMode != "full" {
		t.Fatalf("scoped search degraded to %q — the vector half must still run", out.RetrievalMode)
	}
	var sawIn, sawOut bool
	for _, r := range out.Results {
		sawIn = sawIn || r.MessageID == inScope
		sawOut = sawOut || r.MessageID == outScope
	}
	if !sawIn {
		t.Fatal("the in-scope message did not survive its own scope")
	}
	if sawOut {
		t.Fatal("a message outside --topic reached the results — the scope did not bind on the vector path")
	}
}

// TestD1VectorPathRespectsCapabilityGrant: the D3 grant binds on the vector
// candidates as well, over real IPC under a real minted handle.
func TestD1VectorPathRespectsCapabilityGrant(t *testing.T) {
	dir := initCairn(t)
	callD := serveSessionEmbedded(t, dir)
	pub := func(body, topic string) string {
		t.Helper()
		resp, err := callD(daemon.Request{Op: "publish", Publish: &daemon.PublishRequest{
			Actor: "operator", Body: body, Topics: []string{topic}, AutoCreateTopics: true,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Publish.MessageID
	}
	granted := pub("the reticulation controller wakes the pump at dawn", "a/b")
	forbidden := pub("the reticulation controller wakes the pump at dusk", "z")
	if _, err := callD(daemon.Request{Op: "reindex-semantic"}); err != nil {
		t.Fatalf("semantic reindex: %v", err)
	}

	token := mkConfined(t, callD, daemon.Selectors{Topics: []string{"a/*"}})
	resp, err := callD(daemon.Request{Op: "search", Session: token,
		Search2: &daemon.SearchOptions{Query: "reticulation controller pump", K: 20}})
	if err != nil {
		t.Fatalf("confined search: %v", err)
	}
	if resp.Search.RetrievalMode != "full" {
		t.Fatalf("confined search ran %q, not a hybrid one — the vector half must be exercised here", resp.Search.RetrievalMode)
	}
	got := searchIDs(t, resp)
	if !got[granted] {
		t.Fatal("the granted message did not survive its own grant")
	}
	if got[forbidden] {
		t.Fatal("a message outside the grant reached a confined session through the vector path")
	}
	if strings.Contains(resp.Search.Payload, "at dusk") {
		t.Fatalf("forbidden CONTENT reached a confined session:\n%s", resp.Search.Payload)
	}
}

// TestD1StatusNamesTheVectorPath: which path answers is an operator fact.
func TestD1StatusNamesTheVectorPath(t *testing.T) {
	dir := initCairn(t)
	callD := serveSessionEmbedded(t, dir)
	resp, err := callD(daemon.Request{Op: "status"})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := resp.Status["vector_path"].(string)
	if path != "vec0" && path != "brute_force" {
		t.Fatalf("status does not name the vector path: %v", resp.Status["vector_path"])
	}
	if detail, _ := resp.Status["vector_path_detail"].(string); detail == "" {
		t.Fatal("status names the path but not the reason — an operator cannot act on that")
	}
}

func serveSessionEmbedded(t *testing.T, dir string) func(daemon.Request) (*daemon.Response, error) {
	t.Helper()
	return serveSessionWith(t, daemon.Options{Dir: dir, Embedder: embed.BagOfWords{}})
}
