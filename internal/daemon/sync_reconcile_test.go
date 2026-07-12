package daemon_test

// N6 acceptance: two-node convergence (buildpack N6; spec §6.2; R29/R30).
// The two nodes are the enrolment-ceremony pair from N5 (A init + root
// offline; B enrolled/approved/joined) — the SAME mesh, which is the only
// configuration in which origin logs may converge (R34: independent meshes
// never merge). Drills, in order:
//
//	1. normal operation — A publishes (incl. a >64 KiB non-inline canonical
//	   body forcing real object replication); B pulls; B publishes; A pulls.
//	2. one node offline for 100+ events — A publishes 120 while B is silent;
//	   B catches up in one reconcile.
//	3. kill-9 mid-sync on the RECEIVER — B loses recent A-origin frames
//	   (torn/lost tail); restart repairs; resync re-fetches and converges.
//	4. kill-9 mid-sync on the SENDER — A loses recent B-origin frames;
//	   restart repairs; resync re-pushes and converges.
//	5. a deleted segment on the receiver — B's whole copy of A's origin is
//	   removed; restart regresses the frontier; resync re-fetches + verifies.
//	6. reindex on BOTH nodes rebuilds identical query results for canonical
//	   content, and deep doctor is clean on both.

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
)

// startSyncNode sets the device-state env for this node's base and starts a
// daemon with the sync listener bound; returns the daemon, a cancel, and the
// bound address. Restart-safe: the address is re-read from SyncAddr each call.
func startSyncNode(t *testing.T, base, dir string, warn io.Writer) (*daemon.Daemon, context.CancelFunc, string) {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", base)
	d, err := daemon.Start(daemon.Options{Dir: dir, Warn: warn})
	if err != nil {
		t.Fatalf("start daemon (%s): %v", dir, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx, func() error { return nil })
	deadline := time.Now().Add(3 * time.Second)
	for d.SyncAddr() == "" {
		if time.Now().After(deadline) {
			cancel()
			d.Close()
			t.Fatal("sync listener never bound")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return d, cancel, d.SyncAddr()
}

func searchCount(t *testing.T, d *daemon.Daemon, query string) int {
	t.Helper()
	hits, err := d.Projection().SearchLexical(query, 500, false)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return len(hits)
}

// originNext returns a node's LOG frontier (next expected sequence) for an
// origin — 1 if the node has no frames for it. This reflects the durable log,
// not the derived projection.
func originNext(t *testing.T, d *daemon.Daemon, o cairnlog.Origin) int64 {
	t.Helper()
	fr, err := d.Frontiers()
	if err != nil {
		t.Fatalf("frontiers: %v", err)
	}
	for _, f := range fr {
		if f.DeviceID == o.DeviceID && f.Generation == o.Generation {
			return f.NextSeq
		}
	}
	return int64(config.FirstSequence)
}

// n6Pair is the enrolment-ceremony output for a two-node N6 test.
type n6Pair struct {
	dirA, dirB       string
	baseA, baseB     string
	deviceA, deviceB string
	rootKey          string // path to the restored offline root key (A's mesh root)
}

// setupN6Pair runs the offline enrolment ceremony (A init + root offline; B
// enroll/approve/join) and returns both nodes' portable dirs, device-state
// bases, and device ids. Both nodes have the sync listener configured
// (127.0.0.1:0 under the loopback dev acknowledgement); neither daemon is
// started yet.
func setupN6Pair(t *testing.T) n6Pair {
	t.Helper()
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1")
	baseA, baseB := t.TempDir(), t.TempDir()
	tmp := t.TempDir()
	reqPath := filepath.Join(tmp, "req.json")
	grantPath := filepath.Join(tmp, "grant.json")
	rootRestored := filepath.Join(tmp, "restored-root.key")

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dirA, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	loadedA, err := identity.Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	rootLocal := filepath.Join(loadedA.DeviceDir, config.RootKeyName)
	blob, _ := os.ReadFile(rootLocal)
	os.WriteFile(rootRestored, blob, 0o600)
	os.Remove(rootLocal)
	loadedA.Device.SyncListen = "127.0.0.1:0"
	if err := loadedA.Device.SaveDevice(loadedA.DeviceDir); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	if _, err := identity.CreateEnrollRequest("wsl2-box", reqPath, time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	grant, err := identity.Approve(identity.ApproveOptions{
		Dir: dirA, RequestPath: reqPath, RootKeyPath: rootRestored,
		GrantPath: grantPath, Out: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = grant
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	dirB := filepath.Join(t.TempDir(), "cairnB")
	if err := identity.Join(grantPath, dirB, io.Discard); err != nil {
		t.Fatal(err)
	}
	loadedB, err := identity.Load(dirB)
	if err != nil {
		t.Fatal(err)
	}
	loadedB.Device.SyncListen = "127.0.0.1:0"
	if err := loadedB.Device.SaveDevice(loadedB.DeviceDir); err != nil {
		t.Fatal(err)
	}
	return n6Pair{
		dirA: dirA, dirB: dirB, baseA: baseA, baseB: baseB,
		deviceA: loadedA.Device.DeviceID, deviceB: loadedB.Device.DeviceID,
		rootKey: rootRestored,
	}
}

func TestN6TwoNodeConvergence(t *testing.T) {
	p := setupN6Pair(t)
	baseA, baseB := p.baseA, p.baseB
	dirA, dirB := p.dirA, p.dirB
	deviceA, deviceB := p.deviceA, p.deviceB
	warnA, warnB := &syncBuf{}, &syncBuf{}

	// =====================================================================
	// Drill 1 — normal operation
	// =====================================================================
	dA, cancelA, addrA := startSyncNode(t, baseA, dirA, warnA)
	dB, cancelB, _ := startSyncNode(t, baseB, dirB, warnB)

	// A publishes three small canonical messages + one >64 KiB body (forces
	// real object replication: the receiver must ingest the body OBJECT to
	// index it, not just the inline event).
	bigBody := strings.Repeat("filler words for a large canonical note. ", 2000) + " zzbigcanary"
	for _, b := range []string{"anchors alpha decision note", "anchors beta meeting summary", "anchors gamma design ruling"} {
		if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: bigBody}); err != nil {
		t.Fatal(err)
	}
	if len(bigBody) <= config.InlineBodyLimit {
		t.Fatalf("test body not large enough to be non-inline (%d <= %d)", len(bigBody), config.InlineBodyLimit)
	}

	// B pulls A's origin (genesis + device.add + messages + the big body).
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("B reconcile with A: %v\nA warn:\n%s\nB warn:\n%s", err, warnA.String(), warnB.String())
	}
	if got := searchCount(t, dB, "anchors"); got != 3 {
		t.Fatalf("drill1: B has %d/3 small canonical messages after sync", got)
	}
	if got := searchCount(t, dB, "zzbigcanary"); got != 1 {
		t.Fatalf("drill1: B did not receive the >64 KiB canonical BODY OBJECT (search hits=%d)", got)
	}

	// B publishes; the same reconcile pushes B's origin to A.
	if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "wsl node observation about anchors"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "second wsl observation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("B->A push reconcile: %v", err)
	}
	if got := searchCount(t, dA, "wsl"); got != 2 {
		t.Fatalf("drill1: A has %d/2 of B's messages after push", got)
	}
	if got := searchCount(t, dA, "anchors"); got != 4 { // 3 A's + 1 B's
		t.Fatalf("drill1: A anchors corpus = %d, want 4", got)
	}

	// =====================================================================
	// Drill 2 — one node offline for 100+ events
	// =====================================================================
	for i := 0; i < 120; i++ {
		if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "backlog burst message about anchors and offline catchup"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("drill2 catch-up reconcile: %v", err)
	}
	if got := searchCount(t, dB, "offline"); got != 120 {
		t.Fatalf("drill2: B caught up %d/120 backlog messages", got)
	}

	// snapshot A's canonical frontier for the crash drills
	aOrigin := cairnlog.Origin{DeviceID: deviceA, Generation: config.FirstGeneration}
	bOrigin := cairnlog.Origin{DeviceID: deviceB, Generation: config.FirstGeneration}

	// =====================================================================
	// Drill 3 — kill -9 mid-sync on the RECEIVER (B loses recent A frames)
	// =====================================================================
	cancelB()
	dB.Close()
	// emulate a crash that lost the most recent durable-looking tail of B's
	// copy of A's origin: truncate the open segment so recovery repairs a
	// torn tail and the frontier regresses.
	truncateTail(t, dirB, aOrigin, 6000)
	dB, cancelB, _ = startSyncNode(t, baseB, dirB, warnB) // recovery repairs the tail
	fullNext := originNext(t, dA, aOrigin)                // A's authoritative frontier
	if regressed := originNext(t, dB, aOrigin); regressed >= fullNext {
		t.Fatalf("drill3: truncation did not regress B's A-origin LOG frontier (%d >= %d)", regressed, fullNext)
	}
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("drill3 re-fetch reconcile: %v\nB warn:\n%s", err, warnB.String())
	}
	if got := originNext(t, dB, aOrigin); got != fullNext {
		t.Fatalf("drill3: B re-fetched to frontier %d, want %d after receiver crash", got, fullNext)
	}

	// =====================================================================
	// Drill 4 — kill -9 mid-sync on the SENDER (A loses recent B frames)
	// =====================================================================
	// B publishes a few more, pushes to A, then A "crashes" losing B's tail.
	for i := 0; i < 5; i++ {
		if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "post-crash wsl note about anchors"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("drill4 pre-crash push: %v", err)
	}
	bFull := originNext(t, dB, bOrigin) // B's authoritative frontier for its own origin
	cancelA()
	dA.Close()
	truncateTail(t, dirA, bOrigin, 400) // A loses recent B-origin frames
	dA, cancelA, addrA = startSyncNode(t, baseA, dirA, warnA)
	if regressed := originNext(t, dA, bOrigin); regressed >= bFull {
		t.Fatalf("drill4: truncation did not regress A's B-origin LOG frontier (%d >= %d)", regressed, bFull)
	}
	// A dials B this time to re-pull B's origin (bidirectional either way).
	addrB := dB.SyncAddr()
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatalf("drill4 sender re-converge: %v\nA warn:\n%s", err, warnA.String())
	}
	if got := originNext(t, dA, bOrigin); got != bFull {
		t.Fatalf("drill4: A re-fetched to frontier %d, want %d after sender crash", got, bFull)
	}

	// =====================================================================
	// Drill 5 — a deleted segment on the receiver, re-fetched + verified
	// =====================================================================
	aFull := originNext(t, dA, aOrigin)
	cancelB()
	dB.Close()
	deleteOrigin(t, dirB, aOrigin) // B's WHOLE copy of A's origin removed
	dB, cancelB, _ = startSyncNode(t, baseB, dirB, warnB)
	if got := originNext(t, dB, aOrigin); got != int64(config.FirstSequence) {
		t.Fatalf("drill5: after deleting the segment B's A-origin frontier is %d, want 1", got)
	}
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("drill5 re-fetch of deleted segment: %v\nB warn:\n%s", err, warnB.String())
	}
	if got := originNext(t, dB, aOrigin); got != aFull {
		t.Fatalf("drill5: B re-fetched deleted origin to frontier %d, want %d", got, aFull)
	}
	if got := searchCount(t, dB, "offline"); got != 120 {
		t.Fatalf("drill5: B corpus lost the backlog after re-fetch (%d/120)", got)
	}

	// =====================================================================
	// Drill 6 — reindex BOTH nodes -> identical canonical query results
	// =====================================================================
	// stop both daemons so their derived DBs can be rebuilt purely from log.
	cancelA()
	cancelB()
	dA.Close()
	dB.Close()

	snapA := reindexCanonical(t, dirA)
	snapB := reindexCanonical(t, dirB)
	if snapA != snapB {
		t.Fatalf("N6 convergence FAILED: reindexed canonical corpora differ\nA:\n%s\nB:\n%s", snapA, snapB)
	}

	// deep doctor clean on both after all drills
	for _, dir := range []string{dirA, dirB} {
		problems, _, err := daemon.DeepDoctor(fsx.OS{}, dir, projection.DBPath(dir), time.Now())
		if err != nil {
			t.Fatalf("deep doctor error on %s: %v", dir, err)
		}
		if len(problems) != 0 {
			t.Fatalf("deep doctor problems on %s: %v", dir, problems)
		}
	}
}

// truncateTail removes `n` bytes from the end of the (single) open segment of
// an origin, emulating a crash that lost the most recent frames.
func truncateTail(t *testing.T, portableDir string, o cairnlog.Origin, n int64) {
	t.Helper()
	seg := filepath.Join(cairnlog.SegmentDir(portableDir, o.DeviceID, o.Generation), cairnlog.SegmentName(config.FirstSequence))
	fi, err := os.Stat(seg)
	if err != nil {
		t.Fatalf("stat segment for truncation: %v", err)
	}
	newSize := fi.Size() - n
	if newSize < 0 {
		newSize = fi.Size() / 2
	}
	if err := os.Truncate(seg, newSize); err != nil {
		t.Fatalf("truncate segment: %v", err)
	}
}

// deleteOrigin removes every segment (and seal sidecar) of an origin from a
// node's portable dir, emulating a deliberately deleted segment.
func deleteOrigin(t *testing.T, portableDir string, o cairnlog.Origin) {
	t.Helper()
	dir := cairnlog.SegmentDir(portableDir, o.DeviceID, o.Generation)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read origin dir: %v", err)
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			t.Fatalf("remove %s: %v", e.Name(), err)
		}
	}
}

// reindexCanonical rebuilds the projection purely from the log and returns a
// stable snapshot of canonical/eager search results (the convergence oracle).
func reindexCanonical(t *testing.T, dir string) string {
	t.Helper()
	dbPath := projection.DBPath(dir)
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}
	bodyFetch := projection.StoreBodyFetch(object.NewStore(fsx.OS{}, dir))
	rep, err := projection.ReindexLexical(fsx.OS{}, dir, dbPath, nil, bodyFetch)
	if err != nil {
		t.Fatalf("reindex %s: %v", dir, err)
	}
	if rep.Parked != 0 {
		t.Fatalf("reindex %s parked %d events", dir, rep.Parked)
	}
	p, err := projection.Open(dbPath, bodyFetch)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var b strings.Builder
	for _, q := range []string{"anchors", "wsl", "offline", "zzbigcanary"} {
		hits, err := p.SearchLexical(q, 500, false)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(q + ":")
		for _, h := range hits {
			b.WriteString(" " + h.BodyHash)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// freePort binds :0, records the address, and closes it — the literal is
// reused as a fixed listen address so peers can name each other before start.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func setPeers(t *testing.T, base, dir, listen string, peers []string) {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", base)
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Device.SyncListen = listen
	loaded.Device.SyncPeers = peers
	if err := loaded.Device.SaveDevice(loaded.DeviceDir); err != nil {
		t.Fatal(err)
	}
}

// TestN6AntiEntropyLoopAndIdempotency exercises R29 (the anti-entropy sweep
// AND push-on-append driving convergence with NO explicit SyncWith) and
// at-least-once idempotent ingest (a redundant reconcile ingests nothing).
// The nodes are symmetric peers, as in a real deployment.
func TestN6AntiEntropyLoopAndIdempotency(t *testing.T) {
	p := setupN6Pair(t)
	addrA, addrB := freePort(t), freePort(t)
	setPeers(t, p.baseA, p.dirA, addrA, []string{addrB})
	setPeers(t, p.baseB, p.dirB, addrB, []string{addrA})

	warnA, warnB := &syncBuf{}, &syncBuf{}

	// A publishes BEFORE B starts — B's initial anti-entropy sweep (pull) must
	// converge it with no explicit SyncWith.
	dA, cancelA, _ := startSyncNode(t, p.baseA, p.dirA, warnA)
	defer func() { cancelA(); dA.Close() }()
	if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "sweep pull canary about anchors"}); err != nil {
		t.Fatal(err)
	}

	dB, cancelB, _ := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB(); dB.Close() }()

	waitFor := func(what string, d *daemon.Daemon, q string, want int) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if searchCount(t, d, q) == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: never converged (%q)\nA warn:\n%s\nB warn:\n%s", what, q, warnA.String(), warnB.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor("anti-entropy pull sweep", dB, "sweep", 1)

	// push-on-append (R29): A publishes AFTER both are running — A's own
	// push-on-append kick sweeps the new event to B with no explicit SyncWith.
	if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "push on append canary about anchors"}); err != nil {
		t.Fatal(err)
	}
	waitFor("push-on-append", dB, "push", 1)

	// idempotency: an explicit reconcile now ingests NOTHING.
	n, err := dB.SyncWith(addrA)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("idempotency: a redundant reconcile ingested %d events, want 0", n)
	}
}

// TestN6BulkCatchup exercises R30: a receiver far behind (past the threshold)
// is caught up via the bulk segment-streaming path (logged), and converges.
func TestN6BulkCatchup(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	defer func() { cancelA(); dA.Close() }()
	dB, cancelB, _ := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB(); dB.Close() }()

	// lower the bulk threshold so a modest backlog trips R30.
	dB.SetSyncBulkThresholdForTest(20)
	for i := 0; i < 40; i++ {
		if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "bulk backlog note about anchors"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("bulk catch-up reconcile: %v", err)
	}
	if !strings.Contains(warnB.String(), "bulk catch-up") {
		t.Fatalf("R30 bulk-catch-up path did not trigger\nB warn:\n%s", warnB.String())
	}
	if got := searchCount(t, dB, "backlog"); got != 40 {
		t.Fatalf("bulk catch-up converged %d/40", got)
	}
}
