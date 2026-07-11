package identity

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

// VolumeStatus is the result of the at-rest encryption check (rulings §9).
// Unknown/indeterminate FAILS CLOSED: the daemon refuses to start without
// the persisted --allow-unencrypted operator override.
type VolumeStatus int

const (
	VolumeUnknown VolumeStatus = iota
	VolumeEncrypted
	VolumeUnencrypted
)

func (s VolumeStatus) String() string {
	switch s {
	case VolumeEncrypted:
		return "encrypted"
	case VolumeUnencrypted:
		return "unencrypted"
	default:
		return "unknown"
	}
}

// VolumeChecker reports the encryption status of the volume holding path.
// Injectable so tests can simulate all three states.
type VolumeChecker interface {
	Status(path string) (VolumeStatus, string, error)
}

// SystemVolumeChecker picks the platform implementation: macOS arm64 primary
// (`fdesetup status`), Linux best-effort (dm-crypt via lsblk), anything else
// unknown → fail closed.
type SystemVolumeChecker struct{}

func (SystemVolumeChecker) Status(path string) (VolumeStatus, string, error) {
	// Fault-injection hook for the TESTING.md volume-state rows (encrypted /
	// unencrypted / indeterminate) at the process boundary. Never set outside
	// tests.
	switch os.Getenv("CAIRN_FAKE_VOLUME_STATUS") {
	case "encrypted":
		return VolumeEncrypted, "injected by CAIRN_FAKE_VOLUME_STATUS", nil
	case "unencrypted":
		return VolumeUnencrypted, "injected by CAIRN_FAKE_VOLUME_STATUS", nil
	case "unknown":
		return VolumeUnknown, "injected by CAIRN_FAKE_VOLUME_STATUS", nil
	}
	switch runtime.GOOS {
	case "darwin":
		return darwinStatus(path)
	case "linux":
		return linuxStatus(path)
	default:
		return VolumeUnknown, fmt.Sprintf("no encryption check for %s", runtime.GOOS), nil
	}
}

// darwinStatus: `fdesetup status` reports FileVault for the boot container
// (System + Data volumes, encrypted together). If path is on neither, the
// answer would be about the wrong disk, so we report unknown (fail closed)
// rather than guess about e.g. an external drive.
func darwinStatus(path string) (VolumeStatus, string, error) {
	same, err := onBootContainer(path)
	if err != nil {
		return VolumeUnknown, "cannot stat path", err
	}
	if !same {
		return VolumeUnknown, "path is not on the boot volume; fdesetup cannot answer for it", nil
	}
	out, err := exec.Command("fdesetup", "status").CombinedOutput()
	if err != nil {
		return VolumeUnknown, "fdesetup failed: " + strings.TrimSpace(string(out)), nil
	}
	return parseFdesetup(string(out))
}

func parseFdesetup(out string) (VolumeStatus, string, error) {
	switch {
	case strings.Contains(out, "FileVault is On"):
		return VolumeEncrypted, "FileVault is On", nil
	case strings.Contains(out, "FileVault is Off"):
		return VolumeUnencrypted, "FileVault is Off", nil
	default:
		return VolumeUnknown, "unrecognized fdesetup output: " + strings.TrimSpace(out), nil
	}
}

// linuxStatus (best-effort per rulings §9): resolve the backing source with
// findmnt, then walk its device chain with lsblk looking for a dm-crypt layer.
func linuxStatus(path string) (VolumeStatus, string, error) {
	src, err := exec.Command("findmnt", "-no", "SOURCE", "--target", path).Output()
	if err != nil {
		return VolumeUnknown, "findmnt failed", nil
	}
	source := strings.TrimSpace(string(src))
	if source == "" {
		return VolumeUnknown, "no mount source for path", nil
	}
	out, err := exec.Command("lsblk", "-sno", "TYPE", source).Output()
	if err != nil {
		return VolumeUnknown, "lsblk failed for " + source, nil
	}
	types := strings.Fields(string(out))
	for _, t := range types {
		if t == "crypt" {
			return VolumeEncrypted, "dm-crypt layer under " + source, nil
		}
	}
	if len(types) == 0 {
		return VolumeUnknown, "lsblk reported no device chain", nil
	}
	return VolumeUnencrypted, "no dm-crypt layer under " + source, nil
}

// onBootContainer: true if path shares a device with the System volume (/)
// or the Data volume (/System/Volumes/Data) — FileVault encrypts both.
func onBootContainer(path string) (bool, error) {
	var p syscall.Stat_t
	if err := syscall.Stat(path, &p); err != nil {
		return false, err
	}
	for _, ref := range []string{"/", "/System/Volumes/Data"} {
		var r syscall.Stat_t
		if err := syscall.Stat(ref, &r); err != nil {
			continue // Data volume may not exist on older layouts
		}
		if p.Dev == r.Dev {
			return true, nil
		}
	}
	return false, nil
}
