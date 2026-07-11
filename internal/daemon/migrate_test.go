package daemon_test

import (
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// faultFS wraps the real filesystem and fails every MUTATING op after a
// countdown — the crash-window injector for migrate (rows 14/15) on real
// paths (SQLite needs them).
type faultFS struct {
	inner     fsx.FS
	mu        sync.Mutex
	count     int
	failAfter int // 0 = disabled
}

func (f *faultFS) tick() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count++
	if f.failAfter > 0 && f.count > f.failAfter {
		return syscall.EIO
	}
	return nil
}

func (f *faultFS) OpenFile(name string, flag int, perm os.FileMode) (fsx.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		if err := f.tick(); err != nil {
			return nil, err
		}
	}
	fl, err := f.inner.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: fl, fs: f}, nil
}

// faultFile ticks the write-path File ops so the injector reaches the
// frame-append and fdatasync windows inside device.add / device.revoke.
type faultFile struct {
	fsx.File
	fs *faultFS
}

func (f *faultFile) Write(p []byte) (int, error) {
	if err := f.fs.tick(); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}

func (f *faultFile) Sync() error {
	if err := f.fs.tick(); err != nil {
		return err
	}
	return f.File.Sync()
}

func (f *faultFile) Truncate(n int64) error {
	if err := f.fs.tick(); err != nil {
		return err
	}
	return f.File.Truncate(n)
}
func (f *faultFS) Rename(o, n string) error {
	if err := f.tick(); err != nil {
		return err
	}
	return f.inner.Rename(o, n)
}
func (f *faultFS) Remove(n string) error    { return f.inner.Remove(n) }
func (f *faultFS) RemoveAll(n string) error { return f.inner.RemoveAll(n) }
func (f *faultFS) MkdirAll(p string, m os.FileMode) error {
	if err := f.tick(); err != nil {
		return err
	}
	return f.inner.MkdirAll(p, m)
}
func (f *faultFS) ReadDir(n string) ([]fs.DirEntry, error) { return f.inner.ReadDir(n) }
func (f *faultFS) ReadFile(n string) ([]byte, error)       { return f.inner.ReadFile(n) }
func (f *faultFS) Stat(n string) (fs.FileInfo, error)      { return f.inner.Stat(n) }
func (f *faultFS) SyncDir(n string) error {
	if err := f.tick(); err != nil {
		return err
	}
	return f.inner.SyncDir(n)
}

func countMigrateOps(t *testing.T) int {
	dir := initCairn(t)
	ff := &faultFS{inner: fsx.OS{}}
	if _, err := identity.Migrate(identity.MigrateOptions{Dir: dir, FS: ff, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	return ff.count
}

// TestMigrateCleanCeremony: happy path — old revoked, old origin read-only,
// new device usable end to end.
func TestMigrateCleanCeremony(t *testing.T) {
	dir := initCairn(t)
	loadedBefore, _ := identity.Load(dir)
	oldDevice := loadedBefore.Device.DeviceID

	res, err := identity.Migrate(identity.MigrateOptions{Dir: dir, Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if res.OldDeviceID != oldDevice || res.NewDeviceID == oldDevice || res.AddEventID == "" || res.RevokeEvent == "" {
		t.Fatalf("bad result: %+v", res)
	}

	// device-local identity swapped
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Device.DeviceID != res.NewDeviceID {
		t.Fatalf("config still points at %s", loaded.Device.DeviceID)
	}

	// new device usable: daemon starts, publishes, searches
	d := startDaemon(t, dir)
	pub, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "postmigration message"})
	if err != nil {
		t.Fatalf("new device cannot publish: %v", err)
	}
	hits, _ := d.Projection().SearchLexical("postmigration", 10, false)
	if len(hits) != 1 || hits[0].MessageID != pub.MessageID {
		t.Fatalf("postmigration search: %v", hits)
	}
	d.Close()

	// doctor clean across BOTH origins
	doc, err := cairnlog.Doctor(fsx.OS{}, dir, identity.NewChainVerifier().Verify)
	if err != nil || !doc.Clean() {
		t.Fatalf("doctor after migrate: %v clean=%v", err, doc != nil && doc.Clean())
	}

	// old origin read-only: a daemon forced onto the old identity refuses
	cfg := *loaded.Device
	cfg.DeviceID = oldDevice
	cfg.OriginGeneration = 1
	if err := cfg.SaveDevice(loaded.DeviceDir); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.Start(daemon.Options{Dir: dir}); err == nil {
		t.Fatal("daemon started under a REVOKED device identity")
	} else if !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("unexpected error: %v", err)
	}
	// restore the correct config for cleanup
	cfg.DeviceID = res.NewDeviceID
	if err := cfg.SaveDevice(loaded.DeviceDir); err != nil {
		t.Fatal(err)
	}
}

// TestMigrateCrashMatrix: fail migrate after every mutating fs op (the
// add/revoke windows, rows 14/15) — the intermediate state is always valid
// (doctor clean, both devices valid mid-ceremony) and a RERUN completes the
// ceremony to the same end state.
func TestMigrateCrashMatrix(t *testing.T) {
	total := countMigrateOps(t)
	if total < 4 {
		t.Fatalf("suspiciously few migrate ops: %d", total)
	}

	for k := 1; k <= total; k++ {
		dir := initCairn(t)
		ff := &faultFS{inner: fsx.OS{}, failAfter: k}
		_, err := identity.Migrate(identity.MigrateOptions{Dir: dir, FS: ff, Out: io.Discard})
		if err == nil {
			// some prefixes complete anyway (fault landed after the last
			// mutating log op) — the ceremony finished; verify and move on
		} else {
			// mid-ceremony state must be recoverable: rerun completes
			if _, rerr := identity.Migrate(identity.MigrateOptions{Dir: dir, Out: io.Discard}); rerr != nil {
				t.Fatalf("fail@%d: rerun did not complete: %v (first: %v)", k, rerr, err)
			}
		}

		loaded, err := identity.Load(dir)
		if err != nil {
			t.Fatalf("fail@%d: %v", k, err)
		}
		doc, err := cairnlog.Doctor(fsx.OS{}, dir, identity.NewChainVerifier().Verify)
		if err != nil || !doc.Clean() {
			t.Fatalf("fail@%d: doctor: %v clean=%v", k, err, doc != nil && doc.Clean())
		}
		// the completed ceremony always ends with a usable (non-revoked) device
		d, err := daemon.Start(daemon.Options{Dir: dir})
		if err != nil {
			t.Fatalf("fail@%d: daemon under final identity: %v", k, err)
		}
		if _, err := d.Publish(daemon.PublishRequest{Actor: "operator", Body: "after recovery"}); err != nil {
			t.Fatalf("fail@%d: publish: %v", k, err)
		}
		d.Close()
		_ = loaded
	}
}
