// Package boundary asserts the property the whole harness rests on: the
// evaluation code CANNOT reach into the daemon's internals.
//
// EVAL-PLAN §3 chose a separate Go module for exactly this reason — Go's
// internal/ visibility rule turns "black box" from a convention someone has
// to remember into something the compiler refuses to build. A harness that
// could read internal state could measure implementation details, and could
// be tuned against them; neither would be visible in the results.
//
// Two checks, because they fail differently:
//
//  1. TestHarnessImportsNoDaemonInternals scans what the harness actually
//     imports today. It is the regression guard, it is offline and fast, and
//     it fails the moment someone adds such an import.
//  2. TestInternalImportIsCompilerRefused proves the RULE is real by trying
//     to compile a module that imports the daemon and requiring the exact
//     internal-visibility error. It needs a working Go toolchain and skips
//     rather than failing if the toolchain cannot resolve the probe.
package boundary

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/eval/internal/cairnctl"
)

const daemonInternalPrefix = "github.com/ggoosen/cairn/internal/"

func TestHarnessImportsNoDaemonInternals(t *testing.T) {
	root, err := evalModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "list", "-deps", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	blob, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, blob)
	}
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), daemonInternalPrefix) {
			t.Fatalf("harness depends on %s — black-box access is the point (EVAL-PLAN §3)", line)
		}
	}
}

func TestInternalImportIsCompilerRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a probe module")
	}
	repo, err := cairnctl.RepoRoot()
	if err != nil {
		t.Skipf("no enclosing cairn repository: %v", err)
	}
	dir := t.TempDir()
	goMod := "module probe\n\ngo 1.25.0\n\ntoolchain go1.26.3\n\n" +
		"require github.com/ggoosen/cairn v0.0.0\n\nreplace github.com/ggoosen/cairn => " + repo + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}
	src := "package probe\n\nimport _ \"github.com/ggoosen/cairn/internal/daemon\"\n"
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	// GOPROXY=off keeps the check offline: the internal-visibility rule is
	// applied during package loading, before any dependency is fetched, so
	// the probe never needs the network to prove the point.
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	blob, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("a foreign module compiled an import of github.com/ggoosen/cairn/internal/daemon — the black-box boundary is not being enforced")
	}
	if !strings.Contains(string(blob), "use of internal package") {
		t.Skipf("probe failed for a different reason than the internal rule (toolchain/environment); output:\n%s", blob)
	}
}

// evalModuleRoot walks up to the eval module's own go.mod.
func evalModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		blob, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(blob), "module github.com/ggoosen/cairn/eval\n") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
