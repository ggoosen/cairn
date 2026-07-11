package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// Emergency reserve (rulings §11): a preallocated 64 MiB file per cairn.
// Ordinary sends can NEVER consume it (it simply occupies disk so ENOSPC
// hits ordinary traffic first). Only the explicit interactive operator
// command releases it, admitting ONE small text event ≤ 64 KiB. Declared
// priority is never authorization to use the reserve.

func reservePath(portableDir string) string {
	return filepath.Join(portableDir, config.DerivedDirName, "reserve.bin")
}

// releaseMarker lives DEVICE-LOCAL (an operator action, not portable data).
func releaseMarkerPath(deviceDir string) string {
	return filepath.Join(deviceDir, "reserve-released.json")
}

// EnsureReserve preallocates the reserve if absent (daemon start) — and
// re-preallocates after an emergency send when space allows.
func (d *Daemon) EnsureReserve() error {
	path := reservePath(d.dir)
	if fi, err := d.fs.Stat(path); err == nil && fi.Size() == int64(config.EmergencyReserveBytes) {
		return nil
	}
	if err := d.fs.MkdirAll(filepath.Dir(path), config.DirPerm); err != nil {
		return err
	}
	f, err := d.fs.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, config.FilePerm)
	if err != nil {
		return err
	}
	chunk := make([]byte, 1<<20)
	for written := 0; written < config.EmergencyReserveBytes; written += len(chunk) {
		if _, err := f.Write(chunk); err != nil {
			f.Close()
			d.fs.Remove(path + ".tmp")
			return fmt.Errorf("preallocating reserve: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	d.fs.Remove(path)
	if err := d.fs.Rename(path+".tmp", path); err != nil {
		return err
	}
	return d.fs.SyncDir(filepath.Dir(path))
}

// ReserveStatus reports the reserve file and release-grant state.
func (d *Daemon) ReserveStatus() (present bool, sizeBytes int64, releaseGranted bool) {
	if fi, err := d.fs.Stat(reservePath(d.dir)); err == nil {
		present, sizeBytes = true, fi.Size()
	}
	if _, err := os.Stat(releaseMarkerPath(d.loaded.DeviceDir)); err == nil {
		releaseGranted = true
	}
	return
}

// ReleaseReserve is the explicit interactive operator command: deletes the
// reserve file (freeing 64 MiB) and grants exactly ONE emergency send.
func (d *Daemon) ReleaseReserve() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := os.Stat(releaseMarkerPath(d.loaded.DeviceDir)); err == nil {
		return errors.New("an emergency release is already granted and unused")
	}
	if err := d.fs.Remove(reservePath(d.dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	marker, _ := json.Marshal(map[string]any{"granted_at": d.now().UTC().Format(config.WallTimeFormat)})
	return fsx.WriteFileAtomic(fsx.OS{}, releaseMarkerPath(d.loaded.DeviceDir), marker, config.KeyFilePerm)
}

// EmergencyPublish consumes the one-shot release grant for a single small
// text event. Size capped at EmergencyReleaseMaxBytes; the grant is consumed
// BEFORE the send (a failed send does not restore it — rerun ReleaseReserve).
// Afterwards the reserve is re-preallocated best-effort.
func (d *Daemon) EmergencyPublish(req PublishRequest) (*PublishResult, error) {
	if len(req.Body) > config.EmergencyReleaseMaxBytes {
		return nil, fmt.Errorf("emergency send is capped at %d bytes (got %d)", config.EmergencyReleaseMaxBytes, len(req.Body))
	}
	marker := releaseMarkerPath(d.loaded.DeviceDir)
	if _, err := os.Stat(marker); err != nil {
		return nil, errors.New("no emergency release granted: run `cairn reserve release` first (declared priority is never reserve authorization)")
	}
	if err := os.Remove(marker); err != nil {
		return nil, err
	}
	res, err := d.Publish(req)
	if err != nil {
		return nil, fmt.Errorf("emergency send failed (the release grant is consumed; rerun `cairn reserve release`): %w", err)
	}
	if rerr := d.EnsureReserve(); rerr != nil {
		fmt.Fprintf(d.warn, "WARNING: reserve not re-preallocated: %v\n", rerr)
	}
	return res, nil
}
