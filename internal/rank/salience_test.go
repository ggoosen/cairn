package rank

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestDemandPosterior(t *testing.T) {
	// no data → prior mean 0.20
	if got := DemandPosterior(0, 0); !approx(got, 0.20) {
		t.Fatalf("no-data demand = %v, want 0.20", got)
	}
	// few impressions, zero fetches → floored at prior (no negative judgment)
	if got := DemandPosterior(0, 4); got < 0.20 {
		t.Fatalf("4 impressions 0 fetches = %v, must not drop below prior 0.20", got)
	}
	// past the min-impressions gate, a truly ignored item CAN drop below prior
	if got := DemandPosterior(0, 20); got >= 0.20 {
		t.Fatalf("20 impressions 0 fetches = %v, want < 0.20 (negative judgment allowed)", got)
	}
	// heavily fetched item → high demand
	if got := DemandPosterior(15, 20); got < 0.5 {
		t.Fatalf("15/20 fetched = %v, want high demand", got)
	}
	// exact posterior: 3 fetches, 6 impressions → (3+1)/(6+1+4)=4/11
	if got := DemandPosterior(3, 6); !approx(got, 4.0/11.0) {
		t.Fatalf("posterior = %v, want %v", got, 4.0/11.0)
	}
}

func TestSalienceBoundedAndMonotone(t *testing.T) {
	// always in [0,1]
	for _, s := range []float64{
		Salience(0, 0, 0, 0),
		Salience(100, 100, 100, 100),
		Salience(-5, -5, -5, -5),
	} {
		if s < 0 || s > 1 {
			t.Fatalf("salience out of [0,1]: %v", s)
		}
	}
	// a fetched + replied + signalled item outranks an ignored one
	hot := Salience(10, 12, 5, 8)
	cold := Salience(0, 20, 0, 0)
	if hot <= cold {
		t.Fatalf("hot item salience %v should exceed cold %v", hot, cold)
	}
	// reference in-degree alone raises salience
	if Salience(0, 0, 4, 0) <= Salience(0, 0, 0, 0) {
		t.Fatal("reference in-degree did not raise salience")
	}
	// operator signal alone raises salience
	if Salience(0, 0, 0, 6) <= Salience(0, 0, 0, 0) {
		t.Fatal("operator signal did not raise salience")
	}
}
