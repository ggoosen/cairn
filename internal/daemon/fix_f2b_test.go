package daemon_test

// F2(b): identity show works identically before and after migration —
// mesh-level trust, not per-origin genesis (Codex AUDIT-005).

import (
	"io"
	"testing"

	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
)

// F2(b): identity show works identically before and after migration
// (mesh-level trust, not per-origin genesis) — Codex AUDIT-005.
func TestF2IdentityShowPostMigrate(t *testing.T) {
	dir := initCairn(t)
	publishOne(t, dir, "a message before migrating")
	if _, err := identity.Migrate(identity.MigrateOptions{Dir: dir, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}

	trust, err := identity.MeshTrust(fsx.OS{}, dir)
	if err != nil {
		t.Fatalf("RED (auditor scenario): mesh trust unresolvable post-migrate: %v", err)
	}
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if trust.GenesisEnv == nil || trust.GenesisPayload == nil {
		t.Fatal("genesis not located")
	}
	// the CURRENT (post-migrate) device must be a verified member
	if !trust.Member(loaded.Device.DeviceID) {
		t.Fatalf("current device %s not in the mesh membership set", loaded.Device.DeviceID)
	}
	if trust.Revoked(loaded.Device.DeviceID) {
		t.Fatal("current device wrongly revoked")
	}
	// and the OLD device is revoked
	revokedAny := false
	for _, id := range trust.Devices() {
		if id != loaded.Device.DeviceID && trust.Revoked(id) {
			revokedAny = true
		}
	}
	if !revokedAny {
		t.Fatal("old device not marked revoked in mesh trust")
	}
}
