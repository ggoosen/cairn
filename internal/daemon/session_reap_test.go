package daemon_test

// D9 — capability sessions are never reaped.
//
// The defect: expiry was checked ONLY inside resolve(), i.e. only when the
// expired token was presented again, which a dead MCP client never does. The
// pid binding was recorded and never read. Nothing swept. On the dev node that
// produced 2,673 resident records, 1,524 of them expired, and a 772 KB
// sessions.json rewritten in full on every mint.
//
// The tests that matter here are the NEGATIVE ones. This fix REMOVES
// behaviour, and over-reaping revokes a running agent's handle mid-session —
// strictly worse than the leak it replaces. So: a live process inside its TTL
// is never reaped, not on list, not on mint, not across a daemon restart; and
// a pid binding from an unknown device is never trusted.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
)

// serveDaemon is serveSession's sibling that also hands back the Daemon (the
// mint-cost assertion reads a counter off it) and an explicit stop, so a test
// can restart the daemon over the same directory — only one writer at a time.
func serveSessionDaemon(t *testing.T, dir string) (*daemon.Daemon, func(daemon.Request) (*daemon.Response, error), func()) {
	t.Helper()
	d, err := daemon.Start(daemon.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Serve(ctx)
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		d.Close()
	}
	t.Cleanup(stop)
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
			return d, callD, stop
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never became reachable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// liveChild starts a process that will sit still until the test kills it, and
// returns its pid. It is a real process, not a fake: the whole point of the
// pid binding is that it tracks the OS.
func liveChild(t *testing.T) (pid int, kill func()) {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a helper process: %v", err)
	}
	killed := false
	kill = func() {
		if killed {
			return
		}
		killed = true
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	t.Cleanup(kill)
	return cmd.Process.Pid, kill
}

func sessionTokens(t *testing.T, callD func(daemon.Request) (*daemon.Response, error)) map[string]bool {
	t.Helper()
	resp, err := callD(daemon.Request{Op: "session-list"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := resp.Status["sessions"].([]any)
	out := map[string]bool{}
	for _, row := range rows {
		m, _ := row.(map[string]any)
		if p, ok := m["token_prefix"].(string); ok {
			out[p] = true
		}
	}
	return out
}

// THE test: a session whose process is alive and whose TTL has not run out is
// never reaped — not by the sweep on list, not by the sweep on mint, not by a
// daemon restart.
func TestD9LiveSessionIsNeverReaped(t *testing.T) {
	dir := initCairn(t)
	pid, _ := liveChild(t)

	_, callD, _ := serveSessionDaemon(t, dir)
	resp, err := callD(daemon.Request{Op: "session-create",
		SessionProfile: "agent-standard", SessionName: "live-agent", SessionPID: pid})
	if err != nil {
		t.Fatal(err)
	}
	token := resp.Status["session"].(string)

	// sweep on list, then sweep on mint (a second session), then use it
	if !sessionTokens(t, callD)[token[:8]] {
		t.Fatal("live session vanished from `session list`")
	}
	if _, err := callD(daemon.Request{Op: "session-create",
		SessionProfile: "read-only", SessionName: "other"}); err != nil {
		t.Fatal(err)
	}
	if !sessionTokens(t, callD)[token[:8]] {
		t.Fatal("live session reaped by the sweep on mint")
	}
	if _, err := callD(daemon.Request{Op: "search", Session: token, Query: "still granted"}); err != nil {
		t.Fatalf("live session no longer resolves: %v", err)
	}

	// and it survives a daemon restart (loadSessions reaps, and must not
	// reap this one)
	if _, err := callD(daemon.Request{Op: "session-prune"}); err != nil {
		t.Fatal(err)
	}
	if !sessionTokens(t, callD)[token[:8]] {
		t.Fatal("live session reaped by an explicit prune")
	}
}

func TestD9LiveSessionSurvivesDaemonRestart(t *testing.T) {
	dir := initCairn(t)
	pid, _ := liveChild(t)

	_, callD, stop := serveSessionDaemon(t, dir)
	resp, err := callD(daemon.Request{Op: "session-create",
		SessionProfile: "agent-standard", SessionName: "restarter", SessionPID: pid})
	if err != nil {
		t.Fatal(err)
	}
	token := resp.Status["session"].(string)
	stop()

	_, callD2, _ := serveSessionDaemon(t, dir)
	if !sessionTokens(t, callD2)[token[:8]] {
		t.Fatal("live session did not survive a daemon restart (loadSessions over-reaped)")
	}
	if _, err := callD2(daemon.Request{Op: "search", Session: token, Query: "after restart"}); err != nil {
		t.Fatalf("session no longer grants after the restart: %v", err)
	}
}

// The leak, reaped at source: a killed client (SIGKILL — nothing runs on its
// way out) leaves no resident session once the sweep runs.
func TestD9DeadProcessSessionIsReaped(t *testing.T) {
	dir := initCairn(t)
	pid, kill := liveChild(t)

	_, callD, _ := serveSessionDaemon(t, dir)
	resp, err := callD(daemon.Request{Op: "session-create",
		SessionProfile: "agent-standard", SessionName: "mcp", SessionPID: pid})
	if err != nil {
		t.Fatal(err)
	}
	token := resp.Status["session"].(string)
	if !sessionTokens(t, callD)[token[:8]] {
		t.Fatal("session not resident before the kill")
	}

	kill()
	if sessionTokens(t, callD)[token[:8]] {
		t.Fatal("session of a dead process is still resident after a sweep")
	}
	// and the handle no longer grants anything
	if _, err := callD(daemon.Request{Op: "search", Session: token, Query: "x"}); err == nil ||
		!strings.Contains(err.Error(), "capability") {
		t.Fatalf("reaped handle still resolves: %v", err)
	}
}

// A pid binding is only meaningful on the device that recorded it. Records
// written by an older daemon carry no device at all, so their pid is not
// trusted: they are reaped on expiry only. Conservative on purpose — the
// alternative is judging a foreign pid number against local processes.
func TestD9ForeignAndLegacyPidBindingsAreNotTrusted(t *testing.T) {
	dir := initCairn(t)
	pid, kill := liveChild(t)
	kill() // definitely dead, and its pid is very unlikely to be reused here

	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(12 * time.Hour).UTC().Format(config.WallTimeFormat)
	now := time.Now().UTC().Format(config.WallTimeFormat)
	writeSessions(t, loaded.DeviceDir, []map[string]any{
		{"token": strings.Repeat("a", 64), "name": "legacy", "profile": "agent-standard",
			"parent": "operator", "created_at": now, "expires_at": future, "bound_pid": pid},
		{"token": strings.Repeat("b", 64), "name": "elsewhere", "profile": "agent-standard",
			"parent": "operator", "created_at": now, "expires_at": future, "bound_pid": pid,
			"bound_device": "some-other-device"},
	})

	_, callD, _ := serveSessionDaemon(t, dir)
	got := sessionTokens(t, callD)
	if !got[strings.Repeat("a", 8)] || !got[strings.Repeat("b", 8)] {
		t.Fatalf("a pid binding this device cannot vouch for was reaped anyway: %v", got)
	}
}

// Acceptance: a daemon restarted with a sessions.json full of expired records
// loads a bounded set and rewrites the file smaller.
func TestD9ExpiredBacklogIsDroppedOnLoadAndFileShrinks(t *testing.T) {
	dir := initCairn(t)
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour).UTC().Format(config.WallTimeFormat)
	expired := time.Now().Add(-24 * time.Hour).UTC().Format(config.WallTimeFormat)
	future := time.Now().Add(12 * time.Hour).UTC().Format(config.WallTimeFormat)

	rows := []map[string]any{}
	for i := 0; i < 500; i++ {
		rows = append(rows, map[string]any{
			"token": tokenOf(i), "name": "mcp", "profile": "agent-standard",
			"parent": "operator", "created_at": past, "expires_at": expired,
		})
	}
	rows = append(rows, map[string]any{
		"token": strings.Repeat("f", 64), "name": "keeper", "profile": "agent-standard",
		"parent": "operator", "created_at": past, "expires_at": future,
	})
	path := writeSessions(t, loaded.DeviceDir, rows)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	_, callD, _ := serveSessionDaemon(t, dir)
	got := sessionTokens(t, callD)
	if len(got) != 1 || !got[strings.Repeat("f", 8)] {
		t.Fatalf("expired backlog survived load: %d resident", len(got))
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("sessions.json did not shrink: %d → %d bytes", before.Size(), after.Size())
	}
}

// Acceptance: `cairn session prune` retires the backlog and reports what it
// removed, per reason.
func TestD9PruneReportsWhatItRemoved(t *testing.T) {
	dir := initCairn(t)
	_, callD, _ := serveSessionDaemon(t, dir)

	// two sessions, two DIFFERENT processes: one survives the prune, one does
	// not. Binding both to the same pid would prove nothing about either.
	livePID, _ := liveChild(t)
	doomedPID, kill := liveChild(t)
	resp, err := callD(daemon.Request{Op: "session-create",
		SessionProfile: "agent-standard", SessionName: "live", SessionPID: livePID})
	if err != nil {
		t.Fatal(err)
	}
	live := resp.Status["session"].(string)
	if _, err := callD(daemon.Request{Op: "session-create",
		SessionProfile: "agent-standard", SessionName: "doomed", SessionPID: doomedPID}); err != nil {
		t.Fatal(err)
	}
	kill()

	pruned, err := callD(daemon.Request{Op: "session-prune"})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := pruned.Status["removed"].(float64); n != 1 {
		t.Fatalf("prune removed %v, want the 1 dead-process session (%v)", n, pruned.Status)
	}
	if n, _ := pruned.Status["remaining"].(float64); n != 1 {
		t.Fatalf("prune left %v sessions, want the 1 live one", n)
	}
	by, _ := pruned.Status["by_reason"].(map[string]any)
	if n, _ := by["process-gone"].(float64); n != 1 {
		t.Fatalf("prune did not name the reason: %v", by)
	}
	if !sessionTokens(t, callD)[live[:8]] {
		t.Fatal("prune reaped the live session")
	}
}

// Acceptance: minting the 1000th session costs no more than the 10th. Before
// D9 every mint rewrote the whole sorted array, so N mints serialized
// N(N+1)/2 records — 45,150 at N=300. The journal makes it amortized O(1):
// one line per mint plus the occasional compaction.
func TestD9MintCostIsBounded(t *testing.T) {
	dir := initCairn(t)
	d, callD, _ := serveSessionDaemon(t, dir)

	const n = 300
	start := d.SessionRecordsWrittenForTest()
	for i := 0; i < n; i++ {
		if _, err := callD(daemon.Request{Op: "session-create",
			SessionProfile: "read-only", SessionName: "bulk"}); err != nil {
			t.Fatal(err)
		}
	}
	written := d.SessionRecordsWrittenForTest() - start
	if written > 4*n {
		t.Fatalf("%d mints serialized %d session records (quadratic path would be %d); want < %d",
			n, written, n*(n+1)/2, 4*n)
	}
	t.Logf("%d mints serialized %d records (quadratic would be %d)", n, written, n*(n+1)/2)
}

func tokenOf(i int) string {
	hex := "0123456789abcdef"
	out := make([]byte, 64)
	for j := range out {
		out[j] = hex[(i+j)%16]
	}
	// keep prefixes distinct
	out[0] = hex[i%16]
	out[1] = hex[(i/16)%16]
	out[2] = hex[(i/256)%16]
	return string(out)
}

func writeSessions(t *testing.T, deviceDir string, rows []map[string]any) string {
	t.Helper()
	blob, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(deviceDir, config.SessionsFileName)
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
