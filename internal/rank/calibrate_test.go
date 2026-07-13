package rank

import (
	"math"
	"testing"
)

// build an episode where the found item has high S but low R, so a salience-
// heavy weighting ranks it first and an R-only weighting ranks it last.
func salienceEpisode(task, found string) Episode {
	return Episode{
		TaskID: task, Found: found,
		Cands: []CalibCandidate{
			{MessageID: found, R: 0.0, S: 1.0},
			{MessageID: "other1", R: 1.0, S: 0.0},
			{MessageID: "other2", R: 0.9, S: 0.0},
		},
	}
}

func TestEvaluateSuccessAndMRR(t *testing.T) {
	eps := []Episode{salienceEpisode("t1", "good")}
	// R-only: good has R=0 → ranked last (3rd) → not in top5? top5 yes (only 3),
	// so Success@5=1 but MRR=1/3.
	rOnly := Evaluate(eps, WeightVector{R: 1})
	if math.Abs(rOnly.MRR-1.0/3.0) > 1e-9 {
		t.Fatalf("R-only MRR = %v, want 1/3", rOnly.MRR)
	}
	// S-only: good ranked first → MRR=1.
	sOnly := Evaluate(eps, WeightVector{S: 1})
	if math.Abs(sOnly.MRR-1.0) > 1e-9 {
		t.Fatalf("S-only MRR = %v, want 1", sOnly.MRR)
	}
}

func TestSimplexGridSumsToOne(t *testing.T) {
	grid := SimplexGrid(0.25, []string{"R", "S"})
	// units of 4: (0,4),(1,3),(2,2),(3,1),(4,0) → 5 vectors
	if len(grid) != 5 {
		t.Fatalf("grid size = %d, want 5", len(grid))
	}
	for _, w := range grid {
		if math.Abs((w.R+w.S)-1.0) > 1e-9 {
			t.Fatalf("weights do not sum to 1: %+v", w)
		}
	}
}

func TestCalibrateRecommendsSalienceWhenItHelpsOnHoldout(t *testing.T) {
	// several tasks, all salience-dominant. Held-out tasks should confirm that
	// a salience-heavy vector beats an R-only baseline.
	var eps []Episode
	for _, task := range []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"} {
		eps = append(eps, salienceEpisode(task, "good-"+task))
	}
	train, holdout := HoldOutByTask(eps, 4)
	if len(train) == 0 || len(holdout) == 0 {
		t.Fatalf("bad split: train=%d holdout=%d", len(train), len(holdout))
	}
	grid := SimplexGrid(0.25, []string{"R", "S"})
	baseline := WeightVector{R: 1.0}
	rec := Calibrate(train, holdout, grid, baseline)
	if !rec.Improves {
		t.Fatalf("calibration should improve on holdout: %+v", rec)
	}
	if rec.Best.S <= rec.Best.R {
		t.Fatalf("best vector should be salience-heavy: %+v", rec.Best)
	}
	// the winner must actually beat the baseline on the held-out tasks
	if !rec.BestHoldout.better(rec.BaselineHoldout) {
		t.Fatalf("winner does not beat baseline on holdout: best=%+v base=%+v", rec.BestHoldout, rec.BaselineHoldout)
	}
}

func TestCalibrateNoImprovementIsHonest(t *testing.T) {
	// episodes where R alone already ranks the found item first → no weight
	// change can improve; Improves must be false (a strict beat, not a tie).
	var eps []Episode
	for _, task := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		eps = append(eps, Episode{TaskID: task, Found: "top-" + task, Cands: []CalibCandidate{
			{MessageID: "top-" + task, R: 1.0},
			{MessageID: "lo-" + task, R: 0.0},
		}})
	}
	train, holdout := HoldOutByTask(eps, 4)
	grid := SimplexGrid(0.25, []string{"R", "S"})
	rec := Calibrate(train, holdout, grid, WeightVector{R: 1.0})
	if rec.Improves {
		t.Fatalf("no weighting should beat R-only here, but Improves=true: %+v", rec)
	}
}
