package identity

import (
	"fmt"
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
	switch runtime.GOOS {
	case "darwin":
		return darwinStatus(path)
	case "linux":
		return linuxStatus(path)
	default:
		return VolumeUnknown, fmt.Sprintf("no encryption check for %s", runtime.GOOS), nil
	}
}

// darwinStatus: `fdesetup status` reports FileVault for the boot volume.
// If path is NOT on the boot volume the answer would be about the wrong
// disk, so we report unknown (fail closed) rather than guess.
func darwinStatus(path string) (VolumeStatus, string, error) {
	same, err := sameDeviceAsRoot(path)
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

func sameDeviceAsRoot(path string) (bool, error) {
	var a, b syscall.Stat_t
	if err := syscall.Stat(path, &a); err != nil {
		return false, err
	}
	if err := syscall.Stat("/", &b); err != nil {
		return false, err
	}
	return a.Dev == b.Dev, nil
}
