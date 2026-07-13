package daemon_test

// P2-3b: calibration harness end to end. Real searches + `found` outcomes
// produce episodes assembled from logged why_ranked; rank-stats reports the
// per-term contribution distribution; Calibrate replays under a weight grid and
// returns a recommendation (never auto-applied).

import (
	"fmt"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/rank"
)

func TestP23bCalibrationHarness(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// a corpus that all match "zzcorpus"
	for i := 0; i < 6; i++ {
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: fmt.Sprintf("zzcorpus item %d about anchors", i)}); err != nil {
			t.Fatal(err)
		}
	}
	// generate genuine episodes: 10 tasks, each searches then records a found
	// outcome on the top result (a self-consistent baseline the ranker already
	// serves — calibration should find no strict improvement, honestly).
	for i := 0; i < 10; i++ {
		out, err := d.Search(daemon.SearchOptions{Query: "zzcorpus", TaskID: fmt.Sprintf("task-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Results) == 0 {
			t.Fatal("no results")
		}
		if err := d.Outcome(out.InteractionID, "found", out.Results[0].MessageID); err != nil {
			t.Fatal(err)
		}
	}

	// episodes assemble from telemetry + stored explanations
	eps, err := d.CalibrationEpisodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) < 10 {
		t.Fatalf("expected ≥10 episodes, got %d", len(eps))
	}
	for _, ep := range eps {
		if ep.Found == "" || len(ep.Cands) == 0 {
			t.Fatalf("malformed episode: %+v", ep)
		}
	}

	// rank-stats reports the R term contribution (P0 profile → R,F,P active)
	stats, err := d.RankStats()
	if err != nil {
		t.Fatal(err)
	}
	var haveR bool
	for _, s := range stats {
		if s.Term == "R" && s.Count > 0 {
			haveR = true
		}
	}
	if !haveR {
		t.Fatalf("rank-stats missing R-term distribution: %+v", stats)
	}

	// calibration runs and returns a recommendation (found-top-result episodes
	// → the R-weighted baseline is already optimal, so no strict improvement).
	rec, n, err := d.Calibrate(rank.ProfileSearch, 0.25, 4)
	if err != nil {
		t.Fatalf("calibrate: %v", err)
	}
	if n < 10 {
		t.Fatalf("calibrate saw %d episodes", n)
	}
	if rec.GridSize == 0 {
		t.Fatal("empty weight grid")
	}
	if rec.Improves {
		t.Fatalf("baseline already serves the found result; no weighting should strictly beat it: %+v", rec)
	}
}
