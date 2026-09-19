package daemon

// D12 — the socket path must stay inside the platform's sockaddr_un bound.
//
// This is the regression guard for a failure nobody could see locally, and
// the reason it was invisible is worth stating precisely: it was never that
// Linux tolerates the over-long path. Linux rejects it too (sun_path is 108
// there, against macOS's 104 — the same EINVAL, four bytes later). It was that
// the PATHS DIFFERED. The same line of test code — os.MkdirTemp("", …) —
// honors $TMPDIR, which is /tmp on Linux and a 48-byte /var/folders directory
// on macOS, so it produced a 65-byte path on the machine anyone develops on
// and a 110-byte one on the PRIMARY platform. Five sprints reported "verify
// green" on Linux while macOS CI was deterministically red.
//
// So these tests do not ask "does it work here". They pin the macOS-shaped
// environment by LENGTH, on every platform, and then bind for real — the only
// check that a generous local $TMPDIR cannot fool.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
)

// macOSTMPDIRBytes is the length of a real macOS per-user temporary directory:
// /var/folders/<2>/<32>/T. The exact bytes are irrelevant to sun_path; the
// count is the whole problem, so the count is what these tests reproduce.
const macOSTMPDIRBytes = len("/var/folders/df/djsxfhc17x95674wsm_g8s980000gn/T")

// d12Root is a deliberately SHORT test root: anything longer would dominate
// the arithmetic instead of the environment under test. /tmp is the shortest
// directory present on both supported platforms (on macOS it is a symlink to
// /private/tmp, which the kernel resolves — sun_path counts the bytes passed
// to bind, not the bytes they resolve to).
func d12Root(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "d12")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

// dirOfLength creates a real directory under root whose path is exactly n
// bytes long, so a test can say "a TMPDIR as long as macOS's" and mean it.
func dirOfLength(t *testing.T, root string, n int) string {
	t.Helper()
	if n <= len(root)+1 {
		t.Fatalf("test root %q (%d bytes) is already longer than the %d-byte target", root, len(root), n)
	}
	dir := filepath.Join(root, strings.Repeat("t", n-len(root)-1))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A cairn id of the real shape: a 36-character UUIDv7.
const d12CairnID = "01a00bff-e74e-7785-a282-b7f72ee83c31"

func TestD12SocketPathFitsPlatformLimit(t *testing.T) {
	root := d12Root(t)
	macTmp := dirOfLength(t, root, macOSTMPDIRBytes)

	cases := []struct {
		name string
		tmp  string // TMPDIR
		xdg  string // XDG_RUNTIME_DIR ("" = unset)
		// whether the FULL cairn id should survive in the leaf: degrading the
		// name is a last resort, not the normal case.
		wantFullID bool
	}{
		// The production macOS case FIX-A7 reasoned about, and reasoned about
		// correctly: 100 bytes, it fits, and the full id is kept.
		{"macos production, no runtime dir", macTmp, "", true},
		// The case that actually broke CI: a runtime dir from
		// os.MkdirTemp("", …) — genuinely short on Linux, 62 bytes on macOS.
		// The runtime dir is honored; the NAME gives way.
		{"macos-length runtime dir", macTmp, filepath.Join(macTmp, "cd52667834331"), false},
		// A runtime dir so deep no leaf can rescue it: the ladder must fall
		// through to another directory rather than overflow.
		{"pathologically deep runtime dir", macTmp, filepath.Join(root, strings.Repeat("deep/", 20)), true},
		// And a TMPDIR that is itself hopeless, with no runtime dir to fall
		// back on. /tmp is the floor.
		{"pathological TMPDIR, no runtime dir", filepath.Join(root, strings.Repeat("wide/", 24)), "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(tc.tmp, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", tc.tmp) // os.TempDir() reads this on unix
			t.Setenv("XDG_RUNTIME_DIR", tc.xdg)
			if tc.xdg != "" {
				if err := os.MkdirAll(tc.xdg, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			p := SocketPath(d12CairnID)
			if len(p) > config.SocketPathMaxBytes {
				t.Fatalf("socket path is %d bytes, over the %d-byte limit:\n  %s",
					len(p), config.SocketPathMaxBytes, p)
			}
			if got := strings.Contains(p, d12CairnID); got != tc.wantFullID {
				t.Errorf("full cairn id in the leaf = %v, want %v: %s", got, tc.wantFullID, p)
			}

			// The bound is only worth asserting if the kernel agrees with it.
			// Binding for real is what fails with "invalid argument" when the
			// arithmetic is wrong — and it fails HERE, in a developer's
			// terminal, instead of on a runner nobody is watching.
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			l, err := net.Listen("unix", p)
			if err != nil {
				t.Fatalf("binding %s (%d bytes): %v", p, len(p), err)
			}
			l.Close()
			os.Remove(p)
		})
	}
}

// The ladder degrades the NAME before the DIRECTORY: an operator who exported
// XDG_RUNTIME_DIR chose it for privacy and logout cleanup, and losing that
// silently is worse than an illegible leaf.
func TestD12SocketPathDegradesNameBeforeDirectory(t *testing.T) {
	root := d12Root(t)
	t.Setenv("TMPDIR", root)
	// 60 bytes: "<xdg>/cairn/<36-char id>.sock" is 108 and overflows, while
	// "<xdg>/cairn/<16 hex>.sock" is 88 and fits.
	xdg := dirOfLength(t, root, 60)
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	p := SocketPath(d12CairnID)
	if len(p) > config.SocketPathMaxBytes {
		t.Fatalf("socket path is %d bytes, over the limit: %s", len(p), p)
	}
	if !strings.HasPrefix(p, xdg) {
		t.Fatalf("the runtime dir was abandoned when shortening the name would have sufficed:\n  got  %s\n  want under %s", p, xdg)
	}
	if strings.Contains(p, d12CairnID) {
		t.Fatalf("expected a shortened leaf, got the full id: %s", p)
	}
	// Deterministic and id-derived: a daemon and a client must compute the
	// same path, and two meshes must never share one.
	if p != SocketPath(d12CairnID) {
		t.Fatal("the shortened socket path is not deterministic")
	}
	if p == SocketPath("01a00bff-e74e-7785-a282-b7f72ee83c32") {
		t.Fatal("two different cairn ids produced the same socket path")
	}
}

// When it fits, nothing changes: the preferred runtime directory is used and
// the full cairn id stays in the leaf. Guards against a "fix" that makes every
// socket illegible to buy headroom it does not need.
func TestD12SocketPathKeepsFullIDWhenItFits(t *testing.T) {
	xdg := d12Root(t)
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	want := filepath.Join(xdg, "cairn", d12CairnID+".sock")
	if got := SocketPath(d12CairnID); got != want {
		t.Fatalf("socket path\n  got  %s\n  want %s", got, want)
	}
	if got := SocketDir(); got != filepath.Join(xdg, "cairn") {
		t.Fatalf("SocketDir = %s, want %s", got, filepath.Join(xdg, "cairn"))
	}
}

// Serve refuses an impossible path by NAME. The ladder cannot run out on any
// sane machine, so this branch is a safety net — but a safety net whose
// message is never read is not one. bind(2)'s bare EINVAL is precisely the
// error that hid this bug for five sprints; the replacement must name the
// path, its size and the limit, so the next person is not left guessing.
func TestD12SocketPathLengthCheckNamesTheOffender(t *testing.T) {
	if err := checkSocketPathLength(strings.Repeat("a", config.SocketPathMaxBytes)); err != nil {
		t.Fatalf("a path exactly at the limit must be accepted: %v", err)
	}
	over := strings.Repeat("a", config.SocketPathMaxBytes+1)
	err := checkSocketPathLength(over)
	if err == nil {
		t.Fatal("a path over the limit must be refused")
	}
	for _, want := range []string{
		over,
		fmt.Sprintf("%d", config.SocketPathMaxBytes+1),
		fmt.Sprintf("%d", config.SocketPathMaxBytes),
		"TMPDIR",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}
