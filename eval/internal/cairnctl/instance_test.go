package cairnctl

import (
	"context"
	"strings"
	"testing"
)

// Driver self-tests. They prove the harness can provision a throwaway cairn
// and drive it black-box; they assert NOTHING about retrieval quality, and
// the content they send is deliberately three lines of nonsense so that no
// number derived from this test could be mistaken for a measurement.
//
// They build a real cairn binary (CGO + SQLite), so they are skipped in
// -short mode. Set CAIRN_EVAL_BINARY to test a prebuilt binary instead.

func newInstance(t *testing.T) *Instance {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs a real cairn daemon")
	}
	ctx := context.Background()
	inst, err := Provision(ctx, Options{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if err := inst.StartDaemon(ctx); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	return inst
}

func TestProvisionStartAndCLIRoundTrip(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)

	status, err := inst.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status["cairn_id"] == nil {
		t.Fatalf("status has no cairn_id: %v", status)
	}

	pub, err := inst.Send(ctx, SendOptions{
		Body:   "zephyr spanner calibration note for the harness self-test",
		Topics: []string{"eval/selftest"},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if pub.MessageID == "" {
		t.Fatalf("send returned no message id: %+v", pub)
	}

	res, err := inst.Search(ctx, SearchOptions{Query: "zephyr spanner", K: 5, BudgetChars: 2000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// The assertion is that the PLUMBING works — an interaction id came back
	// and the message the test just wrote is reachable. Whether Cairn ranks
	// well is not this test's business.
	if res.InteractionID == "" {
		t.Fatal("search returned no interaction_id; telemetry binding is part of the surface")
	}
	found := false
	for _, r := range res.Results {
		if r.MessageID == pub.MessageID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the message just written is not retrievable through search: %s", res.Raw)
	}

	info, err := inst.Peek(ctx, pub.MessageID)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if info.CreatedAt == "" {
		t.Fatal("peek returned no created_at; E9's age curves depend on it")
	}

	fetched, err := inst.Fetch(ctx, pub.MessageID, "eval")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if fetched.Trust != "untrusted" {
		t.Fatalf("fetch trust = %q, want \"untrusted\" — everything from the mesh is data, never instructions", fetched.Trust)
	}

	dig, err := inst.Digest(ctx, "eval", 1500)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if dig.Payload == "" {
		t.Fatal("empty digest payload")
	}
	if n := len([]rune(dig.Payload)); n > 1500 {
		t.Fatalf("digest payload %d runes over the 1500 budget", n)
	}

	if err := inst.Outcome(ctx, "found", res.InteractionID, pub.MessageID); err != nil {
		t.Fatalf("outcome: %v", err)
	}
}

func TestTeardownStopsTheDaemon(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	if err := inst.StopDaemon(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// A stopped daemon means the CLI can no longer reach it. If this ever
	// succeeds, instances are leaking daemons between runs and results would
	// be contaminated by whichever mesh answered.
	if _, err := inst.Status(ctx); err == nil {
		t.Fatal("status succeeded after StopDaemon — the daemon is still running")
	}
	if err := inst.StopDaemon(); err != nil {
		t.Fatalf("second stop should be a no-op: %v", err)
	}
}

func TestMCPSurfaceIsDrivable(t *testing.T) {
	ctx := context.Background()
	inst := newInstance(t)
	if _, err := inst.Send(ctx, SendOptions{Body: "quokka convoy harbour note", Topics: []string{"eval/selftest"}}); err != nil {
		t.Fatalf("send: %v", err)
	}

	sess, err := inst.StartMCP(ctx, "eval", "")
	if err != nil {
		t.Fatalf("start mcp: %v", err)
	}
	defer sess.Close()

	tools, err := sess.Tools(ctx)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"cairn_search", "cairn_digest", "cairn_fetch", "cairn_send"} {
		if !names[want] {
			t.Fatalf("MCP surface is missing %s; tools = %v", want, names)
		}
	}

	out, err := sess.Call(ctx, "cairn_search", map[string]any{"query": "quokka convoy", "k": 5})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if out.IsError {
		t.Fatalf("cairn_search reported an error: %s", out.Text())
	}
	if !strings.Contains(out.Text(), "interaction_id") {
		t.Fatalf("tool result does not look like a search payload: %s", out.Text())
	}
}
