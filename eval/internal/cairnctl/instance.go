package cairnctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// KeepEnvVar, when set to a non-empty value, leaves a throwaway instance's
// directories on disk after Close so a failed run can be inspected.
const KeepEnvVar = "CAIRN_EVAL_KEEP"

// Options configures a throwaway cairn.
type Options struct {
	// Binary to run. Zero value resolves via FindBinary.
	Binary Binary

	// Root is the parent directory for the instance. Empty means a new
	// os.MkdirTemp; a caller supplying a t.TempDir() gets test-framework
	// cleanup as well as ours.
	Root string

	// DisplayName for the device certificate. Empty lets cairn choose.
	DisplayName string

	// Thin provisions a thin node (recent window only, partial universal
	// search). BUILD-PLAN §9.4 measures partiality honesty on such a node.
	Thin bool

	// SyncListen is passed to `cairn init --sync-listen`. It defaults to
	// "off": an evaluation instance must not bind a listener on the
	// operator's tailnet, and every mesh experiment (E9 §9.4) wires its own
	// topology explicitly rather than inheriting "auto".
	SyncListen string

	// Clock, when non-zero, runs the daemon on a SIMULATED clock (clock.go).
	// Requires a binary built with cairn_testhooks — which is what
	// FindBinary produces, and which a release binary is not.
	Clock Clock

	// ExtraEnv entries ("K=V") are appended to every subprocess environment.
	ExtraEnv []string
}

// Instance is one provisioned, black-box cairn.
type Instance struct {
	Bin Binary

	Root      string // parent of everything this instance owns
	Dir       string // the portable cairn directory (--dir)
	DeviceDir string // CAIRN_DEVICE_STATE_DIR

	env       []string
	ownedRoot bool // Root was created by us and may be removed

	mu        sync.Mutex
	daemonCmd *exec.Cmd
	daemonOut *lockedBuffer
	daemonErr *lockedBuffer
	daemonWG  sync.WaitGroup
	daemonRun error
}

// Provision creates a throwaway cairn: a temp root, a portable directory,
// device-local state of its own, and the genesis event via `cairn init`.
// It does NOT start the daemon; call StartDaemon.
//
// --allow-unencrypted is passed unconditionally. A throwaway directory under
// the system temp dir is not on an encrypted volume on a CI runner, and the
// alternative (the CAIRN_FAKE_VOLUME_STATUS test hook) would tie
// provisioning to a testhooks build. The override is device-local and dies
// with the instance.
func Provision(ctx context.Context, opts Options) (*Instance, error) {
	bin := opts.Binary
	if bin.Path == "" {
		b, err := FindBinary(ctx)
		if err != nil {
			return nil, err
		}
		bin = b
	}

	root, owned := opts.Root, false
	if root == "" {
		r, err := os.MkdirTemp("", "cairn-eval-")
		if err != nil {
			return nil, err
		}
		root, owned = r, true
	}

	inst := &Instance{
		Bin:       bin,
		Root:      root,
		Dir:       filepath.Join(root, "cairn"),
		DeviceDir: filepath.Join(root, "device-state"),
		ownedRoot: owned,
	}
	if err := os.MkdirAll(inst.DeviceDir, 0o700); err != nil {
		return nil, err
	}

	env := append([]string{}, os.Environ()...)
	// CAIRN_DIR and CAIRN_SESSION are removed rather than overridden: an
	// inherited session handle would silently confine every verb to another
	// agent's capability profile, and an inherited CAIRN_DIR would make a
	// missing --dir point at the operator's real mesh.
	env = filterEnv(env, "CAIRN_DIR", "CAIRN_SESSION", "CAIRN_DEVICE_STATE_DIR")
	env = append(env, "CAIRN_DEVICE_STATE_DIR="+inst.DeviceDir)
	// The clock hook is read by the DAEMON only; `cairn init` runs its
	// identity ceremonies on the real clock either way. Exporting it to
	// every verb keeps one environment for the instance rather than two
	// that could drift.
	env = append(env, opts.Clock.env()...)
	env = append(env, opts.ExtraEnv...)
	inst.env = env

	syncListen := opts.SyncListen
	if syncListen == "" {
		syncListen = "off"
	}
	args := []string{"init", "--dir", inst.Dir, "--allow-unencrypted", "--sync-listen", syncListen}
	if opts.DisplayName != "" {
		args = append(args, "--display-name", opts.DisplayName)
	}
	if opts.Thin {
		args = append(args, "--thin")
	}
	if _, _, err := inst.exec(ctx, nil, args...); err != nil {
		inst.cleanupDirs()
		return nil, fmt.Errorf("cairn init: %w", err)
	}
	return inst, nil
}

// StartDaemon runs `cairn daemon` as a subprocess and waits until it answers
// `cairn status`.
func (i *Instance) StartDaemon(ctx context.Context) error {
	i.mu.Lock()
	if i.daemonCmd != nil {
		i.mu.Unlock()
		return errors.New("daemon already running")
	}
	cmd := exec.Command(i.Bin.Path, "daemon", "--dir", i.Dir)
	cmd.Env = i.env
	out, errBuf := &lockedBuffer{}, &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, errBuf
	// Own process group: Stop signals the group, so a daemon that has
	// spawned an embedder subprocess does not leak it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		i.mu.Unlock()
		return fmt.Errorf("starting daemon: %w", err)
	}
	i.daemonCmd, i.daemonOut, i.daemonErr = cmd, out, errBuf
	i.daemonWG.Add(1)
	go func() {
		defer i.daemonWG.Done()
		err := cmd.Wait()
		i.mu.Lock()
		i.daemonRun = err
		i.mu.Unlock()
	}()
	i.mu.Unlock()

	deadline := time.Now().Add(tunables.DaemonReadyTimeout)
	for {
		if _, _, err := i.exec(ctx, nil, "status", "--dir", i.Dir, "--json"); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			_ = i.StopDaemon()
			return fmt.Errorf("daemon not ready after %s: %w\ndaemon stderr:\n%s",
				tunables.DaemonReadyTimeout, err, i.DaemonStderr())
		}
		if !i.daemonAlive() {
			return fmt.Errorf("daemon exited during startup: %v\nstdout:\n%s\nstderr:\n%s",
				i.daemonExitErr(), i.DaemonStdout(), i.DaemonStderr())
		}
		select {
		case <-ctx.Done():
			_ = i.StopDaemon()
			return ctx.Err()
		case <-time.After(tunables.DaemonReadyPoll):
		}
	}
}

// StopDaemon terminates the daemon: SIGTERM to its process group, escalating
// to SIGKILL if it has not exited within DaemonStopTimeout. Stopping an
// already-stopped instance is a no-op.
func (i *Instance) StopDaemon() error {
	i.mu.Lock()
	cmd := i.daemonCmd
	i.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

	done := make(chan struct{})
	go func() { i.daemonWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(tunables.DaemonStopTimeout):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}

	i.mu.Lock()
	i.daemonCmd = nil
	err := i.daemonRun
	i.mu.Unlock()
	// SIGTERM is how we ask; a signal-caused exit is success, not failure.
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil
	}
	return err
}

// Close stops the daemon and removes everything the instance owns, unless
// CAIRN_EVAL_KEEP is set.
func (i *Instance) Close() error {
	err := i.StopDaemon()
	i.cleanupDirs()
	return err
}

func (i *Instance) cleanupDirs() {
	if os.Getenv(KeepEnvVar) != "" {
		fmt.Fprintf(os.Stderr, "cairn-eval: keeping %s (%s set)\n", i.Root, KeepEnvVar)
		return
	}
	if i.ownedRoot {
		_ = os.RemoveAll(i.Root)
	}
}

// Run invokes one CLI verb against this instance and returns stdout. --dir is
// prepended, so callers pass the verb and its own flags only.
func (i *Instance) Run(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, err := i.exec(ctx, nil, append([]string{"--dir", i.Dir}, args...)...)
	if err != nil {
		return stdout, fmt.Errorf("cairn %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr)
	}
	return stdout, nil
}

// RunStdin is Run with bytes piped to the verb's stdin (`cairn send -`).
func (i *Instance) RunStdin(ctx context.Context, stdin []byte, args ...string) (string, error) {
	stdout, stderr, err := i.exec(ctx, stdin, append([]string{"--dir", i.Dir}, args...)...)
	if err != nil {
		return stdout, fmt.Errorf("cairn %s: %w\nstderr: %s", strings.Join(args, " "), err, stderr)
	}
	return stdout, nil
}

func (i *Instance) exec(ctx context.Context, stdin []byte, args ...string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, tunables.CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, i.Bin.Path, args...)
	cmd.Env = i.env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	return out.String(), errBuf.String(), err
}

// DaemonStdout / DaemonStderr expose the daemon subprocess's captured output.
// Its stderr carries the warnings the harness must be able to see (stale
// binary, unencrypted volume, simulated clock).
func (i *Instance) DaemonStdout() string { return i.bufString(i.daemonOut) }
func (i *Instance) DaemonStderr() string { return i.bufString(i.daemonErr) }

func (i *Instance) bufString(b *lockedBuffer) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if b == nil {
		return ""
	}
	return b.String()
}

func (i *Instance) daemonAlive() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.daemonCmd != nil && i.daemonRun == nil
}

func (i *Instance) daemonExitErr() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.daemonRun
}

func filterEnv(env []string, drop ...string) []string {
	out := env[:0:0]
	for _, e := range env {
		keep := true
		for _, d := range drop {
			if strings.HasPrefix(e, d+"=") {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, e)
		}
	}
	return out
}

// lockedBuffer is a bytes.Buffer safe for a subprocess writer goroutine and
// a reading test to share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
