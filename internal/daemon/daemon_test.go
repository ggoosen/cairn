package daemon_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/projection"
)

// initCairn creates a real initialized cairn on the OS filesystem (SQLite
// needs real paths) with device state redirected into the test dir.
func initCairn(t *testing.T) (dir string) {
	t.Helper()
	t.Setenv("CAIRN_DEVICE_STATE_DIR", t.TempDir())
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	dir = filepath.Join(t.TempDir(), "cairn")
	_, err := identity.Initialize(identity.InitOptions{Dir: dir, Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func startDaemon(t *testing.T, dir string) *daemon.Daemon {
	t.Helper()
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// walkActive verifies + walks the active origin, returning the event ids.
func walkActive(t *testing.T, dir string) []string {
	t.Helper()
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	_, err = cairnlog.Walk(fsx.OS{}, dir,
		cairnlog.Origin{DeviceID: loaded.Device.DeviceID, Generation: loaded.Device.OriginGeneration},
		identity.NewChainVerifier().Verify,
		func(env *event.Envelope, _ []byte) error {
			ids = append(ids, env.EventID)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestPublishSearchPeekRoundTrip(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	res, err := d.Publish(daemon.PublishRequest{
		Actor: "operator",
		Body:  "the quick brown fox jumps over the lazy daemon",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.MessageID == "" || res.RevisionID == "" || len(res.EventIDs) != 1 {
		t.Fatalf("bad result: %+v", res)
	}

	// synchronous lexical enrichment: immediately search-visible
	hits, err := d.Projection().SearchLexical("daemon", 10, false)
	if err != nil || len(hits) != 1 || hits[0].MessageID != res.MessageID {
		t.Fatalf("search after publish: %v %v", hits, err)
	}

	info, err := d.Projection().MessageInfo(res.MessageID)
	if err != nil || info.HeadRevisionID != res.RevisionID {
		t.Fatalf("peek: %+v %v", info, err)
	}

	// reply inherits/creates the thread (atomic publish variant)
	rep, err := d.Publish(daemon.PublishRequest{
		Actor:            "operator",
		Body:             "a reply body",
		ReplyToMessageID: res.MessageID,
	})
	if err != nil {
		t.Fatal(err)
	}
	repInfo, _ := d.Projection().MessageInfo(rep.MessageID)
	if repInfo.ThreadID != res.MessageID || repInfo.ReplyToMessageID != res.MessageID {
		t.Fatalf("reply threading: %+v", repInfo)
	}
}

// M4 acceptance: concurrent CLI senders serialize correctly — all acked
// events present after restart, chain contiguous (Walk verifies), none lost.
func TestConcurrentSendersSerialize(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)

	const senders = 10
	var wg sync.WaitGroup
	errs := make(chan error, senders)
	ids := make(chan string, senders)
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			res, err := d.Publish(daemon.PublishRequest{
				Actor: "operator",
				Body:  fmt.Sprintf("concurrent sender %d payload", n),
			})
			if err != nil {
				errs <- err
				return
			}
			ids <- res.EventIDs[0]
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Fatal(err)
	}
	acked := map[string]bool{}
	for id := range ids {
		acked[id] = true
	}
	if len(acked) != senders {
		t.Fatalf("acked %d, want %d", len(acked), senders)
	}
	d.Close()

	// chain contiguity + signatures verified by Walk; count + membership here
	seen := walkActive(t, dir)
	if len(seen) != senders+1 { // + genesis
		t.Fatalf("recovered %d events, want %d", len(seen), senders+1)
	}
	present := map[string]bool{}
	for _, id := range seen {
		present[id] = true
	}
	for id := range acked {
		if !present[id] {
			t.Fatalf("acked event %s missing after restart", id)
		}
	}
}

// Single-writer lock: a second daemon must refuse to start.
func TestSingleWriterLock(t *testing.T) {
	dir := initCairn(t)
	_ = startDaemon(t, dir)
	if _, err := daemon.Start(daemon.Options{Dir: dir, DBPath: filepath.Join(t.TempDir(), "other.sqlite")}); err == nil {
		t.Fatal("second daemon acquired the write lock")
	} else if !strings.Contains(err.Error(), "write lock") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Startup replay heals a deleted projection (derived state).
func TestStartupReplayHealsProjection(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "heal me after restart"})
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	dbPath := projection.DBPath(dir)
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		os.Remove(f)
	}

	d2 := startDaemon(t, dir)
	hits, err := d2.Projection().SearchLexical("heal", 10, false)
	if err != nil || len(hits) != 1 || hits[0].MessageID != res.MessageID {
		t.Fatalf("projection not healed: %v %v", hits, err)
	}
}

// IPC round trip over the real unix socket: publish, search, peek, fetch
// (manifest/body separation, trust=untrusted).
func TestIPCRoundTrip(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx)

	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// wait for socket registration
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(loaded.DeviceDir, "daemon.sock.path")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon socket never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := daemon.Call(loaded.DeviceDir, daemon.Request{
		Op:      "publish",
		Publish: &daemon.PublishRequest{Actor: "operator", Body: "ipc body content"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Publish == nil || resp.Publish.MessageID == "" {
		t.Fatalf("bad response: %+v", resp)
	}

	sresp, err := daemon.Call(loaded.DeviceDir, daemon.Request{Op: "search", Query: "ipc"})
	if err != nil || len(sresp.Results) != 1 {
		t.Fatalf("ipc search: %+v %v", sresp, err)
	}

	presp, err := daemon.Call(loaded.DeviceDir, daemon.Request{Op: "peek", MessageID: resp.Publish.MessageID})
	if err != nil || presp.Message == nil {
		t.Fatalf("ipc peek: %v", err)
	}

	fresp, err := daemon.Call(loaded.DeviceDir, daemon.Request{Op: "fetch", MessageID: resp.Publish.MessageID, AgentView: "tester"})
	if err != nil || fresp.Fetched == nil {
		t.Fatalf("ipc fetch: %v", err)
	}
	body, err := os.ReadFile(fresp.Fetched.BodyPath)
	if err != nil || string(body) != "ipc body content" {
		t.Fatalf("fetched body: %q %v", body, err)
	}
	manifest, err := os.ReadFile(fresp.Fetched.ManifestPath)
	if err != nil || !strings.Contains(string(manifest), `"trust": "untrusted"`) {
		t.Fatalf("manifest: %s %v", manifest, err)
	}
	if strings.Contains(string(manifest), "ipc body content") {
		t.Fatal("manifest contains body content — provenance separation violated")
	}
}

// Retract via SimpleEvent hides the message from default search.
func TestRetractHidesFromSearch(t *testing.T) {
	dir := initCairn(t)
	d := startDaemon(t, dir)
	res, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "retractable content"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SimpleEvent("message.retract", "message", res.MessageID,
		map[string]any{"message_id": res.MessageID}, daemon.PublishRequest{Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	hits, _ := d.Projection().SearchLexical("retractable", 10, false)
	if len(hits) != 0 {
		t.Fatal("retracted message still in default search")
	}
	hits, _ = d.Projection().SearchLexical("retractable", 10, true)
	if len(hits) != 1 {
		t.Fatal("retracted message missing with include_retracted")
	}
}
