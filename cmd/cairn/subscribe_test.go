package main

// N3 CLI level: R25 — only --durable creates events; the default is the
// LOCAL view config. Durable subscription ops are admin-capability.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestN3SubscribeLocalVsDurable(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	seq := func() float64 {
		resp, err := daemon.Call(loaded.DeviceDir, daemon.Request{Op: "status"})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Status["next_seq"].(float64)
	}

	// default = session tier: view.json written, NO events (R25)
	base := seq()
	out, err := runCLI(t, "subscribe", "coffee equipment maintenance", "--view", "workshop", "--dir", dir)
	if err != nil {
		t.Fatalf("local subscribe: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no events") {
		t.Fatalf("local subscribe output does not state the tier:\n%s", out)
	}
	blob, err := os.ReadFile(filepath.Join(dir, "views", "workshop", "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg daemon.ViewConfig
	json.Unmarshal(blob, &cfg)
	if cfg.InterestQuery != "coffee equipment maintenance" {
		t.Fatalf("view.json not written: %+v", cfg)
	}
	if got := seq(); got != base {
		t.Fatalf("local subscribe appended events: next_seq %v → %v", base, got)
	}

	// --durable creates subscription.create and lists
	out, err = runCLI(t, "subscribe", "council planning approval", "--view", "workshop", "--durable", "--dir", dir)
	if err != nil {
		t.Fatalf("durable subscribe: %v\n%s", err, out)
	}
	if seq() == base {
		t.Fatal("durable subscribe appended nothing")
	}
	var res daemon.SubscribeResult
	json.Unmarshal([]byte(out), &res)
	if res.SubscriptionID == "" {
		t.Fatalf("no subscription id:\n%s", out)
	}
	out, err = runCLI(t, "subscription", "list", "--dir", dir)
	if err != nil || !strings.Contains(out, res.SubscriptionID) {
		t.Fatalf("subscription list: %v\n%s", err, out)
	}

	// durable subscribe with an unknown topic rejects pre-ack
	if _, err := runCLI(t, "subscribe", "q", "--view", "workshop", "--durable",
		"--topic", "never-made", "--dir", dir); err == nil || !strings.Contains(err.Error(), "before ack") {
		t.Fatalf("unknown topic accepted: %v", err)
	}

	// disable via CLI
	if out, err := runCLI(t, "subscription", "disable", res.SubscriptionID, "--dir", dir); err != nil {
		t.Fatalf("disable: %v\n%s", err, out)
	}

	// N2 interlock: durable subscriptions are admin — agent-standard refused
	resp, err := daemon.Call(loaded.DeviceDir, daemon.Request{
		Op: "session-create", SessionProfile: "agent-standard", SessionName: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	token := resp.Status["session"].(string)
	_, err = daemon.Call(loaded.DeviceDir, daemon.Request{Op: "subscribe-durable", Session: token,
		Subscribe: &daemon.SubscribeRequest{OwnerView: "agent", InterestQuery: "escalate me"}})
	if err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("agent-standard created a durable subscription: %v", err)
	}
}

// F9: --embedder real refuses to run without the venv (a fake "real" score
// is worse than none); --embedder dev remains the default path.
func TestF9BenchEmbedderFlag(t *testing.T) {
	dir := setupEnv(t)
	t.Setenv("CAIRN_EMBED_PYTHON", "") // ensure no ambient interpreter
	if _, err := runCLI(t, "bench", "golden", "--embedder", "real", "--dir", dir); err == nil ||
		!strings.Contains(err.Error(), "embed venv") {
		t.Fatalf("real without venv should refuse with instructions: %v", err)
	}
	if _, err := runCLI(t, "bench", "golden", "--embedder", "nonsense", "--dir", dir); err == nil {
		t.Fatal("bad embedder kind accepted")
	}
}

// N4 CLI plumbing: --attach reads and ships the file; --summary lands as
// the sender claim; derivative/summary verbs round-trip.
func TestN4SendAttachAndSummaryCLI(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)

	attach := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(attach, []byte("hydraulic pump rebuild procedure"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "send", "see attachment", "--attach", attach,
		"--summary", "pump rebuild notes", "--dir", dir)
	if err != nil {
		t.Fatalf("send --attach: %v\n%s", err, out)
	}
	var pub daemon.PublishResult
	json.Unmarshal([]byte(out), &pub)

	out, err = runCLI(t, "derivative", "summary", pub.MessageID, "--dir", dir)
	if err != nil || !strings.Contains(out, "pump rebuild notes") {
		t.Fatalf("summary-show: %v\n%s", err, out)
	}
	if out, err = runCLI(t, "derivative", "list", pub.MessageID, "--dir", dir); err != nil {
		t.Fatalf("derivative list: %v\n%s", err, out)
	}
}

// FIX-A3 regression: `cairn subscribe` without --topic must PRESERVE the
// view's operator-set hard topic filters. The old implementation bypassed
// the daemon and rebuilt view.json from scratch, erasing them (and racing
// the daemon's reader).
func TestSubscribeLocalPreservesOperatorTopics(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)

	if out, err := runCLI(t, "subscribe", "first interest", "--view", "workshop",
		"--topic", "alpha", "--topic", "beta", "--dir", dir); err != nil {
		t.Fatalf("seed with topics: %v\n%s", err, out)
	}

	// Re-tune the interest WITHOUT --topic: topics must survive.
	if out, err := runCLI(t, "subscribe", "second interest", "--view", "workshop", "--dir", dir); err != nil {
		t.Fatalf("re-subscribe: %v\n%s", err, out)
	}

	blob, err := os.ReadFile(filepath.Join(dir, "views", "workshop", "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg daemon.ViewConfig
	json.Unmarshal(blob, &cfg)
	if cfg.InterestQuery != "second interest" {
		t.Fatalf("interest not updated: %+v", cfg)
	}
	if len(cfg.Topics) != 2 || cfg.Topics[0] != "alpha" || cfg.Topics[1] != "beta" {
		t.Fatalf("operator topics erased by topic-less subscribe: %+v", cfg)
	}

	// An explicit --topic still replaces.
	if out, err := runCLI(t, "subscribe", "third", "--view", "workshop", "--topic", "gamma", "--dir", dir); err != nil {
		t.Fatalf("replace topics: %v\n%s", err, out)
	}
	blob, _ = os.ReadFile(filepath.Join(dir, "views", "workshop", "view.json"))
	cfg = daemon.ViewConfig{}
	json.Unmarshal(blob, &cfg)
	if len(cfg.Topics) != 1 || cfg.Topics[0] != "gamma" {
		t.Fatalf("explicit --topic did not replace: %+v", cfg)
	}
}
