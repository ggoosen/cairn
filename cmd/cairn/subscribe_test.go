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
