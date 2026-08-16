package daemon_test

// D2 acceptance: the origin-liveness beacon (BUILD-PLAN §4 D2; spec §13.2).
//
// Two real daemons, the N6 enrolment pair. The drills are in the order that
// matters: every way an origin frontier legitimately moves or legitimately
// looks small comes FIRST, so that the one drill which must alarm is proved
// against a background of ones that must not. A beacon that cries wolf on an
// ordinary restart would be turned off within a week.
//
//	1. ordinary operation — publish + reconcile, no alarm
//	2. ordinary restart — stop and start B unchanged, no alarm
//	3. ordinary catch-up — B is 20 events behind A's origin, no alarm
//	4. thin node — B declares itself thin while behind, no alarm
//	5. STALE BACKUP RESTORE — B comes back holding less of its OWN origin: alarm
//	   on A, naming the origin and both watermarks, surfaced by doctor
//	6. recovery — once B is back at its watermark the alarm is marked recovered
//	   and doctor demotes it from PROBLEM to note

import (
	"io"
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
	"github.com/ggoosen/cairn/internal/projection"
)

// noAlarms fails with context when either node has recorded a regression.
func noAlarms(t *testing.T, drill string, nodes map[string]*daemon.Daemon) {
	t.Helper()
	for name, d := range nodes {
		if a := d.LivenessAlarms(); len(a) > 0 {
			t.Fatalf("%s: FALSE POSITIVE — node %s raised %d liveness alarm(s): %s",
				drill, name, len(a), a[0].Describe())
		}
	}
}

func watermarkFor(t *testing.T, d *daemon.Daemon, o cairnlog.Origin) int64 {
	t.Helper()
	for _, w := range d.OriginWatermarks() {
		if w.OriginDevice == o.DeviceID && w.OriginGen == o.Generation {
			return w.NextSeq
		}
	}
	return 0
}

func doctorProblems(t *testing.T, dir string) []string {
	t.Helper()
	problems, _, err := daemon.DeepDoctor(fsx.OS{}, dir, projection.DBPath(dir), time.Now())
	if err != nil {
		t.Fatalf("deep doctor %s: %v", dir, err)
	}
	return problems
}

func doctorInfos(t *testing.T, dir string) []string {
	t.Helper()
	_, infos, err := daemon.DeepDoctor(fsx.OS{}, dir, projection.DBPath(dir), time.Now())
	if err != nil {
		t.Fatalf("deep doctor %s: %v", dir, err)
	}
	return infos
}

func hasLine(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// setRole rewrites a stopped node's device-local role (P3-3).
func setRole(t *testing.T, base, dir, role string) {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", base)
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatalf("load %s: %v", dir, err)
	}
	loaded.Device.Role = role
	if err := loaded.Device.SaveDevice(loaded.DeviceDir); err != nil {
		t.Fatalf("save device config: %v", err)
	}
}

func TestD2OriginLivenessBeacon(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	bOrigin := cairnlog.Origin{DeviceID: p.deviceB, Generation: config.FirstGeneration}

	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	defer func() { cancelA(); dA.Close() }()
	dB, cancelB, addrB := startSyncNode(t, p.baseB, p.dirB, warnB)

	// =====================================================================
	// Drill 1 — ordinary operation
	// =====================================================================
	for _, body := range []string{"beacon one about anchors", "beacon two about anchors", "beacon three about anchors"} {
		if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatalf("drill1: A reconcile with B: %v", err)
	}
	if got := searchCount(t, dA, "beacon"); got != 3 {
		t.Fatalf("drill1: A pulled %d/3 of B's messages", got)
	}
	noAlarms(t, "drill1 ordinary operation", map[string]*daemon.Daemon{"A": dA, "B": dB})
	backedUpSeq := watermarkFor(t, dA, bOrigin)
	if backedUpSeq <= int64(config.FirstSequence) {
		t.Fatalf("drill1: A recorded no watermark for B's origin (%d)", backedUpSeq)
	}

	// The BACKUP an operator would later restore from: B's portable data and
	// its device-local state, taken together and taken now.
	cancelB()
	dB.Close()
	backupDir := filepath.Join(t.TempDir(), "cairnB-backup")
	backupBase := filepath.Join(t.TempDir(), "baseB-backup")
	copyTree(t, p.dirB, backupDir)
	copyTree(t, p.baseB, backupBase)

	// =====================================================================
	// Drill 2 — ordinary restart (nothing lost, nothing gained)
	// =====================================================================
	dB, cancelB, addrB = startSyncNode(t, p.baseB, p.dirB, warnB)
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatalf("drill2: reconcile after restart: %v", err)
	}
	if _, err := dB.SyncWith(addrA); err != nil { // the other direction too
		t.Fatalf("drill2: reverse reconcile after restart: %v", err)
	}
	noAlarms(t, "drill2 ordinary restart", map[string]*daemon.Daemon{"A": dA, "B": dB})

	// =====================================================================
	// Drill 3 — ordinary catch-up (B is far behind on A's origin)
	// =====================================================================
	for i := 0; i < 20; i++ {
		if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "catchup burst about anchors"}); err != nil {
			t.Fatal(err)
		}
	}
	// A dials B while B holds none of that: B advertises A's origin far below
	// A's own watermark for it, which is exactly what "behind" looks like.
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatalf("drill3: reconcile mid-catch-up: %v", err)
	}
	noAlarms(t, "drill3 ordinary catch-up", map[string]*daemon.Daemon{"A": dA, "B": dB})
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("drill3: B catches up: %v", err)
	}
	if got := searchCount(t, dB, "catchup"); got != 20 {
		t.Fatalf("drill3: B caught up %d/20", got)
	}
	noAlarms(t, "drill3 after catch-up", map[string]*daemon.Daemon{"A": dA, "B": dB})

	// =====================================================================
	// Drill 4 — a thin node that legitimately holds less
	// =====================================================================
	cancelB()
	dB.Close()
	setRole(t, p.baseB, p.dirB, config.RoleThin)
	dB, cancelB, addrB = startSyncNode(t, p.baseB, p.dirB, warnB)
	for i := 0; i < 5; i++ {
		if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "thin-era message about anchors"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dA.SyncWith(addrB); err != nil { // thin B advertises less of A's origin
		t.Fatalf("drill4: reconcile with thin node: %v", err)
	}
	noAlarms(t, "drill4 thin node", map[string]*daemon.Daemon{"A": dA, "B": dB})
	cancelB()
	dB.Close()
	setRole(t, p.baseB, p.dirB, config.RoleFull)
	dB, cancelB, addrB = startSyncNode(t, p.baseB, p.dirB, warnB)

	// B moves on: the watermark A holds for B's origin must now exceed the
	// backup taken above, or the stale-restore drill proves nothing.
	for i := 0; i < 4; i++ {
		if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "post-backup message about anchors"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatalf("A pulls B's post-backup events: %v", err)
	}
	liveSeq := watermarkFor(t, dA, bOrigin)
	if liveSeq <= backedUpSeq {
		t.Fatalf("setup: B's origin did not advance past the backup (%d <= %d)", liveSeq, backedUpSeq)
	}
	noAlarms(t, "pre-restore", map[string]*daemon.Daemon{"A": dA, "B": dB})
	if probs := doctorProblems(t, p.dirA); hasLine(probs, "ORIGIN LIVENESS") {
		t.Fatalf("pre-restore: doctor already reports a liveness problem: %v", probs)
	}

	// =====================================================================
	// Drill 5 — THE STALE BACKUP RESTORE
	// =====================================================================
	cancelB()
	dB.Close()
	if err := os.RemoveAll(p.dirB); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(p.baseB); err != nil {
		t.Fatal(err)
	}
	copyTree(t, backupDir, p.dirB)
	copyTree(t, backupBase, p.baseB)
	dB, cancelB, addrB = startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB(); dB.Close() }()

	// B is genuinely regressed on its OWN origin before anything talks to it.
	if got := originNext(t, dB, bOrigin); got != backedUpSeq {
		t.Fatalf("drill5: restored B is at next_seq %d, want the backup's %d", got, backedUpSeq)
	}
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatalf("drill5: reconcile with the restored node: %v\nA warn:\n%s", err, warnA.String())
	}

	// What the restored node itself experiences, pinned because it is the
	// reason the beacon has to live on the OTHER node: A pushes B's own lost
	// events back, and B refuses them as equivocation on its active origin
	// ("device clone") and freezes it. So a stale restore does NOT self-heal,
	// and without the beacon the only alarm anywhere is one that blames a clone
	// that never existed.
	if n := dB.UnresolvedForks(); n != 1 {
		t.Fatalf("drill5: the restored node recorded %d forks, want 1 — the operator guidance in the alarm text assumes this refusal", n)
	}
	if !strings.Contains(warnB.String(), "device clone") {
		t.Fatalf("drill5: the restored node did not refuse its own pushed-back events as a clone:\n%s", warnB.String())
	}

	alarms := dA.LivenessAlarms()
	if len(alarms) != 1 {
		t.Fatalf("drill5: A raised %d alarms, want exactly 1\nA warn:\n%s", len(alarms), warnA.String())
	}
	a := alarms[0]
	switch {
	case a.OriginDevice != p.deviceB || a.OriginGen != config.FirstGeneration:
		t.Fatalf("drill5: alarm names origin %s/%d, want %s/1", a.OriginDevice, a.OriginGen, p.deviceB)
	case a.WatermarkSeq != liveSeq:
		t.Fatalf("drill5: alarm watermark %d, want %d", a.WatermarkSeq, liveSeq)
	case a.ObservedSeq != backedUpSeq:
		t.Fatalf("drill5: alarm observed %d, want the restored %d", a.ObservedSeq, backedUpSeq)
	case a.Peer != p.deviceB:
		t.Fatalf("drill5: alarm blames peer %s, want the origin device %s", a.Peer, p.deviceB)
	case a.Recovered:
		t.Fatal("drill5: a freshly raised alarm is already marked recovered")
	case a.Lost() != liveSeq-backedUpSeq:
		t.Fatalf("drill5: alarm accounts for %d missing events, want %d", a.Lost(), liveSeq-backedUpSeq)
	}
	if !strings.Contains(warnA.String(), "ORIGIN LIVENESS ALARM") {
		t.Fatalf("drill5: the alarm was not logged on A:\n%s", warnA.String())
	}
	// It ALARMS; it does not quarantine. A's own view of B's origin is intact.
	if n := dA.UnresolvedForks(); n != 0 {
		t.Fatalf("drill5: A froze %d origin(s) — the beacon must alarm, never quarantine", n)
	}
	if got := searchCount(t, dA, "post-backup"); got != 4 {
		t.Fatalf("drill5: A lost B's post-backup events (%d/4)", got)
	}
	// doctor reports it, with both watermarks in the text
	probs := doctorProblems(t, p.dirA)
	if !hasLine(probs, "ORIGIN LIVENESS") {
		t.Fatalf("drill5: doctor does not report the regression: %v", probs)
	}
	if !hasLine(probs, p.deviceB) {
		t.Fatalf("drill5: doctor does not name the origin device: %v", probs)
	}

	// The alarm is durable: a fresh reader (doctor without the daemon) sees it.
	if got := daemon.ReadLivenessAlarms(fsx.OS{}, p.dirA); len(got) != 1 {
		t.Fatalf("drill5: the alarm did not persist to disk (%d records)", len(got))
	}

	// =====================================================================
	// Drill 6 — recovery demotes the alarm without erasing it
	// =====================================================================
	// Bring B's own origin back to (at least) its watermark the only way a
	// device can: by writing. The events themselves are gone; what the beacon
	// tracks is the frontier.
	for int64(originNext(t, dB, bOrigin)) < liveSeq {
		if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "post-restore rewrite about anchors"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dA.SyncWith(addrB); err != nil {
		// The rewritten sequences ARE equivocation (two events at one
		// coordinate), so A may well freeze the origin here — that is the fork
		// machinery doing its job, and it is not this drill's subject.
		t.Logf("drill6: reconcile after the rewrite returned %v (fork handling is N8's, not D2's)", err)
	}
	after := dA.LivenessAlarms()
	if len(after) != 1 {
		t.Fatalf("drill6: alarm count changed to %d — a recovered alarm must be kept, not deleted", len(after))
	}
	if !after[0].Recovered {
		t.Fatalf("drill6: the alarm was not marked recovered once the origin returned to its watermark: %s", after[0].Describe())
	}
	if probs := doctorProblems(t, p.dirA); hasLine(probs, "ORIGIN LIVENESS") {
		t.Fatalf("drill6: doctor still reports a recovered regression as a PROBLEM: %v", probs)
	}
	if infos := doctorInfos(t, p.dirA); !hasLine(infos, "origin-liveness") {
		t.Fatalf("drill6: doctor dropped the recovered regression entirely: %v", infos)
	}
}

// TestD2WatermarkSurvivesRestart pins the beacon's one durability requirement:
// the watermark is what makes a regression detectable, so it must outlive the
// daemon that learned it. (The floor is also recomputed from the local log, so
// this test deliberately checks the PERSISTED file, not just the behaviour.)
func TestD2WatermarkSurvivesRestart(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, _ := startSyncNode(t, p.baseA, p.dirA, warnA)
	dB, cancelB, addrB := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB(); dB.Close() }()

	for i := 0; i < 3; i++ {
		if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "watermark durability check"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dA.SyncWith(addrB); err != nil {
		t.Fatal(err)
	}
	bOrigin := cairnlog.Origin{DeviceID: p.deviceB, Generation: config.FirstGeneration}
	want := watermarkFor(t, dA, bOrigin)
	if want <= int64(config.FirstSequence) {
		t.Fatalf("no watermark recorded for B (%d)", want)
	}
	cancelA()
	dA.Close()

	blob, err := os.ReadFile(filepath.Join(p.dirA, config.DerivedDirName, config.LivenessRegistry))
	if err != nil {
		t.Fatalf("the beacon persisted nothing: %v", err)
	}
	if !strings.Contains(string(blob), p.deviceB) {
		t.Fatalf("persisted registry does not mention B's origin:\n%s", blob)
	}

	dA2, cancelA2, _ := startSyncNode(t, p.baseA, p.dirA, warnA)
	defer func() { cancelA2(); dA2.Close() }()
	if got := watermarkFor(t, dA2, bOrigin); got != want {
		t.Fatalf("watermark after restart is %d, want %d", got, want)
	}
	_ = io.Discard
}
