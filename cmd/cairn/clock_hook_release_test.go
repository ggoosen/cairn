package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The negative half of the EVAL-PLAN §9.2 clock hook: a RELEASE binary must
// contain no way to be told what time it is.
//
// The precedent is FIX-F4's `make verify` guard — a property that only holds
// in a build the test suite itself never uses has to be asserted by BUILDING
// that other build. Here that means compiling cmd/cairn with the release tag
// set (`sqlite_fts5`, no `cairn_testhooks`) and checking two things:
//
//  1. the environment variable NAMES are absent from the binary's bytes —
//     the constants live only in clock_testhook.go, so their presence would
//     mean the hook was compiled in;
//  2. running that binary with the variables set produces REAL timestamps.
//
// (1) alone could be defeated by a rename; (2) alone could pass because the
// value was malformed. Together they are hard to fool by accident, which is
// the failure mode worth defending against — a deliberate attacker with
// local code execution has better options than lying about the clock (R22).
func TestReleaseBuildHasNoClockHook(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a release binary")
	}
	bin := filepath.Join(t.TempDir(), "cairn-release")
	build := exec.Command("go", "build", "-tags", "sqlite_fts5", "-o", bin, ".")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the release binary: %v\n%s", err, out)
	}

	blob, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CAIRN_FAKE_CLOCK_OFFSET", "CAIRN_FAKE_CLOCK"} {
		if bytes.Contains(blob, []byte(name)) {
			t.Fatalf("release binary contains %q — the clock hook must not ship", name)
		}
	}

	// Behavioural half: the same binary, told to move its clock a year back,
	// must timestamp an event with the real time.
	dir := filepath.Join(t.TempDir(), "cairn")
	env := append(os.Environ(),
		"CAIRN_DEVICE_STATE_DIR="+t.TempDir(),
		"CAIRN_FAKE_CLOCK_OFFSET=-8760h",
		"CAIRN_FAKE_CLOCK=2000-01-01T00:00:00Z",
	)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	// --allow-unencrypted, not the volume test hook: this binary has no test
	// hooks at all, which is the whole point.
	run("init", "--dir", dir, "--allow-unencrypted", "--sync-listen", "off")

	daemonCmd := exec.Command(bin, "daemon", "--dir", dir)
	daemonCmd.Env = env
	var daemonOut bytes.Buffer
	daemonCmd.Stdout, daemonCmd.Stderr = &daemonOut, &daemonOut
	if err := daemonCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = daemonCmd.Process.Kill()
		_ = daemonCmd.Wait()
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		cmd := exec.Command(bin, "status", "--dir", dir, "--json")
		cmd.Env = env
		if err := cmd.Run(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("release daemon never became ready:\n%s", daemonOut.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	var pub struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal([]byte(run("send", "clock probe", "--dir", dir)), &pub); err != nil {
		t.Fatalf("parsing send output: %v", err)
	}
	var info struct {
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(run("peek", pub.MessageID, "--dir", dir)), &info); err != nil {
		t.Fatalf("parsing peek output: %v", err)
	}
	created, err := time.Parse(time.RFC3339, info.CreatedAt)
	if err != nil {
		t.Fatalf("parsing created_at %q: %v", info.CreatedAt, err)
	}
	if skew := time.Since(created); skew > time.Hour || skew < -time.Hour {
		t.Fatalf("release binary timestamped an event %v away from now (%s) — the environment moved its clock",
			skew, info.CreatedAt)
	}
	if strings.Contains(daemonOut.String(), "SIMULATED CLOCK") {
		t.Fatalf("release daemon announced a simulated clock:\n%s", daemonOut.String())
	}
}
