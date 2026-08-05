package fsx

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrCrashed is returned by every operation after a simulated crash point:
// from the crash on, NOTHING mutates — like the process being gone.
var ErrCrashed = errors.New("fsx: simulated crash")

// Op identifies the operation for fault-injection matching.
type Op string

const (
	OpOpen    Op = "open"
	OpWrite   Op = "write"
	OpSync    Op = "sync"
	OpClose   Op = "close"
	OpRename  Op = "rename"
	OpRemove  Op = "remove"
	OpMkdir   Op = "mkdir"
	OpSyncDir Op = "syncdir"
	OpRead    Op = "read"
	OpStat    Op = "stat"
	OpTrunc   Op = "truncate"
	OpSeek    Op = "seek"
)

// mutating reports whether an op counts toward crash/fault schedules.
// Read-only ops never mutate state, so counting them would make crash-point
// enumeration depend on incidental reads.
func (o Op) mutating() bool {
	switch o {
	case OpWrite, OpSync, OpRename, OpRemove, OpMkdir, OpSyncDir, OpTrunc:
		return true
	}
	return false
}

type inode struct {
	cache   []byte // what the OS page cache would hold
	durable []byte // what survives a power cycle (last fsync)
}

// MemFS models durability precisely enough for the TESTING.md matrix:
//   - file DATA is durable only up to the last File.Sync
//   - namespace ENTRIES (create/rename/remove) are durable only after
//     SyncDir of the parent directory
//   - PowerCycle() discards everything not durable
//   - Crash(): freezes all state; later ops fail with ErrCrashed
//
// Simplification (documented): directories created by MkdirAll are durable
// immediately. The crash rows exercise file-entry durability; losing a
// leading empty directory is indistinguishable from losing the entry.
type MemFS struct {
	mu sync.Mutex

	inodes   map[string]*inode // cache namespace: path → inode
	durables map[string]*inode // durable namespace snapshot: path → same inode
	dirs     map[string]bool

	crashed  bool
	opCount  int // mutating ops seen so far
	crashAt  int // crash BEFORE mutating op N (1-based); 0 = never
	failAt   int // fail mutating op N once with failErr; 0 = never
	failErr  error
	shortLen int // on a failing write, bytes that still reach the cache
}

func NewMemFS() *MemFS {
	return &MemFS{
		inodes:   map[string]*inode{},
		durables: map[string]*inode{},
		dirs:     map[string]bool{"/": true},
	}
}

// --- fault scheduling -------------------------------------------------------

// CrashAt arranges a simulated crash immediately BEFORE the nth mutating
// operation from now (1-based). Combine with AfterCrash to restart.
func (m *MemFS) CrashAt(n int) { m.mu.Lock(); m.crashAt = m.opCount + n; m.mu.Unlock() }

// FailAt arranges the nth mutating operation from now to fail once with err
// (e.g. syscall.ENOSPC, syscall.EIO). If shortLen > 0 and the op is a write,
// that many bytes still land in the cache first (short write).
func (m *MemFS) FailAt(n int, err error, shortLen int) {
	m.mu.Lock()
	m.failAt, m.failErr, m.shortLen = m.opCount+n, err, shortLen
	m.mu.Unlock()
}

// MutatingOps returns how many mutating ops have run (for enumerating crash
// points: run once cleanly, read this, then re-run with CrashAt(i)).
func (m *MemFS) MutatingOps() int { m.mu.Lock(); defer m.mu.Unlock(); return m.opCount }

// PowerCycle simulates power loss: unsynced file data and unsynced namespace
// entries vanish. Clears any crash state (the machine has rebooted).
func (m *MemFS) PowerCycle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inodes = map[string]*inode{}
	for p, ino := range m.durables {
		ino.cache = append([]byte(nil), ino.durable...)
		m.inodes[p] = ino
	}
	m.crashed, m.crashAt, m.failAt = false, 0, 0
}

// Restart clears crash state WITHOUT losing the page cache — the SIGKILL
// model: the process dies but the OS (and its dirty pages) survive.
func (m *MemFS) Restart() {
	m.mu.Lock()
	m.crashed, m.crashAt, m.failAt = false, 0, 0
	m.mu.Unlock()
}

// Clone deep-copies the filesystem state (crash-matrix harness: build the
// base state once, clone per enumerated crash point). Inode aliasing between
// the cache and durable namespaces is preserved.
func (m *MemFS) Clone() *MemFS {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := NewMemFS()
	copied := map[*inode]*inode{}
	dup := func(old *inode) *inode {
		if n, ok := copied[old]; ok {
			return n
		}
		n := &inode{
			cache:   append([]byte(nil), old.cache...),
			durable: append([]byte(nil), old.durable...),
		}
		copied[old] = n
		return n
	}
	for p, ino := range m.inodes {
		c.inodes[p] = dup(ino)
	}
	for p, ino := range m.durables {
		c.durables[p] = dup(ino)
	}
	for d := range m.dirs {
		c.dirs[d] = true
	}
	return c
}

// enter gates every operation: crash schedule, one-shot fault, op counting.
func (m *MemFS) enter(op Op) error {
	if m.crashed {
		return ErrCrashed
	}
	if !op.mutating() {
		return nil
	}
	m.opCount++
	if m.crashAt > 0 && m.opCount >= m.crashAt {
		m.crashed = true
		return ErrCrashed
	}
	if m.failAt > 0 && m.opCount == m.failAt {
		m.failAt = 0
		return m.failErr
	}
	return nil
}

// --- FS implementation ------------------------------------------------------

func clean(p string) string { return filepath.Clean(p) }

func (m *MemFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter(OpOpen); err != nil {
		return nil, err
	}
	name = clean(name)
	ino, exists := m.inodes[name]
	if flag&os.O_CREATE != 0 {
		if exists && flag&os.O_EXCL != 0 {
			return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrExist}
		}
		if !exists {
			if !m.dirs[filepath.Dir(name)] {
				return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
			}
			ino = &inode{}
			m.inodes[name] = ino
		}
	} else if !exists {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrNotExist}
	}
	if flag&os.O_TRUNC != 0 {
		ino.cache = nil
	}
	h := &memFile{fs: m, path: name, ino: ino, writable: flag&(os.O_WRONLY|os.O_RDWR) != 0}
	if flag&os.O_APPEND != 0 {
		h.appendMode = true
		h.offset = int64(len(ino.cache))
	}
	return h, nil
}

func (m *MemFS) Rename(oldpath, newpath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter(OpRename); err != nil {
		return err
	}
	oldpath, newpath = clean(oldpath), clean(newpath)
	ino, ok := m.inodes[oldpath]
	if !ok {
		return &os.PathError{Op: "rename", Path: oldpath, Err: os.ErrNotExist}
	}
	m.inodes[newpath] = ino
	delete(m.inodes, oldpath)
	return nil
}

func (m *MemFS) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter(OpRemove); err != nil {
		return err
	}
	name = clean(name)
	if _, ok := m.inodes[name]; !ok {
		return &os.PathError{Op: "remove", Path: name, Err: os.ErrNotExist}
	}
	delete(m.inodes, name)
	return nil
}

// RemoveAll removes a path and all children (counts as ONE mutating op —
// rm -rf granularity matches the crash model well enough for bundles).
func (m *MemFS) RemoveAll(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter(OpRemove); err != nil {
		return err
	}
	path = clean(path)
	for p := range m.inodes {
		if p == path || strings.HasPrefix(p, path+"/") {
			delete(m.inodes, p)
		}
	}
	for d := range m.dirs {
		if d == path || strings.HasPrefix(d, path+"/") {
			delete(m.dirs, d)
		}
	}
	return nil
}

func (m *MemFS) MkdirAll(path string, perm os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter(OpMkdir); err != nil {
		return err
	}
	path = clean(path)
	for p := path; p != "/" && p != "."; p = filepath.Dir(p) {
		m.dirs[p] = true
	}
	return nil
}

func (m *MemFS) SyncDir(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter(OpSyncDir); err != nil {
		return err
	}
	name = clean(name)
	if !m.dirs[name] {
		return &os.PathError{Op: "syncdir", Path: name, Err: os.ErrNotExist}
	}
	// propagate adds/renames of DIRECT children
	for p, ino := range m.inodes {
		if filepath.Dir(p) == name {
			m.durables[p] = ino
		}
	}
	// propagate removals of direct children
	for p := range m.durables {
		if filepath.Dir(p) == name {
			if _, still := m.inodes[p]; !still {
				delete(m.durables, p)
			}
		}
	}
	return nil
}

func (m *MemFS) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter(OpRead); err != nil {
		return nil, err
	}
	ino, ok := m.inodes[clean(name)]
	if !ok {
		return nil, &os.PathError{Op: "read", Path: name, Err: os.ErrNotExist}
	}
	return append([]byte(nil), ino.cache...), nil
}

func (m *MemFS) ReadDir(name string) ([]fs.DirEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter(OpRead); err != nil {
		return nil, err
	}
	name = clean(name)
	if !m.dirs[name] {
		return nil, &os.PathError{Op: "readdir", Path: name, Err: os.ErrNotExist}
	}
	seen := map[string]fs.DirEntry{}
	for p, ino := range m.inodes {
		if filepath.Dir(p) == name {
			seen[filepath.Base(p)] = memDirEntry{name: filepath.Base(p), size: int64(len(ino.cache))}
		}
	}
	for d := range m.dirs {
		if filepath.Dir(d) == name {
			seen[filepath.Base(d)] = memDirEntry{name: filepath.Base(d), dir: true}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]fs.DirEntry, len(names))
	for i, n := range names {
		out[i] = seen[n]
	}
	return out, nil
}

func (m *MemFS) Stat(name string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.enter(OpStat); err != nil {
		return nil, err
	}
	name = clean(name)
	if ino, ok := m.inodes[name]; ok {
		return memFileInfo{name: filepath.Base(name), size: int64(len(ino.cache))}, nil
	}
	if m.dirs[name] {
		return memFileInfo{name: filepath.Base(name), dir: true}, nil
	}
	return nil, &os.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
}

// DurableExists reports whether a path would survive a power cycle (test helper).
func (m *MemFS) DurableExists(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.durables[clean(name)]
	return ok
}

// --- file handle ------------------------------------------------------------

type memFile struct {
	fs       *MemFS
	path     string
	ino      *inode
	offset   int64
	writable bool
	// appendMode mirrors POSIX O_APPEND: EVERY write lands at the current
	// EOF, not at a tracked offset. The distinction matters once a live
	// handle truncates and keeps writing (log append rollback): offset-based
	// emulation would leave a zero-filled gap real O_APPEND cannot produce.
	appendMode bool
	closed     bool
}

func (f *memFile) Write(p []byte) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if !f.writable {
		return 0, syscall.EBADF
	}
	if err := f.fs.enter(OpWrite); err != nil {
		if err != ErrCrashed && f.fs.shortLen > 0 && f.fs.shortLen < len(p) {
			n := f.fs.shortLen
			f.fs.shortLen = 0
			f.writeLocked(p[:n])
			return n, err
		}
		return 0, err
	}
	f.writeLocked(p)
	return len(p), nil
}

func (f *memFile) writeLocked(p []byte) {
	if f.appendMode {
		f.offset = int64(len(f.ino.cache))
	}
	end := f.offset + int64(len(p))
	if int64(len(f.ino.cache)) < end {
		grown := make([]byte, end)
		copy(grown, f.ino.cache)
		f.ino.cache = grown
	}
	copy(f.ino.cache[f.offset:end], p)
	f.offset = end
}

func (f *memFile) Read(p []byte) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if err := f.fs.enter(OpRead); err != nil {
		return 0, err
	}
	if f.offset >= int64(len(f.ino.cache)) {
		return 0, io.EOF
	}
	n := copy(p, f.ino.cache[f.offset:])
	f.offset += int64(n)
	return n, nil
}

func (f *memFile) Sync() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.closed {
		return os.ErrClosed
	}
	if err := f.fs.enter(OpSync); err != nil {
		return err
	}
	f.ino.durable = append([]byte(nil), f.ino.cache...)
	return nil
}

func (f *memFile) Truncate(size int64) error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.closed {
		return os.ErrClosed
	}
	if err := f.fs.enter(OpTrunc); err != nil {
		return err
	}
	if int64(len(f.ino.cache)) > size {
		f.ino.cache = f.ino.cache[:size]
	}
	return nil
}

func (f *memFile) Seek(offset int64, whence int) (int64, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if err := f.fs.enter(OpSeek); err != nil {
		return 0, err
	}
	switch whence {
	case io.SeekStart:
		f.offset = offset
	case io.SeekCurrent:
		f.offset += offset
	case io.SeekEnd:
		f.offset = int64(len(f.ino.cache)) + offset
	}
	return f.offset, nil
}

func (f *memFile) Close() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.closed {
		return os.ErrClosed
	}
	// Close never counts as a fault point (closing loses no durable state),
	// but after a crash it must not succeed silently.
	if f.fs.crashed {
		return ErrCrashed
	}
	f.closed = true
	return nil
}

// --- fs.FileInfo / fs.DirEntry ----------------------------------------------

type memFileInfo struct {
	name string
	size int64
	dir  bool
}

func (i memFileInfo) Name() string { return i.name }
func (i memFileInfo) Size() int64  { return i.size }
func (i memFileInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o700
	}
	return 0o644
}
func (i memFileInfo) ModTime() time.Time { return time.Time{} }
func (i memFileInfo) IsDir() bool        { return i.dir }
func (i memFileInfo) Sys() any           { return nil }

type memDirEntry struct {
	name string
	size int64
	dir  bool
}

func (e memDirEntry) Name() string { return e.name }
func (e memDirEntry) IsDir() bool  { return e.dir }
func (e memDirEntry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0
}
func (e memDirEntry) Info() (fs.FileInfo, error) {
	return memFileInfo(e), nil
}

var _ FS = (*MemFS)(nil)
var _ FS = OS{}
