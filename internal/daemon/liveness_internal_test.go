package daemon

// D2 unit drills on the beacon's registry. The two-daemon acceptance test
// (liveness_test.go) proves the whole path; these pin the decisions inside
// observePeer that a live pair cannot easily stage — a vanished origin, a
// third party gossiping about someone else's chain, a truncated frontier.

import (
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

func fr(device string, gen int, next int64) originFrontier {
	return originFrontier{originRef: originRef{DeviceID: device, Generation: gen}, NextSeq: next, LastEventID: "head"}
}

func TestD2WatermarksOnlyRise(t *testing.T) {
	r := newLivenessRegistry()
	r.observeLocal([]originFrontier{fr("dev-b", 1, 40)}, "t0")
	// a peer's own advertisement below the floor must not lower it
	r.observePeer([]originFrontier{fr("dev-b", 1, 10)}, "dev-b", "t1")
	if got := r.watermarks[cairnlog.Origin{DeviceID: "dev-b", Generation: 1}].NextSeq; got != 40 {
		t.Fatalf("watermark fell to %d — the highest EVER observed must be a floor", got)
	}
	// and a legitimate advance raises it
	r.observePeer([]originFrontier{fr("dev-b", 1, 55)}, "dev-b", "t2")
	if got := r.watermarks[cairnlog.Origin{DeviceID: "dev-b", Generation: 1}].NextSeq; got != 55 {
		t.Fatalf("watermark did not advance with the origin (%d)", got)
	}
}

func TestD2ThirdPartyGossipNeverAlarms(t *testing.T) {
	r := newLivenessRegistry()
	r.observeLocal([]originFrontier{fr("dev-b", 1, 40)}, "t0")
	// C advertises B's origin far behind — C is merely behind, which is the
	// normal state of a mesh and says nothing about B.
	if raised := r.observePeer([]originFrontier{fr("dev-b", 1, 3), fr("dev-c", 1, 9)}, "dev-c", "t1"); len(raised) != 0 {
		t.Fatalf("a third party holding less of B's origin raised %d alarm(s)", len(raised))
	}
	// ...and C's gossip must not raise B's watermark either: only the origin
	// device is authoritative about its own chain, in both directions.
	if got := r.watermarks[cairnlog.Origin{DeviceID: "dev-b", Generation: 1}].NextSeq; got != 40 {
		t.Fatalf("third-party gossip moved B's watermark to %d", got)
	}
}

func TestD2VanishedOriginAlarms(t *testing.T) {
	r := newLivenessRegistry()
	r.observeLocal([]originFrontier{fr("dev-b", 1, 40), fr("dev-b", 2, 12)}, "t0")
	// B comes back advertising generation 2 only: its whole generation-1 chain
	// is gone, which is loss even though nothing "moved backwards" in gen 2.
	raised := r.observePeer([]originFrontier{fr("dev-b", 2, 12)}, "dev-b", "t1")
	if len(raised) != 1 {
		t.Fatalf("a vanished origin raised %d alarm(s), want 1", len(raised))
	}
	a := raised[0]
	if a.OriginGen != 1 || a.ObservedSeq != -1 {
		t.Fatalf("alarm names gen %d observed %d, want gen 1 observed -1", a.OriginGen, a.ObservedSeq)
	}
	if want := int64(40 - config.FirstSequence); a.Lost() != want {
		t.Fatalf("vanished origin accounts for %d events, want %d", a.Lost(), want)
	}
}

func TestD2EmptyOrForeignOnlyFrontierIsNotARegression(t *testing.T) {
	r := newLivenessRegistry()
	r.observeLocal([]originFrontier{fr("dev-b", 1, 40)}, "t0")
	if raised := r.observePeer(nil, "dev-b", "t1"); len(raised) != 0 {
		t.Fatal("an empty frontier was read as a regression")
	}
	// A frontier that mentions nothing of the peer's own is malformed, not
	// evidence: every node advertises its own active origin.
	if raised := r.observePeer([]originFrontier{fr("dev-a", 1, 7)}, "dev-b", "t2"); len(raised) != 0 {
		t.Fatal("a frontier with no self-origin was read as a regression")
	}
	if len(r.alarms) != 0 {
		t.Fatalf("%d alarm(s) recorded from non-evidence", len(r.alarms))
	}
}

func TestD2RepeatObservationDoesNotDuplicate(t *testing.T) {
	r := newLivenessRegistry()
	r.observeLocal([]originFrontier{fr("dev-b", 1, 40)}, "t0")
	first := r.observePeer([]originFrontier{fr("dev-b", 1, 10)}, "dev-b", "t1")
	second := r.observePeer([]originFrontier{fr("dev-b", 1, 10)}, "dev-b", "t2")
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("regression reported %d then %d times", len(first), len(second))
	}
	if len(r.alarms) != 1 {
		t.Fatalf("%d alarm records for one regression", len(r.alarms))
	}
	a := r.alarms[cairnlog.Origin{DeviceID: "dev-b", Generation: 1}]
	if a.Observations != 2 || a.LastSeenAt != "t2" || a.DetectedAt != "t1" {
		t.Fatalf("repeat observation bookkeeping wrong: %+v", *a)
	}
	// recovery, then a fresh regression, re-opens the same record
	r.observePeer([]originFrontier{fr("dev-b", 1, 40)}, "dev-b", "t3")
	if !a.Recovered {
		t.Fatal("returning to the watermark did not mark the alarm recovered")
	}
	r.observePeer([]originFrontier{fr("dev-b", 1, 12)}, "dev-b", "t4")
	if a.Recovered || a.DetectedAt != "t4" {
		t.Fatalf("a renewed regression did not re-open the alarm: %+v", *a)
	}
}
