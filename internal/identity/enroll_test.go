package identity

// N5 ceremony unit coverage: enroll → approve (offline root key) → join;
// R28 expiry + single-use; root-signed revocation. Two "machines" are
// simulated with two CAIRN_DEVICE_STATE_DIR bases.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

func TestN5EnrolmentCeremony(t *testing.T) {
	baseA, baseB := t.TempDir(), t.TempDir()
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	tmp := t.TempDir()
	reqPath := filepath.Join(tmp, "req.json")
	grantPath := filepath.Join(tmp, "grant.json")
	rootRestored := filepath.Join(tmp, "restored-root.key")

	// machine A: init the mesh
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := Initialize(InitOptions{Dir: dirA, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	loadedA, err := Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	// operator exports the root key offline (simulate: move it out)
	rootLocal := filepath.Join(loadedA.DeviceDir, config.RootKeyName)
	blob, err := os.ReadFile(rootLocal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootRestored, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(rootLocal); err != nil {
		t.Fatal(err)
	}

	// machine B: enroll (keypair stays under baseB)
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	_, err = CreateEnrollRequest("macbook-air", reqPath, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// machine A: approve with the RESTORED key
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	grant, err := Approve(ApproveOptions{
		Dir: dirA, RequestPath: reqPath, RootKeyPath: rootRestored,
		GrantPath: grantPath, Out: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if grant.DeviceID == "" || len(grant.Chain) < 2 {
		t.Fatalf("grant incomplete: %+v", grant)
	}
	// the new device is now in A's verified trust
	trustA, err := MeshTrust(fsx.OS{}, dirA)
	if err != nil {
		t.Fatal(err)
	}
	if !trustA.Member(grant.DeviceID) {
		t.Fatal("approved device not admitted on A")
	}

	// R28 single-use: approving the same request again is refused
	if _, err := Approve(ApproveOptions{
		Dir: dirA, RequestPath: reqPath, RootKeyPath: rootRestored,
		GrantPath: grantPath + ".2", Out: io.Discard,
	}); err == nil {
		t.Fatal("request approved twice (R28)")
	}

	// machine B: join
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	dirB := filepath.Join(t.TempDir(), "cairnB")
	if err := Join(grantPath, dirB, io.Discard); err != nil {
		t.Fatal(err)
	}
	loadedB, err := Load(dirB)
	if err != nil {
		t.Fatalf("joined node identity does not load: %v", err)
	}
	if loadedB.Device.DeviceID != grant.DeviceID || loadedB.Portable.CairnID != grant.CairnID {
		t.Fatalf("joined identity mismatch: %+v", loadedB.Device)
	}
	trustB, err := BootstrapTrust(loadedB.DeviceDir)
	if err != nil {
		t.Fatal(err)
	}
	if !trustB.Member(loadedA.Device.DeviceID) || !trustB.Member(grant.DeviceID) {
		t.Fatal("bootstrap trust missing members")
	}

	// machine A: root-signed revocation of B
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	if _, err := RevokeDevice(dirA, grant.DeviceID, rootRestored, "test", nil, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	trustA, err = MeshTrust(fsx.OS{}, dirA)
	if err != nil {
		t.Fatal(err)
	}
	if !trustA.Revoked(grant.DeviceID) {
		t.Fatal("revocation not in verified trust")
	}
	// double revoke refused; self revoke refused
	if _, err := RevokeDevice(dirA, grant.DeviceID, rootRestored, "again", nil, nil, io.Discard); err == nil {
		t.Fatal("double revoke accepted")
	}
	if _, err := RevokeDevice(dirA, loadedA.Device.DeviceID, rootRestored, "self", nil, nil, io.Discard); err == nil {
		t.Fatal("self revoke accepted")
	}
}

func TestN5RequestExpiryAndTamper(t *testing.T) {
	baseA := t.TempDir()
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := Initialize(InitOptions{Dir: dirA, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	loadedA, err := Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	rootKey := filepath.Join(loadedA.DeviceDir, config.RootKeyName)

	tmp := t.TempDir()
	reqPath := filepath.Join(tmp, "req.json")
	if _, err := CreateEnrollRequest("expired", reqPath, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// R28: expired request refused
	if _, err := Approve(ApproveOptions{
		Dir: dirA, RequestPath: reqPath, RootKeyPath: rootKey,
		GrantPath: filepath.Join(tmp, "g.json"), Out: io.Discard,
	}); err == nil {
		t.Fatal("expired request approved (R28)")
	}

	// wrong root key refused
	if _, err := CreateEnrollRequest("fresh", reqPath, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, wrongPriv, _ := GenerateKey()
	wrongPath := filepath.Join(tmp, "wrong.key")
	if err := SaveKey(wrongPath, wrongPriv); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ApproveOptions{
		Dir: dirA, RequestPath: reqPath, RootKeyPath: wrongPath,
		GrantPath: filepath.Join(tmp, "g.json"), Out: io.Discard,
	}); err == nil {
		t.Fatal("wrong root key accepted")
	}

	// tampered grant chain refused at join
	grantPath := filepath.Join(tmp, "grant.json")
	grant, err := Approve(ApproveOptions{
		Dir: dirA, RequestPath: reqPath, RootKeyPath: rootKey,
		GrantPath: grantPath, Out: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	grant.Chain = grant.Chain[1:] // drop genesis
	blob, _ := json.Marshal(grant)
	os.WriteFile(grantPath, blob, 0o600)
	if err := Join(grantPath, filepath.Join(t.TempDir(), "cairnB"), io.Discard); err == nil {
		t.Fatal("tampered grant accepted")
	}
}
