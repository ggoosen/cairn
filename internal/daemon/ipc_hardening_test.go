package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

// serveDaemon starts an initialized daemon serving on the real unix socket
// and returns the device dir for daemon.Call.
func serveDaemon(t *testing.T) (d *daemon.Daemon, deviceDir string) {
	t.Helper()
	dir := initCairn(t)
	d = startDaemon(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Serve(ctx)

	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
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
	return d, loaded.DeviceDir
}

// A malformed object hash must be a typed pre-ack refusal, never a panic:
// store.Path slices hash[:2] and joins into the filesystem.
func TestPinMalformedHashRefused(t *testing.T) {
	_, deviceDir := serveDaemon(t)

	for _, bad := range []string{
		"",
		"z",
		"zz",
		"../../../etc/passwd",
		strings.Repeat("A", 64), // uppercase hex is not a store address
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
	} {
		// Call folds resp.Error into err; a dead daemon surfaces as a
		// "daemon not running" transport error instead.
		_, err := daemon.Call(deviceDir, daemon.Request{Op: "pin", ObjectRef: bad, Durability: "any-node"})
		if err == nil || !strings.Contains(err.Error(), "malformed object hash") {
			t.Fatalf("pin %q: want malformed-hash refusal, got %v", bad, err)
		}
	}

	// The daemon must still be serving after every refusal.
	resp, err := daemon.Call(deviceDir, daemon.Request{
		Op:      "publish",
		Publish: &daemon.PublishRequest{Actor: "operator", Body: "still alive"},
	})
	if err != nil || resp.Publish == nil {
		t.Fatalf("daemon not serving after malformed-hash pins: %+v %v", resp, err)
	}
}

// A panic inside one request's handling is recovered: the client gets a
// typed error and the daemon keeps serving subsequent connections.
func TestHandleConnRecoversPanic(t *testing.T) {
	_, deviceDir := serveDaemon(t)

	daemon.DispatchHookForTest = func(req daemon.Request) {
		if req.Op == "peek" {
			panic("injected test panic")
		}
	}
	t.Cleanup(func() { daemon.DispatchHookForTest = nil })

	_, err := daemon.Call(deviceDir, daemon.Request{Op: "peek", MessageID: "whatever"})
	if err == nil || !strings.Contains(err.Error(), "panic recovered") {
		t.Fatalf("want panic-recovered error, got %v", err)
	}

	daemon.DispatchHookForTest = nil
	after, err := daemon.Call(deviceDir, daemon.Request{
		Op:      "publish",
		Publish: &daemon.PublishRequest{Actor: "operator", Body: "alive after panic"},
	})
	if err != nil || after.Publish == nil {
		t.Fatalf("daemon not serving after recovered panic: %+v %v", after, err)
	}
}
