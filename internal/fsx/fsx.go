// Package fsx is the filesystem seam required by TESTING.md: an interface
// over the os calls the durability-critical paths use, with a production
// implementation (OS) and a fault-injecting in-memory implementation (MemFS)
// able to fail/short-write at call N, return ENOSPC/EIO on demand, and drop
// unsynced writes on a simulated power cycle. Built in M1, reused by every
// later milestone.
//
// NOTE: this package is a deliberate addition to the CLAUDE.md layout
// (recorded in PROGRESS.md): the wrapper is cross-cutting (log, object
// store, outbox receipts, views) so it lives beside them, not inside log.
package fsx

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// File is the subset of *os.File the durability paths need.
type File interface {
	io.Writer
	io.Reader
	io.Closer
	// Sync flushes file DATA to stable storage (fsync/fdatasync class; on
	// darwin Go's os.File.Sync issues F_FULLFSYNC, which is what the
	// durability claim needs).
	Sync() error
	// Truncate changes the file size (recovery truncates a trailing
	// invalid frame).
	Truncate(size int64) error
	// Seek positions the next read/write.
	Seek(offset int64, whence int) (int64, error)
}

// FS is the filesystem interface over which the log, object store, and
// receipt paths run.
type FS interface {
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	MkdirAll(path string, perm os.FileMode) error
	ReadDir(name string) ([]fs.DirEntry, error)
	ReadFile(name string) ([]byte, error)
	Stat(name string) (fs.FileInfo, error)
	// SyncDir fsyncs a directory so its entries (creates/renames) are durable.
	SyncDir(name string) error
}

// OS is the production implementation backed by the real filesystem.
type OS struct{}

func (OS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	return os.OpenFile(name, flag, perm)
}
func (OS) Rename(oldpath, newpath string) error         { return os.Rename(oldpath, newpath) }
func (OS) Remove(name string) error                     { return os.Remove(name) }
func (OS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (OS) ReadDir(name string) ([]fs.DirEntry, error)   { return os.ReadDir(name) }
func (OS) ReadFile(name string) ([]byte, error)         { return os.ReadFile(name) }
func (OS) Stat(name string) (fs.FileInfo, error)        { return os.Stat(name) }

func (OS) SyncDir(name string) error {
	d, err := os.Open(name)
	if err != nil {
		return err
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return serr
	}
	return cerr
}

// WriteFileAtomic is THE durable-publish primitive (rulings §3.1): write to
// a temp name in the same directory → fsync the file → rename to the final
// name → fsync the directory. Used for objects (M2), seal sidecars, and
// receipts (M4). Never overwrites: fails if final exists (callers that allow
// idempotent re-publish check first).
func WriteFileAtomic(fsys FS, path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp := path + ".tmp"
	// A stale temp from an interrupted earlier attempt is always discardable
	// (crash rows 1–2: "orphan temp removable") — clear it so retries work.
	fsys.Remove(tmp)
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		fsys.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		fsys.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		fsys.Remove(tmp)
		return err
	}
	if _, err := fsys.Stat(path); err == nil {
		fsys.Remove(tmp)
		return &os.PathError{Op: "rename", Path: path, Err: os.ErrExist}
	}
	if err := fsys.Rename(tmp, path); err != nil {
		fsys.Remove(tmp)
		return err
	}
	return fsys.SyncDir(dir)
}
