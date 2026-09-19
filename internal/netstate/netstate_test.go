package netstate

// P3-6 — sensing tests. The invariant under test everywhere is the same one the
// package doc states: sensing may only ADD caution. Every failure mode of every
// probe must land on Unknown, and Unknown must be behaviourally identical to no
// sensing at all.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTriZeroValueIsUnknown(t *testing.T) {
	var s State
	if s.Metered != Unknown || s.OnBattery != Unknown {
		t.Fatalf("zero State is not Unknown: %+v", s)
	}
	if Unknown.String() != "unknown" || Yes.String() != "yes" || No.String() != "no" {
		t.Fatal("tri-state rendering changed")
	}
}

func TestDisabledSensorReadsNothing(t *testing.T) {
	st := Disabled.Sense(context.Background())
	if st.Metered != Unknown || st.OnBattery != Unknown {
		t.Fatalf("the disabled sensor reported a reading: %+v", st)
	}
	if st.Source == "" {
		t.Fatal("the disabled sensor gives no reason")
	}
}

func TestPlatformSensorNeverPanicsAndIsBounded(t *testing.T) {
	// Whatever this host is, Platform() must answer without panicking and
	// without hanging. On a machine with no NetworkManager and no battery (a
	// container) the answer is Unknown for both — which is the point.
	done := make(chan State, 1)
	go func() { done <- Platform().Sense(context.Background()) }()
	select {
	case st := <-done:
		t.Logf("this host senses: metered=%s battery=%s source=%q", st.Metered, st.OnBattery, st.Source)
		if st.Source == "" {
			t.Fatal("platform sensor gave no source detail")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("platform sensor hung")
	}
}

func TestCachedRefreshesOnlyAfterTTL(t *testing.T) {
	calls := 0
	now := time.Now()
	c := &Cached{
		inner: SensorFunc(func(context.Context) State {
			calls++
			return State{Metered: Yes, Source: "fake"}
		}),
		ttl: time.Minute,
		now: func() time.Time { return now },
	}
	for i := 0; i < 5; i++ {
		if st := c.Sense(context.Background()); st.Metered != Yes {
			t.Fatalf("reading %d: %+v", i, st)
		}
	}
	if calls != 1 {
		t.Fatalf("probe ran %d times inside the TTL (want 1)", calls)
	}
	now = now.Add(2 * time.Minute)
	c.Sense(context.Background())
	if calls != 2 {
		t.Fatalf("probe ran %d times after the TTL expired (want 2)", calls)
	}
}

func TestCachedWithNoSensorIsUnknown(t *testing.T) {
	c := NewCached(nil)
	st := c.Sense(context.Background())
	if st.Metered != Unknown || st.OnBattery != Unknown {
		t.Fatalf("a nil sensor produced a reading: %+v", st)
	}
	var nilCache *Cached
	if got := nilCache.Sense(context.Background()); got.Metered != Unknown {
		t.Fatal("a nil *Cached must still answer Unknown")
	}
}

func TestCachedIsConcurrencySafe(t *testing.T) {
	c := NewCached(SensorFunc(func(context.Context) State { return State{Metered: No, Source: "fake"} }))
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				c.Sense(context.Background())
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// A probe that fails, is absent, or times out must NEVER be reported as "not
// metered" — every one of these paths has to reach Unknown.
func TestProbeFailureIsAlwaysUnknown(t *testing.T) {
	orig := runProbe
	t.Cleanup(func() { runProbe = orig })

	for _, tc := range []struct {
		name string
		fn   func(ctx context.Context, name string, args ...string) (string, error)
	}{
		{"absent binary", func(context.Context, string, ...string) (string, error) {
			return "", errors.New(`exec: "nmcli": executable file not found in $PATH`)
		}},
		{"non-zero exit", func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exit status 8")
		}},
		{"timeout", func(ctx context.Context, _ string, _ ...string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}},
		{"empty output", func(context.Context, string, ...string) (string, error) {
			return "", nil
		}},
		{"garbage output", func(context.Context, string, ...string) (string, error) {
			return "not\nany\nformat\nwe\nknow\n", nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runProbe = tc.fn
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			st := sensePlatform(ctx)
			if st.Metered == No {
				// No is behaviourally identical to Unknown downstream, but a
				// failed probe must not even claim that much.
				t.Fatalf("a failed probe claimed a reading: %+v", st)
			}
			if st.Metered == Yes {
				t.Fatalf("a failed probe reported METERED: %+v", st)
			}
		})
	}
}
