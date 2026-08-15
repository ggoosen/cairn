//go:build !cairn_testhooks

package identity

// Release builds have no volume-status injection hook at all: the real
// platform check is the only path to a VolumeEncrypted verdict. See
// volume_testhook.go for why this is split.
func fakeVolumeStatus() (VolumeStatus, string, bool) {
	return VolumeUnknown, "", false
}
