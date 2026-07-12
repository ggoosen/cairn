package daemon_test

// F6: restore UX (read-only ops permitted, writes refused with adopt
// pointer), TTL config tunables + housekeep, version handled at CLI level.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// R9: a portable-only restore serves search/digest/fetch read-only; every
// write is refused pointing at init --adopt.
func TestF6ReadOnlyRestoreDaemon(t *testing.T) {
	dir := initCairn(t)
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "survives the restore drill"})
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	// simulate the restore: device-local identity gone
	loaded, _ := identity.Load(dir)
	if err := os.RemoveAll(filepath.Dir(loaded.DeviceDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Load(dir); !errors.Is(err, identity.ErrRestoredCopy) {
		t.Fatalf("restore not detected as typed error: %v", err)
	}

	ro, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatalf("read-only daemon refused to start over restore: %v", err)
	}
	defer ro.Close()

	// READS work
	out, err := ro.Search(daemon.SearchOptions{Query: "restore drill", K: 5})
	if err != nil || len(out.Results) != 1 || out.Results[0].MessageID != pub.MessageID {
		t.Fatalf("read-only search: %v %v", out, err)
	}
	dg, err := ro.Digest(daemon.DigestOptions{AgentView: "operator", BudgetChars: 2000})
	if err != nil || !strings.Contains(dg.Payload, pub.MessageID) {
		t.Fatalf("read-only digest: %v", err)
	}
	fr, err := ro.Fetch(pub.MessageID, "operator")
	if err != nil || fr.BodyPath == "" {
		t.Fatalf("read-only fetch: %v", err)
	}

	// WRITES refused with the adopt pointer
	if _, err := ro.Publish(daemon.PublishRequest{Actor: "operator", Body: "should be refused"}); !errors.Is(err, daemon.ErrReadOnly) {
		t.Fatalf("publish on restore: %v", err)
	}
	if !strings.Contains(daemon.ErrReadOnly.Error(), "init --adopt") {
		t.Fatalf("refusal does not point at adopt: %v", daemon.ErrReadOnly)
	}
	if _, err := ro.SimpleEvent("message.retract", "message", pub.MessageID,
		map[string]any{"message_id": pub.MessageID}, daemon.PublishRequest{Actor: "operator"}); !errors.Is(err, daemon.ErrReadOnly) {
		t.Fatalf("retract on restore: %v", err)
	}
	if _, err := ro.Housekeep(); !errors.Is(err, daemon.ErrReadOnly) {
		t.Fatalf("housekeep on restore: %v", err)
	}
	if err := ro.ReleaseReserve(); !errors.Is(err, daemon.ErrReadOnly) {
		t.Fatalf("reserve release on restore: %v", err)
	}
	// the log is untouched (walk via mesh trust; no device identity here)
	trust, err := identity.MeshTrust(fsx.OS{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	origins, _ := cairnlog.Origins(fsx.OS{}, dir)
	total := int64(0)
	for _, o := range origins {
		rep, err := cairnlog.Walk(fsx.OS{}, dir, o, trust.Verifier(), nil)
		if err != nil {
			t.Fatal(err)
		}
		total += rep.Events
	}
	if total != 2 {
		t.Fatalf("read-only daemon appended events: %d", total)
	}
}

// setTOMLKey rewrites (or appends) a top-level integer key in cairn.toml.
func setTOMLKey(t *testing.T, dir, key string, value int) {
	t.Helper()
	path := filepath.Join(dir, "cairn.toml")
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key) {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.Join(kept, "\n") + fmt.Sprintf("\n%s = %d\n", key, value)
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

// R10: TTL and housekeeping cadence come from portable config.
func TestF6TTLConfigTunables(t *testing.T) {
	dir := initCairn(t)
	// tighten the TTL to 1 hour via portable config
	setTOMLKey(t, dir, "ephemeral_ttl_hours", 1)
	setTOMLKey(t, dir, "housekeep_minutes", 5)

	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "short-lived scratch", TextClass: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}
	// not yet expired (published "now", TTL 1h)
	deleted, err := d.Housekeep()
	if err != nil || len(deleted) != 0 {
		t.Fatalf("fresh ephemeral swept: %v %v", deleted, err)
	}
	_ = pub
}

func TestF6TTLExpiryWithTightConfig(t *testing.T) {
	dir := initCairn(t)
	setTOMLKey(t, dir, "ephemeral_ttl_hours", 1)

	// publish with a clock we control, then advance past the 1h TTL
	clock := timeAt(t, "2026-07-12T00:00:00.000000Z")
	d, err := daemon.Start(daemon.Options{Dir: dir, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "scratch to expire", TextClass: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}
	clock.AdvanceHours(2) // past the tightened 1h TTL, far under the 7d default
	deleted, err := d.Housekeep()
	if err != nil || len(deleted) != 1 || deleted[0] != pub.BodyHash {
		t.Fatalf("tightened TTL not honored: deleted=%v err=%v", deleted, err)
	}
}

// testClock is a controllable clock for TTL tests.
type testClock struct{ t time.Time }

func timeAt(t *testing.T, s string) *testClock {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return &testClock{t: parsed}
}
func (c *testClock) Now() time.Time     { return c.t }
func (c *testClock) AdvanceHours(h int) { c.t = c.t.Add(time.Duration(h) * time.Hour) }
