package main

// F3: the deepened doctor fails loudly (nonzero) on missing objects,
// parked events, and drift — and stays clean on healthy meshes including
// post-migrate (both auditors' false-clean scenarios).

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestF3DoctorFailsOnMissingObject(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	// fresh mesh: clean, with the no-projection note (informational)
	out, err := runCLI(t, "doctor", "--dir", dir)
	if err != nil {
		t.Fatalf("doctor on fresh mesh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "deep doctor: clean") {
		t.Fatalf("no deep-doctor line:\n%s", out)
	}

	// publish a canonical message, then delete its object (CAIRN-05)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	waitForSocket(t, dir)
	sout, err := runCLI(t, "send", "canonical body that will vanish", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	var pub daemon.PublishResult
	json.Unmarshal([]byte(sout), &pub)
	cancel()
	d.Close()

	if err := os.Remove(filepath.Join(dir, "objects", pub.BodyHash[:2], pub.BodyHash[2:])); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, "doctor", "--dir", dir)
	if err == nil {
		t.Fatalf("doctor CLEAN with a missing canonical object (the auditor's false-clean):\n%s", out)
	}
	if !strings.Contains(out, pub.BodyHash) {
		t.Fatalf("problem does not name the object:\n%s", out)
	}
	// FIX-F8.5: the line also names the referencing revision and message
	if !strings.Contains(out, pub.RevisionID) || !strings.Contains(out, pub.MessageID) {
		t.Fatalf("problem does not name the referencing revision/message:\n%s", out)
	}
}

// TestF3DoctorFailsOnParkedEvent: a TERMINAL parked event (genuine corruption a
// replay can never heal — here a revise_body with no revisions) FAILS doctor.
// R49 distinguishes this from a RETRYABLE park (a missing intra-mesh reference),
// which is informational within the grace window — asserted separately below.
func TestF3DoctorFailsOnParkedEvent(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// terminal: revise_body carrying no revisions can never project, on any
	// arrival order — not a FOREIGN KEY / missing-reference failure.
	if _, err := d.SimpleEventUnvalidatedForTest("message.revise_body", "message",
		"0190a1b2-c3d4-7e5f-8901-00000000dead",
		map[string]any{"message_id": "0190a1b2-c3d4-7e5f-8901-00000000beef", "revisions": []any{}}); err != nil {
		t.Fatal(err)
	}
	d.Close()

	out, err := runCLI(t, "doctor", "--dir", dir)
	if err == nil {
		t.Fatalf("doctor CLEAN with a TERMINAL parked event:\n%s", out)
	}
	if !strings.Contains(out, "parked") {
		t.Fatalf("problem does not mention parking:\n%s", out)
	}
}

// TestR49RetryableParkIsCleanWithinGrace: a FK-failing topic.link.add (a missing
// intra-mesh reference — the R49 class) is a RETRYABLE park; doctor exits 0
// within the grace window and surfaces it as a note (the dependency may still
// arrive during active sync), never a red gate on a transient ordering gap.
func TestR49RetryableParkIsCleanWithinGrace(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SimpleEventUnvalidatedForTest("topic.link.add", "link",
		"0190a1b2-c3d4-7e5f-8901-00000000dead",
		map[string]any{"link_id": "0190a1b2-c3d4-7e5f-8901-00000000dead",
			"message_id": "0190a1b2-c3d4-7e5f-8901-00000000beef", "topic_id": "bogus"}); err != nil {
		t.Fatal(err)
	}
	d.Close()

	out, err := runCLI(t, "doctor", "--dir", dir)
	if err != nil {
		t.Fatalf("doctor FAILED on a retryable park within grace (should be informational):\n%s", out)
	}
	if !strings.Contains(out, "retryable parked") {
		t.Fatalf("doctor did not surface the retryable park as a note:\n%s", out)
	}
}

func TestF3DoctorCleanPostMigrate(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "pre-migrate content"}); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := identity.Migrate(identity.MigrateOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "doctor", "--dir", dir)
	if err != nil {
		t.Fatalf("doctor failed on healthy migrated mesh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "deep doctor: clean") {
		t.Fatalf("no clean line:\n%s", out)
	}
	// identity show also green post-migrate (F2 ruling 2, CLI level)
	out, err = runCLI(t, "identity", "show", "--dir", dir)
	if err != nil || !strings.Contains(out, "genesis event:") {
		t.Fatalf("identity show post-migrate: %v\n%s", err, out)
	}
}

// F8.2: `cairn bench golden` reproduces the retrieval claim from the binary.
func TestF8BenchGoldenCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("bench loads 184 messages")
	}
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	out, err := runCLI(t, "bench", "golden")
	if err != nil {
		t.Fatalf("bench golden: %v\n%s", err, out)
	}
	for _, want := range []string{"Success@5", "lexical-only top-10", "PASS"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}
