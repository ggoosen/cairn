package daemon_test

// N8 acceptance: live fork detection + repair (buildpack N8; spec §6.4; R33).
//
// Manufactured equivocation: clone device B's state after a common prefix, then
// have B and the clone (B2) write DIFFERENT events at the same next sequence —
// both validly signed by B's key. Node A, syncing both, detects the fork at
// the frontier (same next_seq, different head), freezes + quarantines the
// remote branch, surfaces it (doctor fork, gates FAIL), keeps other origins
// syncing (R33), and repairs it via the documented flow with BOTH branches
// preserved.

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/projection"
)

// copyTree recursively copies a directory (used to clone device state).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copyTree %s → %s: %v", src, dst, err)
	}
}

func TestN8ForkDetectionAndRepair(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	bOrigin := cairnlog.Origin{DeviceID: p.deviceB, Generation: 1}

	// A runs throughout.
	dA, cancelA, _ := startSyncNode(t, p.baseA, p.dirA, warnA)

	// --- B writes a common prefix (seq 1,2), then we clone it -----------------
	dB, cancelB, addrB := startSyncNode(t, p.baseB, p.dirB, warnB)
	if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "common event one about anchors"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "common event two about anchors"}); err != nil {
		t.Fatal(err)
	}
	cancelB()
	dB.Close()

	// clone B's portable data AND device state → B2 (same identity/key)
	dirB2 := filepath.Join(t.TempDir(), "cairnB2")
	baseB2 := t.TempDir()
	copyTree(t, p.dirB, dirB2)
	copyTree(t, p.baseB, baseB2)

	// --- B extends with branch A (seq 3 = A3); A pulls it ---------------------
	dB, cancelB, addrB = startSyncNode(t, p.baseB, p.dirB, warnB)
	if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "branch A three unique-aaa"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatalf("A pulls branch A: %v", err)
	}
	if searchCount(t, dA, "unique-aaa") != 1 {
		t.Fatal("A did not pull branch A")
	}
	cancelB()
	dB.Close()

	// --- B2 (the clone) extends with branch B (seq 3 = B3) --------------------
	warnB2 := &syncBuf{}
	dB2, cancelB2, addrB2 := startSyncNode(t, baseB2, dirB2, warnB2)
	if _, err := dB2.Publish(daemon.PublishRequest{Actor: "operator", Body: "branch B three unique-bbb"}); err != nil {
		t.Fatal(err)
	}

	// --- A syncs the clone → FORK detected at the frontier --------------------
	if _, err := dA.SyncWith(addrB2); err != nil {
		t.Fatalf("A sync with clone returned error (fork should be non-fatal, R33): %v\n%s", err, warnA.String())
	}
	forks := dA.Forks()
	if len(forks) != 1 || forks[0].Resolved {
		t.Fatalf("A did not freeze exactly one fork: %+v\nA warn:\n%s", forks, warnA.String())
	}
	f := forks[0]
	if f.OriginDevice != p.deviceB || f.DivergenceSeq != 3 {
		t.Fatalf("fork record wrong: origin=%s divergence=%d (want %s / 3)", f.OriginDevice, f.DivergenceSeq, p.deviceB)
	}
	if len(f.RemoteBranch) == 0 {
		t.Fatal("remote (losing) branch was not quarantined into the fork record")
	}
	// A did NOT ingest branch B into its log (still branch A at seq 3)
	if searchCount(t, dA, "unique-bbb") != 0 {
		t.Fatal("A ingested the forked branch instead of quarantining it")
	}
	if searchCount(t, dA, "unique-aaa") != 1 {
		t.Fatal("A lost its own branch")
	}
	// gates FAIL + deep doctor problem
	probs, _, err := daemon.DeepDoctor(fsx.OS{}, p.dirA, projection.DBPath(p.dirA), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(probs, "FORK") {
		t.Fatalf("deep doctor did not report the fork: %v", probs)
	}
	// the loud detection log fired
	if !containsOne(warnA.String(), "FORK DETECTED") {
		t.Fatalf("no loud fork log:\n%s", warnA.String())
	}

	// --- R33: a NON-forked origin still syncs despite the freeze --------------
	if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "post-fork message on A origin unique-ccc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dA.SyncWith(addrB2); err != nil {
		t.Fatalf("R33: reconcile with a frozen origin present still errored: %v", err)
	}
	if searchCount(t, dB2, "unique-ccc") != 1 {
		t.Fatalf("R33: A's own (non-forked) origin did not replicate while B-origin was frozen\nA:\n%s", warnA.String())
	}
	cancelB2()
	dB2.Close()

	// --- repair (offline): keep branch A, reissue branch B under recovery -----
	cancelA()
	dA.Close()
	resID, err := daemon.ResolveFork(daemon.ResolveForkOptions{
		Dir: p.dirA, OriginDevice: p.deviceB, Canonical: "local",
		RootKeyPath: p.rootKey, Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("fork resolve: %v", err)
	}
	if resID == "" {
		t.Fatal("no fork_resolution_id")
	}

	// restart A: log verifies (device.fork.resolve + reissued events), the fork
	// is resolved (info, not a problem), and the losing branch's content is
	// recovered AND its original is still quarantined (both branches preserved).
	warnA2 := &syncBuf{}
	dA, cancelA, _ = startSyncNode(t, p.baseA, p.dirA, warnA2)
	defer func() { cancelA(); dA.Close() }()

	rforks := dA.Forks()
	if len(rforks) != 1 || !rforks[0].Resolved || rforks[0].Canonical != "local" {
		t.Fatalf("fork not marked resolved after repair: %+v", rforks)
	}
	if searchCount(t, dA, "unique-bbb") != 1 {
		t.Fatal("repair did not reissue the losing branch's content under the recovery origin")
	}
	if searchCount(t, dA, "unique-aaa") != 1 {
		t.Fatal("repair lost the canonical branch")
	}
	// both branches preserved: the losing branch's frame is still in quarantine
	qdir := filepath.Join(p.dirA, ".cairn", "quarantine")
	if !hasFilesUnder(qdir) {
		t.Fatal("losing branch quarantine was deleted — must be preserved forever")
	}
	// deep doctor: the resolved fork is informational, not a problem
	probs2, infos2, err := daemon.DeepDoctor(fsx.OS{}, p.dirA, projection.DBPath(p.dirA), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if containsSubstr(probs2, "FORK") {
		t.Fatalf("resolved fork still reported as a problem: %v", probs2)
	}
	if !containsSubstr(infos2, "RESOLVED") {
		t.Fatalf("resolved fork not reported informationally: %v", infos2)
	}
	_ = bOrigin
}

func containsOne(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func hasFilesUnder(dir string) bool {
	found := false
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			found = true
		}
		return nil
	})
	return found
}
