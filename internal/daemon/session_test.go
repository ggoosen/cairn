package daemon_test

// N2 acceptance: capability enforcement at the dispatch boundary.
// - a read-only session's send is refused pre-ack with a capability error
// - handle expiry (TTL and idle) ends access
// - telemetry rows carry the principal hierarchy
// - handles are non-delegable and override the client-supplied actor
// - device-local profiles.toml adds profiles strictly

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/telemetry"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// serveSession starts a daemon (with an injectable clock) serving IPC and
// returns a Call helper — sessions only exist at the dispatch boundary.
func serveSession(t *testing.T, dir string, clock *fakeClock) func(daemon.Request) (*daemon.Response, error) {
	t.Helper()
	opts := daemon.Options{Dir: dir}
	if clock != nil {
		opts.Now = clock.Now
	}
	d, err := daemon.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	t.Cleanup(func() {
		cancel()
		d.Close()
	})
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	callD := func(req daemon.Request) (*daemon.Response, error) {
		return daemon.Call(loaded.DeviceDir, req)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := callD(daemon.Request{Op: "status"}); err == nil {
			return callD
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never became reachable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mkSession(t *testing.T, callD func(daemon.Request) (*daemon.Response, error), profile, name string) string {
	t.Helper()
	resp, err := callD(daemon.Request{Op: "session-create", SessionProfile: profile, SessionName: name})
	if err != nil {
		t.Fatalf("session-create %s: %v", profile, err)
	}
	return resp.Status["session"].(string)
}

func nextSeq(t *testing.T, callD func(daemon.Request) (*daemon.Response, error)) float64 {
	t.Helper()
	resp, err := callD(daemon.Request{Op: "status"})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Status["next_seq"].(float64)
}

func TestN2ReadOnlySendRefusedPreAck(t *testing.T) {
	dir := initCairn(t)
	callD := serveSession(t, dir, nil)
	token := mkSession(t, callD, "read-only", "ro-agent")

	base := nextSeq(t, callD)
	_, err := callD(daemon.Request{Op: "publish", Session: token,
		Publish: &daemon.PublishRequest{Actor: "ro-agent", Body: "should never land"}})
	if err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("read-only send not refused with capability error: %v", err)
	}
	if got := nextSeq(t, callD); got != base {
		t.Fatalf("send was refused but next_seq moved %v → %v (not pre-ack!)", base, got)
	}

	// admin and signal ops equally refused
	for _, req := range []daemon.Request{
		{Op: "retract", Session: token, MessageID: "0190a1b2-c3d4-7e5f-8901-234567890abc"},
		{Op: "topic-ensure", Session: token, TopicName: "sneaky"},
		{Op: "signal", Session: token, MessageID: "x", Kind: "ack"},
		{Op: "housekeep", Session: token},
	} {
		if _, err := callD(req); err == nil || !strings.Contains(err.Error(), "capability") {
			t.Fatalf("op %s not capability-refused for read-only: %v", req.Op, err)
		}
	}

	// reads still work
	if _, err := callD(daemon.Request{Op: "search", Session: token, Query: "anything"}); err != nil {
		t.Fatalf("read-only search refused: %v", err)
	}
	if _, err := callD(daemon.Request{Op: "digest", Session: token, AgentView: "ro-agent", BudgetChars: 500}); err != nil {
		t.Fatalf("read-only digest refused: %v", err)
	}
}

func TestN2ExpiryAndIdleEndAccess(t *testing.T) {
	dir := initCairn(t)
	clock := &fakeClock{t: time.Now()}
	callD := serveSession(t, dir, clock)

	// idle: past the idle window but within TTL
	idle := mkSession(t, callD, "agent-standard", "idler")
	if _, err := callD(daemon.Request{Op: "publish", Session: idle,
		Publish: &daemon.PublishRequest{Body: "before idle"}}); err != nil {
		t.Fatalf("fresh session publish: %v", err)
	}
	clock.Advance(config.SessionIdleTimeout + time.Hour)
	_, err := callD(daemon.Request{Op: "publish", Session: idle,
		Publish: &daemon.PublishRequest{Body: "after idle"}})
	if err == nil || !strings.Contains(err.Error(), "idle") {
		t.Fatalf("idle session not revoked: %v", err)
	}

	// TTL: touched continuously, expiry still ends access
	ttl := mkSession(t, callD, "agent-standard", "long-runner")
	step := config.SessionIdleTimeout / 2
	for elapsed := time.Duration(0); elapsed <= config.SessionTTLDefault; elapsed += step {
		clock.Advance(step)
		callD(daemon.Request{Op: "search", Session: ttl, Query: "keepalive"})
	}
	// the keepalive that crossed the TTL already deleted the handle, so the
	// refusal reads either "expired" or "unknown or revoked" — both END access
	_, err = callD(daemon.Request{Op: "search", Session: ttl, Query: "past ttl"})
	if err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("expired session not refused: %v", err)
	}

	// a revoked/unknown token is a capability error too
	_, err = callD(daemon.Request{Op: "search", Session: "deadbeef", Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("unknown token: %v", err)
	}
}

func TestN2NonDelegableActorOverrideAndTelemetryPrincipal(t *testing.T) {
	dir := initCairn(t)
	callD := serveSession(t, dir, nil)
	token := mkSession(t, callD, "agent-standard", "claude-a")

	// non-delegable: session-* under a session is refused outright
	for _, op := range []string{"session-create", "session-revoke", "session-list"} {
		_, err := callD(daemon.Request{Op: op, Session: token, SessionProfile: "read-only", TargetSession: token})
		if err == nil || !strings.Contains(err.Error(), "non-delegable") {
			t.Fatalf("%s under a session: %v", op, err)
		}
	}

	// actor spoof: the event's sender is the session's leaf principal
	resp, err := callD(daemon.Request{Op: "publish", Session: token,
		Publish: &daemon.PublishRequest{Actor: "operator", OperatorOverride: true, Body: "who am i"}})
	if err != nil {
		t.Fatal(err)
	}
	peek, err := callD(daemon.Request{Op: "peek", MessageID: resp.Publish.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	if peek.Message.Sender != "claude-a" {
		t.Fatalf("sender = %q, want the session principal claude-a (spoof not overridden)", peek.Message.Sender)
	}

	// telemetry rows carry the principal hierarchy (acceptance)
	sresp, err := callD(daemon.Request{Op: "search", Session: token, Query: "who am i"})
	if err != nil {
		t.Fatal(err)
	}
	dresp, err := callD(daemon.Request{Op: "digest", Session: token, AgentView: "claude-a", BudgetChars: 800})
	if err != nil {
		t.Fatal(err)
	}
	tel, err := telemetry.Open(telemetry.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()
	for _, id := range []string{sresp.Search.InteractionID, dresp.Digest.InteractionID} {
		principal, err := tel.Principal(id)
		if err != nil {
			t.Fatal(err)
		}
		if principal != "operator>claude-a" {
			t.Fatalf("telemetry principal = %q, want operator>claude-a", principal)
		}
	}
	// tier-1 rows record plain operator
	op, err := callD(daemon.Request{Op: "search", Query: "tier one"})
	if err != nil {
		t.Fatal(err)
	}
	if principal, _ := tel.Principal(op.Search.InteractionID); principal != "operator" {
		t.Fatalf("tier-1 principal = %q", principal)
	}
}

func TestN2ProfilesTOML(t *testing.T) {
	dir := initCairn(t)
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeProfiles := func(body string) {
		if err := os.WriteFile(filepath.Join(loaded.DeviceDir, config.ProfilesFileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeProfiles("[profiles.ci-bot]\ncapabilities = [\"read\", \"send\"]\n")
	callD := serveSession(t, dir, nil)
	token := mkSession(t, callD, "ci-bot", "ci")
	if _, err := callD(daemon.Request{Op: "publish", Session: token,
		Publish: &daemon.PublishRequest{Body: "ci can send"}}); err != nil {
		t.Fatalf("ci-bot send: %v", err)
	}
	if _, err := callD(daemon.Request{Op: "signal", Session: token, MessageID: "x", Kind: "ack"}); err == nil ||
		!strings.Contains(err.Error(), "capability") {
		t.Fatalf("ci-bot signal should be refused: %v", err)
	}

	// bad files fail daemon startup loudly
	for _, bad := range []string{
		"[profiles.full]\ncapabilities = [\"read\"]\n",              // builtin redefinition
		"[profiles.x]\ncapabilities = [\"root\"]\n",                 // unknown capability
		"[profiles.x]\ncapabilities = [\"read\"]\nmystery = true\n", // unknown key
	} {
		if _, err := daemon.LoadProfiles(func() string {
			tmp := t.TempDir()
			os.WriteFile(filepath.Join(tmp, config.ProfilesFileName), []byte(bad), 0o600)
			return tmp
		}()); err == nil {
			t.Fatalf("bad profiles.toml accepted:\n%s", bad)
		}
	}
}
