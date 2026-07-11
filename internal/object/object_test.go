package object

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

func newStore(t *testing.T) (*fsx.MemFS, *Store) {
	t.Helper()
	m := fsx.NewMemFS()
	if err := m.MkdirAll("/p", 0o700); err != nil {
		t.Fatal(err)
	}
	return m, NewStore(m, "/p")
}

func TestPutGetRoundTrip(t *testing.T) {
	_, s := newStore(t)
	body := []byte("hello cairn")
	hash, err := s.Put(body)
	if err != nil {
		t.Fatal(err)
	}
	if hash != Hash(body) || len(hash) != 64 {
		t.Fatalf("bad hash %q", hash)
	}
	if !strings.HasSuffix(s.Path(hash), "/"+hash[:2]+"/"+hash[2:]) || !strings.Contains(s.Path(hash), config.ObjectsDirName) {
		t.Fatalf("bad layout: %s", s.Path(hash))
	}
	got, err := s.Get(hash)
	if err != nil || string(got) != string(body) {
		t.Fatalf("get: %q %v", got, err)
	}
	// idempotent re-put (dedup, never overwrite)
	if _, err := s.Put(body); err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
}

func TestGetVerifiesContent(t *testing.T) {
	m, s := newStore(t)
	hash, _ := s.Put([]byte("authentic"))
	// corrupt the stored object in place
	f, _ := m.OpenFile(s.Path(hash), os.O_WRONLY|os.O_TRUNC, 0o644)
	f.Write([]byte("tampered!"))
	f.Close()
	if _, err := s.Get(hash); err == nil || !strings.Contains(err.Error(), "verification") {
		t.Fatalf("corrupt object served: %v", err)
	}
}

func TestPutCollisionWithDifferentContentIsHardError(t *testing.T) {
	m, s := newStore(t)
	body := []byte("real content")
	hash := Hash(body)
	// plant WRONG bytes at the address (simulated store corruption)
	m.MkdirAll(s.Path(hash)[:len(s.Path(hash))-len(hash[2:])-1], 0o700)
	if err := m.MkdirAll("/p/objects/"+hash[:2], 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := m.OpenFile(s.Path(hash), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("imposter"))
	f.Close()

	if _, err := s.Put(body); err == nil {
		t.Fatal("collision overwrite allowed")
	}
	got, _ := m.ReadFile(s.Path(hash))
	if string(got) != "imposter" {
		t.Fatal("existing object was touched on collision")
	}
}

func TestGetMissingIsErrNotFound(t *testing.T) {
	_, s := newStore(t)
	if _, err := s.Get(Hash([]byte("never stored"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// Crash rows 1–3 for the REAL object store: crash before every mutating op
// of Put, both modes. Invariant: the address either holds the complete
// object or nothing; a retry always succeeds.
func TestPutCrashMatrix(t *testing.T) {
	body := []byte("crash object body")
	countOps := func() int {
		m, s := newStore(t)
		base := m.MutatingOps()
		if _, err := s.Put(body); err != nil {
			t.Fatal(err)
		}
		return m.MutatingOps() - base
	}
	total := countOps()

	for _, mode := range []string{"sigkill", "powersim"} {
		for i := 1; i <= total; i++ {
			m, s := newStore(t)
			m.CrashAt(i)
			if _, err := s.Put(body); err == nil {
				t.Fatalf("%s@op%d: crash not surfaced", mode, i)
			}
			if mode == "powersim" {
				m.PowerCycle()
			} else {
				m.Restart()
			}
			hash := Hash(body)
			if data, err := m.ReadFile(s.Path(hash)); err == nil && string(data) != string(body) {
				t.Fatalf("%s@op%d: partial object at address: %q", mode, i, data)
			}
			// retry after restart must succeed and verify
			if _, err := s.Put(body); err != nil {
				t.Fatalf("%s@op%d: retry: %v", mode, i, err)
			}
			if got, err := s.Get(hash); err != nil || string(got) != string(body) {
				t.Fatalf("%s@op%d: post-retry get: %v", mode, i, err)
			}
		}
	}
}

func TestApplyPolicyDowngrades(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	big := int64(config.AutoDowngradeBytes) + 1

	for _, declared := range []string{ClassCanonical, ClassEager} {
		d, err := ApplyPolicy(declared, big, false, nil, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		if d.Effective != ClassEphemeral || !d.Downgraded || d.Reason == "" {
			t.Fatalf("%s big body not downgraded: %+v", declared, d)
		}
	}

	// ephemeral is never downgraded (it is the floor)
	d, _ := ApplyPolicy(ClassEphemeral, big, false, nil, 0, now)
	if d.Downgraded || d.Effective != ClassEphemeral {
		t.Fatalf("ephemeral mangled: %+v", d)
	}

	// operator override keeps the declared class
	d, _ = ApplyPolicy(ClassCanonical, big, true, nil, 0, now)
	if d.Downgraded || d.Effective != ClassCanonical {
		t.Fatalf("override ignored: %+v", d)
	}

	// small canonical unchanged
	d, _ = ApplyPolicy(ClassCanonical, 512, false, nil, 0, now)
	if d.Downgraded || d.Effective != ClassCanonical {
		t.Fatalf("small canonical downgraded: %+v", d)
	}

	// unknown class rejected
	if _, err := ApplyPolicy("secret", 1, false, nil, 0, now); err == nil {
		t.Fatal("unknown class accepted")
	}
}

func TestApplyPolicyPerMessageCeiling(t *testing.T) {
	// The per-message canonical ceiling is a knob operators tighten BELOW
	// the fixed 1 MiB auto-downgrade threshold (portable config).
	now := time.Now().UTC()
	const tightened = int64(4096)
	d, err := ApplyPolicy(ClassCanonical, tightened+1, false, nil, tightened, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Effective != ClassEphemeral || !strings.Contains(d.Reason, "per-message") {
		t.Fatalf("per-message ceiling not enforced: %+v", d)
	}
	// at the ceiling: allowed
	d, _ = ApplyPolicy(ClassCanonical, tightened, false, nil, tightened, now)
	if d.Downgraded {
		t.Fatalf("at-ceiling body downgraded: %+v", d)
	}
}

func TestDailyCanonicalCeiling(t *testing.T) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	day1 := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	u, err := LoadUsage(m, "/p", 1000)
	if err != nil {
		t.Fatal(err)
	}
	// consume 900 of 1000
	d, _ := ApplyPolicy(ClassCanonical, 900, false, u, 0, day1)
	if d.Downgraded {
		t.Fatalf("within ceiling downgraded: %+v", d)
	}
	// 200 more would exceed → downgrade
	d, _ = ApplyPolicy(ClassCanonical, 200, false, u, 0, day1)
	if !d.Downgraded || !strings.Contains(d.Reason, "daily") {
		t.Fatalf("ceiling not enforced: %+v", d)
	}
	// operator override still lands canonical and is counted
	d, _ = ApplyPolicy(ClassCanonical, 200, true, u, 0, day1)
	if d.Downgraded || d.Effective != ClassCanonical {
		t.Fatalf("override failed: %+v", d)
	}

	// counter persists across reload
	u2, _ := LoadUsage(m, "/p", 1000)
	if u2.usedToday(day1) != 1100 {
		t.Fatalf("persisted usage %d, want 1100", u2.usedToday(day1))
	}

	// next day: counter rolls over
	day2 := day1.Add(24 * time.Hour)
	d, _ = ApplyPolicy(ClassCanonical, 900, false, u2, 0, day2)
	if d.Downgraded {
		t.Fatalf("rollover failed: %+v", d)
	}
}

func TestHousekeepingAndContentExpired(t *testing.T) {
	_, s := newStore(t)
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour)
	old := now.Add(-config.EphemeralTTL - time.Hour)

	expiredBody := []byte("expired scratchpad")
	freshBody := []byte("fresh scratchpad")
	keptBody := []byte("shared with canonical")

	expiredHash, _ := s.Put(expiredBody)
	freshHash, _ := s.Put(freshBody)
	keptHash, _ := s.Put(keptBody)

	refs := []Ref{
		{Hash: expiredHash, TextClass: ClassEphemeral, CreatedAt: old},
		{Hash: freshHash, TextClass: ClassEphemeral, CreatedAt: fresh},
		// same object referenced ephemeral-and-old AND canonical → kept
		{Hash: keptHash, TextClass: ClassEphemeral, CreatedAt: old},
		{Hash: keptHash, TextClass: ClassCanonical, CreatedAt: old},
	}

	deleted, err := s.HousekeepEphemeral(refs, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != expiredHash {
		t.Fatalf("deleted %v, want only %s", deleted, expiredHash)
	}

	// fetch semantics
	if _, err := s.Fetch(freshHash, refs, now); err != nil {
		t.Fatalf("fresh ephemeral fetch: %v", err)
	}
	if _, err := s.Fetch(keptHash, refs, now); err != nil {
		t.Fatalf("kept object fetch: %v", err)
	}
	_, err = s.Fetch(expiredHash, refs, now)
	var exp *ExpiredError
	if !errors.As(err, &exp) || exp.Hash != expiredHash {
		t.Fatalf("want typed content_expired, got %v", err)
	}
	if !strings.Contains(err.Error(), "content_expired") {
		t.Fatalf("expired error not typed in message: %v", err)
	}

	// a missing object that is NOT explainable by expiry stays ErrNotFound
	ghost := Hash([]byte("never put"))
	_, err = s.Fetch(ghost, []Ref{{Hash: ghost, TextClass: ClassCanonical, CreatedAt: old}}, now)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing canonical object mis-typed: %v", err)
	}

	// housekeeping is idempotent
	deleted, err = s.HousekeepEphemeral(refs, now)
	if err != nil || len(deleted) != 0 {
		t.Fatalf("second housekeep: %v %v", deleted, err)
	}
}
