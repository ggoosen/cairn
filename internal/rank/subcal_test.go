package rank

import "testing"

// N3/R24: calibration is relative-only. These pin the decision table.
func TestCalibrateSubscription(t *testing.T) {
	const margin = 0.15
	cases := []struct {
		name      string
		sims      []float64 // sorted desc
		observed  []float64
		allowance int
		mode      string
		pct       int
		want      int
	}{
		{"empty pool", nil, nil, 10, "top_n", 90, 0},
		{"no allowance", []float64{0.9}, nil, 0, "top_n", 90, 0},
		{"single candidate, no history: caps govern", []float64{0.2}, nil, 10, "top_n", 90, 1},
		{"signal above junk", []float64{0.95, 0.10, 0.00}, nil, 10, "top_n", 90, 1},
		{"two signals above junk", []float64{0.95, 0.90, 0.05}, nil, 10, "top_n", 90, 2},
		{"allowance truncates, never widens", []float64{0.95, 0.90, 0.05}, nil, 1, "top_n", 90, 1},
		{"uniform junk surfaces nothing", []float64{0.10, 0.10, 0.09}, nil, 10, "top_n", 90, 0},
		{"junk-above-junk vs history", []float64{0.10, 0.00}, []float64{0.95, 0.10, 0.00}, 10, "top_n", 90, 0},
		{"signal vs junk history", []float64{0.93, 0.02}, []float64{0.10, 0.05, 0.00}, 10, "top_n", 90, 1},
		{"percentile mode cuts at pool percentile", []float64{0.95, 0.90, 0.30, 0.20, 0.10, 0.09, 0.08, 0.07, 0.06, 0.05}, nil, 10, "percentile", 90, 1},
	}
	for _, c := range cases {
		if got := CalibrateSubscription(c.sims, c.observed, c.allowance, c.mode, c.pct, margin); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}
