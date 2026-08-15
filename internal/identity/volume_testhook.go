//go:build cairn_testhooks

package identity

import "os"

// Fault-injection hook for the TESTING.md volume-state rows (encrypted /
// unencrypted / indeterminate). It is compiled ONLY under the
// `cairn_testhooks` build tag, which `make test` / `make vet` set and
// `make build` deliberately does not — so a release binary has no way to
// be told "this volume is encrypted" by its environment.
//
// This used to be unconditional. In a release build that made
// CAIRN_FAKE_VOLUME_STATUS=encrypted a silent bypass of the encryption
// gate: unlike --allow-unencrypted, which persists an override and warns
// on every start, the injected status looked exactly like a real pass.
func fakeVolumeStatus() (VolumeStatus, string, bool) {
	switch os.Getenv("CAIRN_FAKE_VOLUME_STATUS") {
	case "encrypted":
		return VolumeEncrypted, "injected by CAIRN_FAKE_VOLUME_STATUS", true
	case "unencrypted":
		return VolumeUnencrypted, "injected by CAIRN_FAKE_VOLUME_STATUS", true
	case "unknown":
		return VolumeUnknown, "injected by CAIRN_FAKE_VOLUME_STATUS", true
	}
	return VolumeUnknown, "", false
}
