package daemon_test

// FIX-K1 (R50): the ephemeral invariant is delivery-time-scoped, and its
// surface is fetch/store/index/serve/advertise — not just inline-body.
//
// Third instance of the ephemeral-leak class (Codex N9 Brief A run 3, live
// two-node): B was offline when A published `--class ephemeral`; after B
// rejoined, B could search AND fetch the body. The publish EVENT must
// replicate (chain data); the CONTENT must not: an ephemeral body is offered
// only inside the live-gossip window at publish time, never on catch-up.
//
// These drills are the R50 mandatory regression shape — CROSS-NODE with a
// REJOIN — plus the live-peer contrast case and the cache-advertise case with
// a third node. They are RED against the pre-K1 tree (pushOrigin shipped
// ephemeral bodies on ANY push, including catch-up after a rejoin; get_object
// served any stored object; put_object stored anything).

import (
	"io"
	"path/filepath"
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

const (
	k1TinyWindow = 50 * time.Millisecond // "the live window has passed"
	k1PastWindow = 250 * time.Millisecond
)

// k1AssertNotDelivered asserts node d holds the ephemeral EVENT but not the
// CONTENT: no search hit, typed not-delivered fetch, object absent, doctor
// clean, chain contiguity intact up to the origin's frontier.
func k1AssertNotDelivered(t *testing.T, d *daemon.Daemon, dir, phrase string, pub *daemon.PublishResult, originA cairnlog.Origin, wantNext int64) {
	t.Helper()
	// (b) no search hit — the body was never indexed
	if got := searchCount(t, d, phrase); got != 0 {
		t.Fatalf("R50 VIOLATION: ephemeral body searchable on a node that was offline at publish (got %d hits)", got)
	}
	// (a) fetch returns the TYPED not-delivered result — never the body,
	// never an opaque error
	fr, err := d.Fetch(pub.MessageID, "operator")
	if err != nil {
		t.Fatalf("R50: fetch of a never-delivered ephemeral must return the typed result, got error: %v", err)
	}
	if !fr.NotDelivered {
		t.Fatalf("R50 VIOLATION: fetch did not mark ephemeral_not_delivered (expired=%v bodyPath=%q)", fr.Expired, fr.BodyPath)
	}
	if fr.BodyPath != "" {
		t.Fatalf("R50 VIOLATION: fetch produced a body file for a never-delivered ephemeral: %s", fr.BodyPath)
	}
	// the object is absent from the store
	if d.Store().Exists(pub.BodyHash) {
		t.Fatalf("R50 VIOLATION: ephemeral object %s present in a non-recipient's store", pub.BodyHash)
	}
	// (c) doctor exits clean — a missing ephemeral object is informational (R43)
	probs, _, err := daemon.DeepDoctor(fsx.OS{}, dir, projection.DBPath(dir), time.Now())
	if err != nil {
		t.Fatalf("deep doctor: %v", err)
	}
	if len(probs) != 0 {
		t.Fatalf("R43/R50: doctor not clean on a node with a withheld ephemeral: %v", probs)
	}
	// (d) the publish EVENT is present — chain contiguity intact
	if got := originNext(t, d, originA); got != wantNext {
		t.Fatalf("chain contiguity broken: origin %s/%d next=%d, want %d", originA.DeviceID, originA.Generation, got, wantNext)
	}
	info, err := d.Projection().MessageInfo(pub.MessageID)
	if err != nil {
		t.Fatalf("publish EVENT missing on the rejoined node: %v", err)
	}
	if info.TextClass != object.ClassEphemeral {
		t.Fatalf("replicated event lost its class: %q", info.TextClass)
	}
}

// TestK1EphemeralNoBackfillOnRejoin is the Codex drill: A and B connected,
// B disconnects, A publishes ephemeral M, B rejoins after the live window,
// full catch-up runs both directions to quiescence.
func TestK1EphemeralNoBackfillOnRejoin(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	defer func() { cancelA(); dA.Close() }()
	dB, cancelB, _ := startSyncNode(t, p.baseB, p.dirB, warnB)

	// baseline: both nodes converged while connected
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("baseline sync: %v", err)
	}

	// disconnect B
	cancelB()
	dB.Close()

	// A publishes the ephemeral M (plus a canonical control) while B is gone.
	// Tiny windows stand in for "the 60 s live window has passed" without a
	// real 60 s sleep — the production gate with a scaled constant.
	dA.SetEphemeralWindowsForTest(k1TinyWindow, k1TinyWindow)
	eph, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzk1secret rejoined nodes must never see this", TextClass: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}
	if eph.TextClass != object.ClassEphemeral {
		t.Fatalf("not ephemeral: %q", eph.TextClass)
	}
	if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzk1control canonical decision"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(k1PastWindow) // the live window passes while B is offline

	// reconnect B; run catch-up BOTH directions to quiescence — A→B is the
	// push path that leaked (anti-entropy push on rejoin), B→A is the pull.
	dB2, cancelB2, addrB2 := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB2(); dB2.Close() }()
	dB2.SetEphemeralWindowsForTest(k1TinyWindow, k1TinyWindow)
	if _, err := dA.SyncWith(addrB2); err != nil {
		t.Fatalf("A push to rejoined B: %v\nA:\n%s\nB:\n%s", err, warnA.String(), warnB.String())
	}
	if _, err := dB2.SyncWith(addrA); err != nil {
		t.Fatalf("B pull from A: %v", err)
	}

	// quiescence proof: the canonical control DID cross
	if got := searchCount(t, dB2, "zzk1control"); got != 1 {
		t.Fatalf("canonical control did not replicate (%d/1) — catch-up incomplete", got)
	}
	originA := cairnlog.Origin{DeviceID: p.deviceA, Generation: 1}
	k1AssertNotDelivered(t, dB2, p.dirB, "zzk1secret", eph, originA, originNext(t, dA, originA))

	// the ORIGIN still holds, indexes, and serves its own ephemeral
	if got := searchCount(t, dA, "zzk1secret"); got != 1 {
		t.Fatalf("origin lost its own ephemeral (%d/1)", got)
	}
}

// TestK1EphemeralLiveGossipContrast: a peer connected AT publish time DOES
// receive the ephemeral via live gossip — the feature still works (default
// windows, immediate push-on-append).
func TestK1EphemeralLiveGossipContrast(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	defer func() { cancelA(); dA.Close() }()
	dB, cancelB, addrB := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB(); dB.Close() }()
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("baseline sync: %v", err)
	}

	eph, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzk1live connected peers receive this", TextClass: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}
	// the push-on-append sweep, driven synchronously (B is connected NOW)
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatalf("live push to connected B: %v", err)
	}

	if got := searchCount(t, dB, "zzk1live"); got != 1 {
		t.Fatalf("live-gossip regression: connected peer did not receive the ephemeral (%d/1)", got)
	}
	if !dB.Store().Exists(eph.BodyHash) {
		t.Fatal("live-gossip regression: body object absent on a connected peer")
	}
	fr, err := dB.Fetch(eph.MessageID, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if fr.NotDelivered || fr.Expired || fr.BodyPath == "" {
		t.Fatalf("live-delivered ephemeral must fetch normally: %+v", fr)
	}
}

// TestK1EphemeralAcceptGate isolates the RECEIVER side of R50.1 ("not
// accepted"): a nonconforming sender that offers an ephemeral body outside
// the live window is refused by the receiver's accept gate.
func TestK1EphemeralAcceptGate(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, _ := startSyncNode(t, p.baseA, p.dirA, warnA)
	defer func() { cancelA(); dA.Close() }()
	dB, cancelB, addrB := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB(); dB.Close() }()

	// A's send window is huge — it will offer the body regardless of age
	// (standing in for a nonconforming/malicious sender). B's accept window is
	// effectively zero — everything ephemeral is outside it.
	dA.SetEphemeralWindowsForTest(24*time.Hour, 24*time.Hour)
	dB.SetEphemeralWindowsForTest(time.Nanosecond, time.Nanosecond)

	eph, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzk1refuse acceptance is gated too", TextClass: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond) // strictly older than B's window
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatalf("push: %v", err)
	}

	// the event landed; the offered body was refused
	if _, err := dB.Projection().MessageInfo(eph.MessageID); err != nil {
		t.Fatalf("event did not replicate: %v", err)
	}
	if dB.Store().Exists(eph.BodyHash) {
		t.Fatal("R50.1 VIOLATION: receiver stored an ephemeral body offered outside the live window")
	}
	if got := searchCount(t, dB, "zzk1refuse"); got != 0 {
		t.Fatalf("R50.1 VIOLATION: refused body still indexed (%d hits)", got)
	}
}

// addThirdNode enrolls a third device C into the pair's mesh (offline
// ceremony, daemons not running) and returns its portable dir, device-state
// base, and device id.
func addThirdNode(t *testing.T, p n6Pair) (dirC, baseC, deviceC string) {
	t.Helper()
	baseC = t.TempDir()
	tmp := t.TempDir()
	reqC := filepath.Join(tmp, "reqC.json")
	grantC := filepath.Join(tmp, "grantC.json")

	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseC)
	if _, err := identity.CreateEnrollRequest(identity.EnrollRequestOptions{DisplayName: "third-node", OutPath: reqC, Now: time.Now}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAIRN_DEVICE_STATE_DIR", p.baseA)
	if _, err := identity.Approve(identity.ApproveOptions{
		Dir: p.dirA, RequestPath: reqC, RootKeyPath: p.rootKey,
		GrantPath: grantC, Out: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseC)
	dirC = filepath.Join(t.TempDir(), "cairnC")
	if err := identity.Join(identity.JoinOptions{GrantPath: grantC, Dir: dirC, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	loadedC, err := identity.Load(dirC)
	if err != nil {
		t.Fatal(err)
	}
	loadedC.Device.SyncListen = "127.0.0.1:0"
	if err := loadedC.Device.SaveDevice(loadedC.DeviceDir); err != nil {
		t.Fatal(err)
	}
	return dirC, baseC, loadedC.Device.DeviceID
}

// TestK1EphemeralCacheAdvertise: a third node C that received the ephemeral
// LIVE must not later serve it — by push, by pull, or by direct object
// request — to node B, which was offline at publish time. And B must refuse
// the raw bytes even when pushed directly (put gate).
func TestK1EphemeralCacheAdvertise(t *testing.T) {
	p := setupN6Pair(t)
	dirC, baseC, _ := addThirdNode(t, p) // ceremony BEFORE daemons start

	warnA, warnB, warnC := &syncBuf{}, &syncBuf{}, &syncBuf{}
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	defer func() { cancelA(); dA.Close() }()
	dC, cancelC, addrC := startSyncNode(t, baseC, dirC, warnC)
	defer func() { cancelC(); dC.Close() }()

	// C converges with A while connected; B never comes up in this phase.
	if _, err := dC.SyncWith(addrA); err != nil {
		t.Fatalf("C baseline sync: %v\nA:\n%s\nC:\n%s", err, warnA.String(), warnC.String())
	}

	// A publishes the ephemeral; C receives it LIVE (cache-then-advertise
	// would now make C a source — R50 forbids that for ephemeral).
	eph, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "zzk1cache a cache must not re-serve this", TextClass: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dA.SyncWith(addrC); err != nil {
		t.Fatalf("live push to C: %v", err)
	}
	if got := searchCount(t, dC, "zzk1cache"); got != 1 {
		t.Fatalf("C (connected at publish) did not receive the ephemeral (%d/1)", got)
	}
	if !dC.Store().Exists(eph.BodyHash) {
		t.Fatal("C should hold the live-delivered body")
	}

	// the live window passes; then B comes up for the first time
	dA.SetEphemeralWindowsForTest(k1TinyWindow, k1TinyWindow)
	time.Sleep(k1PastWindow)

	dB0, cancelB0, _ := startSyncNode(t, p.baseB, p.dirB, warnB)
	// B catches up from A first (all origins, incl. C's device.add).
	if _, err := dB0.SyncWith(addrA); err != nil {
		t.Fatalf("B catch-up from A: %v", err)
	}
	// R37: trust is the recover-time snapshot — B learned of C (and C's
	// snapshot may predate B's device.add reaching it) so BOTH restart before
	// the B↔C phase. The restart also proves the R50 gates are wall-time
	// scoped, not in-memory state that a bounce would reset.
	cancelB0()
	dB0.Close()
	cancelC()
	dC.Close()
	dC2, cancelC2, addrC2 := startSyncNode(t, baseC, dirC, warnC)
	defer func() { cancelC2(); dC2.Close() }()
	dC2.SetEphemeralWindowsForTest(k1TinyWindow, k1TinyWindow)
	dB, cancelB, addrB := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB(); dB.Close() }()
	dB.SetEphemeralWindowsForTest(k1TinyWindow, k1TinyWindow)
	if !dC2.Store().Exists(eph.BodyHash) {
		t.Fatal("C lost the body across restart")
	}

	// B syncs with the CACHE C in both directions — every replication route a
	// cache could leak through.
	if _, err := dB.SyncWith(addrC2); err != nil {
		t.Fatalf("B pull from cache C: %v", err)
	}
	if _, err := dC2.SyncWith(addrB); err != nil {
		t.Fatalf("C push to B: %v", err)
	}

	if got := searchCount(t, dB, "zzk1cache"); got != 0 {
		t.Fatalf("R50 VIOLATION: cache C re-served an ephemeral to late-joining B (%d hits)", got)
	}
	if dB.Store().Exists(eph.BodyHash) {
		t.Fatal("R50 VIOLATION: ephemeral bytes reached B's store via the cache")
	}

	// direct object request: C must not SERVE the ephemeral body at all
	served, err := dB.ProbeGetObjectForTest(addrC2, eph.BodyHash)
	if err != nil {
		t.Fatalf("get_object probe: %v", err)
	}
	if served {
		t.Fatal("R50 VIOLATION: get_object served an ephemeral body object from the cache")
	}

	// direct object push: B must not ACCEPT the raw ephemeral bytes
	data, err := dA.Store().Get(eph.BodyHash)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := dA.ProbePutObjectForTest(addrB, eph.BodyHash, data)
	if err != nil {
		t.Fatalf("put_object probe: %v", err)
	}
	if accepted {
		t.Fatal("R50 VIOLATION: put_object stored an ephemeral body on a non-recipient")
	}
	if dB.Store().Exists(eph.BodyHash) {
		t.Fatal("R50 VIOLATION: pushed ephemeral bytes present in B's store after refusal")
	}

	// C keeps its own live-delivered copy (refusing to re-serve ≠ losing it)
	if got := searchCount(t, dC2, "zzk1cache"); got != 1 {
		t.Fatalf("C lost its live-delivered ephemeral (%d/1)", got)
	}
	_ = config.FirstSequence // keep config import if assertions above change
}
