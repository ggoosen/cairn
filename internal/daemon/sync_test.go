package daemon_test

// N5 acceptance, end to end: the two-node enrolment ceremony with the real
// offline root-key round-trip; the daemon-wired sync listener; un-enrolled
// tailnet peer refused (and logged); revoked peer refused after
// device.revoke lands. Two machines = two CAIRN_DEVICE_STATE_DIR bases.

import (
	"context"
	"crypto/ed25519"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/peer"
)

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// runListeningDaemon starts A's daemon with the sync listener and waits for
// the bound address.
func runListeningDaemon(t *testing.T, dir string, warn io.Writer) (*daemon.Daemon, context.CancelFunc, string) {
	t.Helper()
	d, err := daemon.Start(daemon.Options{Dir: dir, Warn: warn})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx, func() error { return nil })
	deadline := time.Now().Add(3 * time.Second)
	for d.SyncAddr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("sync listener never bound")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return d, cancel, d.SyncAddr()
}

func TestN5TwoNodeCeremonyAndRefusals(t *testing.T) {
	baseA, baseB := t.TempDir(), t.TempDir()
	t.Setenv("CAIRN_FAKE_VOLUME_STATUS", "encrypted")
	t.Setenv("CAIRN_SYNC_ALLOW_LOOPBACK", "1") // dev binding for the test
	tmp := t.TempDir()
	reqPath := filepath.Join(tmp, "req.json")
	grantPath := filepath.Join(tmp, "grant.json")
	rootRestored := filepath.Join(tmp, "restored-root.key")

	// ---- machine A: init; root key goes OFFLINE --------------------------
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	dirA := filepath.Join(t.TempDir(), "cairnA")
	if _, err := identity.Initialize(identity.InitOptions{Dir: dirA, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	loadedA, err := identity.Load(dirA)
	if err != nil {
		t.Fatal(err)
	}
	rootLocal := filepath.Join(loadedA.DeviceDir, config.RootKeyName)
	blob, _ := os.ReadFile(rootLocal)
	os.WriteFile(rootRestored, blob, 0o600)
	os.Remove(rootLocal) // offline: no root key on the running node

	// configure the sync listener (loopback stands in for the tailnet here;
	// ValidateListenAddr enforces tailnet-only in production)
	loadedA.Device.SyncListen = "127.0.0.1:0"
	if err := loadedA.Device.SaveDevice(loadedA.DeviceDir); err != nil {
		t.Fatal(err)
	}

	// ---- machine B: enroll -------------------------------------------------
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	if _, err := identity.CreateEnrollRequest("macbook-air", reqPath, time.Now()); err != nil {
		t.Fatal(err)
	}

	// ---- machine A: approve with the restored key (daemon stopped) --------
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	grant, err := identity.Approve(identity.ApproveOptions{
		Dir: dirA, RequestPath: reqPath, RootKeyPath: rootRestored,
		GrantPath: grantPath, Out: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}

	// ---- machine B: join ----------------------------------------------------
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseB)
	dirB := filepath.Join(t.TempDir(), "cairnB")
	if err := identity.Join(grantPath, dirB, io.Discard); err != nil {
		t.Fatal(err)
	}
	loadedB, err := identity.Load(dirB)
	if err != nil {
		t.Fatal(err)
	}
	privB, err := identity.LoadKey(filepath.Join(loadedB.DeviceDir, config.DeviceKeyName))
	if err != nil {
		t.Fatal(err)
	}
	trustB, err := identity.BootstrapTrust(loadedB.DeviceDir)
	if err != nil {
		t.Fatal(err)
	}
	identB := peer.Identity{CairnID: loadedB.Portable.CairnID, DeviceID: loadedB.Device.DeviceID, Priv: privB}

	// ---- A serves; B joins the mesh over the wire ---------------------------
	t.Setenv("CAIRN_DEVICE_STATE_DIR", baseA)
	warn := &syncBuf{}
	d, cancel, addr := runListeningDaemon(t, dirA, warn)

	peerDev, err := peer.Ping(addr, identB, trustB)
	if err != nil {
		t.Fatalf("enrolled node refused: %v\n%s", err, warn.String())
	}
	if peerDev != loadedA.Device.DeviceID {
		t.Fatalf("authenticated wrong responder: %s", peerDev)
	}

	// ---- un-enrolled tailnet peer: refused AND logged -----------------------
	_, strangerPriv, _ := ed25519.GenerateKey(nil)
	stranger := peer.Identity{CairnID: loadedA.Portable.CairnID, DeviceID: "0190dead-0000-7000-8000-000000000bad", Priv: strangerPriv}
	if _, err := peer.Ping(addr, stranger, trustB); err == nil {
		t.Fatal("un-enrolled peer accepted")
	}
	if !strings.Contains(warn.String(), stranger.DeviceID) || !strings.Contains(warn.String(), "not enrolled") {
		t.Fatalf("refusal did not log the presented identity:\n%s", warn.String())
	}

	// ---- revoke B (offline root-signed), restart, refused -------------------
	cancel()
	d.Close()
	if _, err := identity.RevokeDevice(dirA, grant.DeviceID, rootRestored, "compromised", nil, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	warn2 := &syncBuf{}
	d2, cancel2, addr2 := runListeningDaemon(t, dirA, warn2)
	defer func() {
		cancel2()
		d2.Close()
	}()
	if _, err := peer.Ping(addr2, identB, trustB); err == nil {
		t.Fatal("revoked peer accepted after device.revoke")
	}
	if !strings.Contains(warn2.String(), grant.DeviceID) || !strings.Contains(warn2.String(), "REVOKED") {
		t.Fatalf("revoked refusal did not log the identity:\n%s", warn2.String())
	}
}
