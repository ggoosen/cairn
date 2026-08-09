package daemon_test

// FIX-A6 regression tests: publish sequencing. topic.create events append
// BEFORE message.publish, so a mid-sequence append failure leaves only
// idempotent topic creations durable — a retry resolves the existing topic
// instead of re-creating it (topics.name is UNIQUE: a duplicate create
// would fail projection and park). The residual window (publish durable,
// link append fails, error returned) is recorded as RULING-NEEDED in
// daemon.go.

import (
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/projection"
)

func activeEventTypes(t *testing.T, dir string) []string {
	t.Helper()
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	_, err = cairnlog.Walk(fsx.OS{}, dir,
		cairnlog.Origin{DeviceID: loaded.Device.DeviceID, Generation: loaded.Device.OriginGeneration},
		identity.NewChainVerifier().Verify,
		func(env *event.Envelope, _ []byte) error {
			types = append(types, env.EventType)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return types
}

// A clean auto-create publish appends create* → publish → link*, in order.
func TestA6PublishEventOrder(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	if _, err := d.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "ordered body",
		Topics: []string{"order-one", "order-two"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatal(err)
	}
	d.Close()

	types := activeEventTypes(t, dir)
	want := []string{"topic.create", "topic.create", "message.publish", "topic.link.add", "topic.link.add"}
	if len(types) < len(want) {
		t.Fatalf("too few events: %v", types)
	}
	tail := types[len(types)-len(want):]
	for i, w := range want {
		if tail[i] != w {
			t.Fatalf("event order = %v, want tail %v", tail, want)
		}
	}
}

// Fault at every mutating-op position of an auto-create publish: an errored
// request, after restart and retry, must never duplicate the topic (which
// would park on the UNIQUE name) and must leave doctor clean.
func TestA6PublishFaultRetryNoDuplicateTopic(t *testing.T) {
	// probe: count the ops one clean publish consumes
	probeDir := initCairn(t)
	pf := &faultFS{inner: fsx.OS{}}
	pd, err := daemon.Start(daemon.Options{Dir: probeDir, FS: pf, Embedder: embed.BagOfWords{}})
	if err != nil {
		t.Fatal(err)
	}
	pf.mu.Lock()
	before := pf.count
	pf.mu.Unlock()
	if _, err := pd.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "probe", Topics: []string{"probe-topic"}, AutoCreateTopics: true,
	}); err != nil {
		t.Fatal(err)
	}
	pf.mu.Lock()
	total := pf.count - before
	pf.mu.Unlock()
	pd.Close()
	if total < 2 {
		t.Fatalf("probe counted only %d ops", total)
	}

	req := daemon.PublishRequest{
		Actor: "operator", Body: "faulted body",
		Topics: []string{"fault-topic"}, AutoCreateTopics: true,
	}
	for k := 1; k <= total; k++ {
		dir := initCairn(t)
		ff := &faultFS{inner: fsx.OS{}}
		d, err := daemon.Start(daemon.Options{Dir: dir, FS: ff, Embedder: embed.BagOfWords{}})
		if err != nil {
			t.Fatal(err)
		}
		ff.mu.Lock()
		ff.failAfter = ff.count + k
		ff.mu.Unlock()
		_, perr := d.Publish(req)
		ff.mu.Lock()
		ff.failAfter = 0
		ff.mu.Unlock()
		d.Close()

		// restart (a failed append may have poisoned the handle — that is
		// the A5 contract) and retry the SAME request if it errored
		d2 := startDaemon(t, dir)
		if perr != nil {
			if _, err := d2.Publish(req); err != nil {
				t.Fatalf("fault@%d: retry after restart failed: %v", k, err)
			}
		}
		if _, err := d2.Projection().TopicIDByName("fault-topic"); err != nil {
			t.Fatalf("fault@%d: topic unresolvable after retry: %v", k, err)
		}
		d2.Close()
		problems, _, err := daemon.DeepDoctor(fsx.OS{}, dir, projection.DBPath(dir), time.Now())
		if err != nil {
			t.Fatalf("fault@%d: doctor: %v", k, err)
		}
		if len(problems) > 0 {
			t.Fatalf("fault@%d: doctor problems after retry (duplicate create parks on UNIQUE name): %v", k, problems)
		}
	}
}
