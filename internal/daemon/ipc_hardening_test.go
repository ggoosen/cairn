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

// A view name that escapes views/ must be refused by every op that turns a
// view name into a filesystem path — and nothing may be created outside the
// cairn dir.
func TestViewNameTraversalRejected(t *testing.T) {
	d, deviceDir := serveDaemon(t)
	_ = d

	// A marker outside the views tree: if traversal works, files land here.
	for _, tc := range []struct {
		op  string
		req daemon.Request
	}{
		{"digest", daemon.Request{Op: "digest", AgentView: "../../evil"}},
		{"digest-sep", daemon.Request{Op: "digest", AgentView: "a/b"}},
		{"fetch", daemon.Request{Op: "fetch", MessageID: "m", AgentView: "../../evil"}},
		{"map", daemon.Request{Op: "map", AgentView: "../../evil"}},
		{"compact", daemon.Request{Op: "compact", AgentView: "../../evil"}},
		{"subscribe-local", daemon.Request{Op: "subscribe-local", LocalSub: &daemon.LocalSubRequest{View: "../../evil", InterestQuery: "q"}}},
	} {
		_, err := daemon.Call(deviceDir, tc.req)
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("%s with traversal view: want invalid-view refusal, got %v", tc.op, err)
		}
	}

	// Nothing named "evil" may exist anywhere under the test root.
	root := filepath.Dir(filepath.Dir(deviceDir)) // above device dir and cairn dir
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && strings.Contains(path, "evil") {
			t.Fatalf("traversal escaped the views tree: %s", path)
		}
		return nil
	})
}

// shortTempDir is t.TempDir() for tests that BIND the unix socket. macOS
// caps sun_path at 104 bytes and t.TempDir() sits under $TMPDIR
// (/var/folders/<2>/<32>/T/<TestName>/001 — already ~90 bytes), so the
// socket path overflows and bind fails with no visible error. Production is
// unaffected: XDG_RUNTIME_DIR is unset on macOS, so SocketDir() takes the
// short os.TempDir() fallback by design.
//
// D12: os.MkdirTemp("/tmp", …) — NOT os.MkdirTemp("", …). The empty root
// honors $TMPDIR, which is 5 bytes on Linux and 48 on macOS, so the second
// form yields a "short" dir only on the platform nobody ships on. That is
// exactly the mistake TestD5AdoptStandaloneScript made.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cairn-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// FIX-A7: the socket lives in a per-user 0700 dir; the daemon refuses to
// serve into a symlinked socket dir (MkdirAll follows symlinks silently).
func TestSocketDirHardened(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))
	_, deviceDir := serveDaemon(t)

	sockBytes, err := os.ReadFile(filepath.Join(deviceDir, "daemon.sock.path"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(string(sockBytes))
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket dir %s mode %v: want a 0700 directory", dir, fi.Mode())
	}
}

func TestSocketDirSymlinkRefused(t *testing.T) {
	// D12: shortTempDir, not t.TempDir() — the socket dir Serve prepares is
	// now the one it will actually BIND, so a runtime dir too long for
	// sun_path would be skipped by the ladder and this test would silently
	// stop exercising the refusal on macOS. The symlink must be the reason
	// Serve fails, not the length.
	base := shortTempDir(t)
	if err := os.Mkdir(filepath.Join(base, "elsewhere"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "elsewhere"), filepath.Join(base, "cairn")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", base) // SocketDir → base/cairn, the symlink

	dir := initCairn(t)
	d := startDaemon(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := d.Serve(ctx)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Serve into a symlinked socket dir: want refusal, got %v", err)
	}
}
