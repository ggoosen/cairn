package cairnctl

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The harness must be able to age a mesh from OUTSIDE the process — it
// cannot reach daemon.Options.Now, by design. This drives the whole path:
// harness Clock → environment → the daemon's testhooks clock hook → an
// event's created_at, read back through the black-box CLI.
//
// It is a plumbing test. It measures nothing about recall; E9's curves are
// what this capability exists for, and they have not been run.
func TestSimulatedClockReachesEventTimestamps(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a real cairn daemon")
	}
	ctx := context.Background()
	const age = 90 * 24 * time.Hour

	inst, err := Provision(ctx, Options{Root: t.TempDir(), Clock: Clock{Offset: -age}})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if err := inst.StartDaemon(ctx); err != nil {
		t.Fatalf("start daemon: %v", err)
	}

	if !strings.Contains(inst.DaemonStderr(), SimulatedClockWarning) {
		t.Fatalf("daemon did not announce a simulated clock; the hook may not be compiled in (tags %q). stderr:\n%s",
			inst.Bin.Tags, inst.DaemonStderr())
	}

	pub, err := inst.Send(ctx, SendOptions{Body: "aged corpus item", Topics: []string{"eval/selftest"}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	info, err := inst.Peek(ctx, pub.MessageID)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	created, err := time.Parse(time.RFC3339, info.CreatedAt)
	if err != nil {
		t.Fatalf("parsing created_at %q: %v", info.CreatedAt, err)
	}
	got := time.Since(created)
	if got < age-time.Hour || got > age+time.Hour {
		t.Fatalf("message aged %v, want ~%v (created_at %s)", got, age, info.CreatedAt)
	}
}

func TestClockEnvEncoding(t *testing.T) {
	if env := (Clock{}).env(); env != nil {
		t.Fatalf("zero clock produced %v; real time must need no environment", env)
	}
	if env := (Clock{Offset: -48 * time.Hour}).env(); len(env) != 1 || !strings.HasPrefix(env[0], fakeClockOffsetEnv+"=") {
		t.Fatalf("offset clock encoded as %v", env)
	}
	anchor := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if env := (Clock{Anchor: anchor}).env(); len(env) != 1 || env[0] != fakeClockAnchorEnv+"=2026-01-02T03:04:05Z" {
		t.Fatalf("anchor clock encoded as %v", env)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("setting both Offset and Anchor must panic at the call site that made the mistake")
		}
	}()
	_ = (Clock{Offset: time.Hour, Anchor: anchor}).env()
}
