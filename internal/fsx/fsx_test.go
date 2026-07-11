package fsx

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func write(t *testing.T, m *MemFS, path string, data string) {
	t.Helper()
	f, err := m.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Unsynced file data must NOT survive a power cycle; synced data must.
func TestPowerCycleDropsUnsyncedData(t *testing.T) {
	m := NewMemFS()
	if err := m.MkdirAll("/d", 0o700); err != nil {
		t.Fatal(err)
	}
	f, _ := m.OpenFile("/d/a", os.O_CREATE|os.O_WRONLY, 0o644)
	f.Write([]byte("synced"))
	f.Sync()
	f.Write([]byte("-unsynced"))
	f.Close()
	m.SyncDir("/d") // entry durable, tail data not

	m.PowerCycle()
	got, err := m.ReadFile("/d/a")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "synced" {
		t.Fatalf("after power cycle got %q, want %q", got, "synced")
	}
}

// A created file whose directory was never fsynced vanishes on power cycle
// (crash row 2/3 semantics), but survives a plain SIGKILL restart.
func TestPowerCycleDropsUnsyncedDirEntries(t *testing.T) {
	m := NewMemFS()
	m.MkdirAll("/d", 0o700)
	write(t, m, "/d/orphan", "x")
	f, _ := m.OpenFile("/d/orphan", os.O_WRONLY, 0o644)
	f.Sync() // data synced, entry NOT dir-synced
	f.Close()

	m.Restart() // SIGKILL: cache survives
	if _, err := m.ReadFile("/d/orphan"); err != nil {
		t.Fatal("SIGKILL lost a cached file")
	}

	m.PowerCycle()
	if _, err := m.ReadFile("/d/orphan"); err == nil {
		t.Fatal("power cycle kept a file whose dir entry was never synced")
	}
}

// Rename before SyncDir: entry reverts on power cycle (old name synced earlier).
func TestRenameDurabilityRequiresSyncDir(t *testing.T) {
	m := NewMemFS()
	m.MkdirAll("/d", 0o700)
	f, _ := m.OpenFile("/d/tmp", os.O_CREATE|os.O_WRONLY, 0o644)
	f.Write([]byte("v"))
	f.Sync()
	f.Close()
	m.SyncDir("/d") // tmp entry durable

	m.Rename("/d/tmp", "/d/final") // NOT dir-synced
	m.PowerCycle()
	if _, err := m.ReadFile("/d/final"); err == nil {
		t.Fatal("unsynced rename survived power cycle")
	}
	if _, err := m.ReadFile("/d/tmp"); err != nil {
		t.Fatal("old durable entry lost")
	}

	// now the synced variant
	m.Rename("/d/tmp", "/d/final")
	m.SyncDir("/d")
	m.PowerCycle()
	if _, err := m.ReadFile("/d/final"); err != nil {
		t.Fatal("synced rename did not survive power cycle")
	}
	if _, err := m.ReadFile("/d/tmp"); err == nil {
		t.Fatal("old name still present after synced rename + power cycle")
	}
}

// After CrashAt fires, no operation may mutate anything.
func TestCrashFreezesState(t *testing.T) {
	m := NewMemFS()
	m.MkdirAll("/d", 0o700)
	write(t, m, "/d/a", "before")
	m.SyncDir("/d")

	m.CrashAt(1)
	f, err := m.OpenFile("/d/b", os.O_CREATE|os.O_WRONLY, 0o644)
	_ = f
	if !errors.Is(err, ErrCrashed) {
		// open is not mutating; the write would crash instead
		if err != nil {
			t.Fatal(err)
		}
		if _, werr := f.Write([]byte("x")); !errors.Is(werr, ErrCrashed) {
			t.Fatalf("want ErrCrashed, got %v", werr)
		}
	}
	m.Restart()
	if _, err := m.ReadFile("/d/b"); err == nil {
		if got, _ := m.ReadFile("/d/b"); len(got) > 0 {
			t.Fatal("write after crash mutated state")
		}
	}
}

func TestFailAtENOSPCAndShortWrite(t *testing.T) {
	m := NewMemFS()
	m.MkdirAll("/d", 0o700)
	f, _ := m.OpenFile("/d/a", os.O_CREATE|os.O_WRONLY, 0o644)

	m.FailAt(1, syscall.ENOSPC, 0)
	if _, err := f.Write([]byte("xxxx")); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("want ENOSPC, got %v", err)
	}
	// one-shot: next write succeeds
	if _, err := f.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}

	// short write: 2 bytes land, then EIO
	m.FailAt(1, syscall.EIO, 2)
	n, err := f.Write([]byte("abcdef"))
	if !errors.Is(err, syscall.EIO) || n != 2 {
		t.Fatalf("want short write (2, EIO), got (%d, %v)", n, err)
	}
	f.Close()
	got, _ := m.ReadFile("/d/a")
	if string(got) != "okab" {
		t.Fatalf("cache content %q, want %q", got, "okab")
	}
}

// WriteFileAtomic end-state: either fully present-and-durable or absent,
// across every crash point, for both crash modes.
func TestWriteFileAtomicCrashMatrix(t *testing.T) {
	countOps := func() int {
		m := NewMemFS()
		m.MkdirAll("/d", 0o700)
		base := m.MutatingOps()
		if err := WriteFileAtomic(m, "/d/obj", []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		return m.MutatingOps() - base
	}
	total := countOps()
	if total < 3 {
		t.Fatalf("suspiciously few ops: %d", total)
	}

	for mode := 0; mode < 2; mode++ { // 0 = SIGKILL, 1 = power cycle
		for i := 1; i <= total; i++ {
			m := NewMemFS()
			m.MkdirAll("/d", 0o700)
			m.CrashAt(i)
			err := WriteFileAtomic(m, "/d/obj", []byte("payload"), 0o644)
			if err == nil {
				t.Fatalf("crash at op %d not surfaced", i)
			}
			if mode == 1 {
				m.PowerCycle()
			} else {
				m.Restart()
			}
			// invariant: final file either absent, or complete
			if data, rerr := m.ReadFile("/d/obj"); rerr == nil {
				if string(data) != "payload" {
					t.Fatalf("mode %d crash@%d: partial content %q", mode, i, data)
				}
			}
			// temp may linger (crash rows 1–2: "orphan temp removable") — but
			// must never be mistaken for the final object.
		}
	}

	// clean run after the matrix: file durable, survives power cycle
	m := NewMemFS()
	m.MkdirAll("/d", 0o700)
	if err := WriteFileAtomic(m, "/d/obj", []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.PowerCycle()
	if data, err := m.ReadFile("/d/obj"); err != nil || string(data) != "payload" {
		t.Fatalf("durable object lost: %q %v", data, err)
	}
}

func TestWriteFileAtomicNeverOverwrites(t *testing.T) {
	m := NewMemFS()
	m.MkdirAll("/d", 0o700)
	if err := WriteFileAtomic(m, "/d/obj", []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(m, "/d/obj", []byte("two"), 0o644); err == nil {
		t.Fatal("overwrite allowed")
	}
	got, _ := m.ReadFile("/d/obj")
	if string(got) != "one" {
		t.Fatalf("content clobbered: %q", got)
	}
}
