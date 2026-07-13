package daemon_test

// N9 hardening — network fault matrix beyond the N6/N7 drills (buildpack N9).
// N6 already covers kill-9 on receiver/sender mid-sync and a deleted segment;
// N7 covers kill-9 mid-blob-transfer; N8 covers fork. This adds the remaining
// rows: bidirectional partition/rejoin, duplicate-delivery floods (idempotency
// under pressure), and revoked-mid-sync (a device retired while the mesh runs).
// The live crossed two-auditor audit is the operator's task, not CI.

import (
	"io"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

func TestN9NetworkHardening(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	dB, cancelB, _ := startSyncNode(t, p.baseB, p.dirB, warnB)

	aOrigin := cairnlog.Origin{DeviceID: p.deviceA, Generation: config.FirstGeneration}
	bOrigin := cairnlog.Origin{DeviceID: p.deviceB, Generation: config.FirstGeneration}

	// =====================================================================
	// Drill 1 — bidirectional partition/rejoin. BOTH sides write while
	// partitioned (no sync); one reconcile on rejoin converges both.
	// =====================================================================
	for i := 0; i < 8; i++ {
		if _, err := dA.Publish(daemon.PublishRequest{Actor: "operator", Body: "partition-era A note about anchors zzpa"}); err != nil {
			t.Fatal(err)
		}
		if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "partition-era B note about anchors zzpb"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("rejoin reconcile: %v\nA:\n%s\nB:\n%s", err, warnA.String(), warnB.String())
	}
	if got := searchCount(t, dB, "zzpa"); got != 8 {
		t.Fatalf("rejoin: B has %d/8 of A's partition-era notes", got)
	}
	if got := searchCount(t, dA, "zzpb"); got != 8 {
		t.Fatalf("rejoin: A has %d/8 of B's partition-era notes", got)
	}
	// both frontiers now cover both origins identically
	aFull, bFull := originNext(t, dA, aOrigin), originNext(t, dA, bOrigin)
	if originNext(t, dB, aOrigin) != aFull || originNext(t, dB, bOrigin) != bFull {
		t.Fatal("rejoin: frontiers did not converge across both origins")
	}

	// =====================================================================
	// Drill 2 — duplicate-delivery flood: re-run the SAME reconcile many
	// times. Overlapping ranges are re-offered every pass; ingestion is
	// idempotent (at-least-once → exactly-once by sequence). No dupes, no
	// frontier drift.
	// =====================================================================
	beforeA := searchCount(t, dA, "anchors")
	for i := 0; i < 12; i++ {
		if _, err := dB.SyncWith(addrA); err != nil {
			t.Fatalf("flood pass %d: %v", i, err)
		}
	}
	if got := searchCount(t, dA, "anchors"); got != beforeA {
		t.Fatalf("flood: A's corpus changed under duplicate delivery (%d → %d) — non-idempotent", beforeA, got)
	}
	if originNext(t, dA, aOrigin) != aFull || originNext(t, dA, bOrigin) != bFull {
		t.Fatal("flood: frontier drifted under duplicate delivery")
	}
	if got := searchCount(t, dB, "zzpa"); got != 8 {
		t.Fatalf("flood: B's copy of A's notes changed (%d/8) — duplication", got)
	}

	// =====================================================================
	// Drill 3 — revoked-mid-sync. A revokes B (offline, root-signed), then
	// restarts. B's later events must NOT be ingested, and B is refused at
	// the authenticated listener (R27: revoked devices refused).
	// =====================================================================
	cancelB()
	dB.Close()
	cancelA()
	dA.Close()

	t.Setenv("CAIRN_DEVICE_STATE_DIR", p.baseA)
	if _, err := identity.RevokeDevice(p.dirA, p.deviceB, p.rootKey, "n9 hardening drill", nil, nil, io.Discard); err != nil {
		t.Fatalf("revoke B: %v", err)
	}

	warnA2 := &syncBuf{}
	dA, cancelA, addrA = startSyncNode(t, p.baseA, p.dirA, warnA2)
	defer func() { cancelA(); dA.Close() }()
	dB, cancelB, _ = startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB(); dB.Close() }()

	// B (now revoked) writes and tries to push to A.
	if _, err := dB.Publish(daemon.PublishRequest{Actor: "operator", Body: "post-revocation B note zzrevoked"}); err != nil {
		t.Fatal(err)
	}
	// The handshake must refuse the revoked device (R27) — SyncWith errors —
	// and regardless A must never ingest the post-revocation event.
	if _, err := dB.SyncWith(addrA); err == nil {
		t.Fatal("revoked-mid-sync: A accepted a sync from a REVOKED device (handshake should refuse)")
	}
	if got := searchCount(t, dA, "zzrevoked"); got != 0 {
		t.Fatalf("revoked-mid-sync: A ingested %d event(s) from a REVOKED device", got)
	}
}
