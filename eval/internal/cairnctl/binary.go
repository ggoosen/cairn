// Package cairnctl drives a real cairn as a BLACK BOX.
//
// It provisions a throwaway cairn in a temporary directory, runs the daemon
// as a subprocess, and talks to it only through the two surfaces a real
// agent has: the `cairn` CLI and the `cairn mcp` stdio JSON-RPC server. It
// links nothing from the main module — it cannot, because eval/ is a
// separate module and the daemon lives under internal/ (BUILD-PLAN §3.3).
//
// One consequence is deliberate and worth stating: the response types in
// this package are hand-written duplicates of the CLI's JSON output, not
// shared structs. They pin the shape of the PUBLIC surface. If the daemon
// changes that shape, the harness breaks — which is the correct outcome,
// because agents would break too.
package cairnctl

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// BinaryEnvVar names a prebuilt cairn binary to evaluate. Set it to measure
// a released artifact rather than a fresh build of the working tree.
const BinaryEnvVar = "CAIRN_EVAL_BINARY"

// BuildTags are the build tags the harness compiles cairn with.
//
//   - sqlite_fts5 is mandatory (mattn/go-sqlite3 compiles FTS5 only behind
//     it; an untagged build fails at compile time by design).
//   - cairn_testhooks adds the fault-injection and clock hooks that a
//     release binary must never contain. The harness needs the clock hook
//     for BUILD-PLAN §3.4 E9 long-horizon replay, and the volume-status hook so
//     provisioning does not depend on the CI runner's disk encryption.
//
// A binary supplied through CAIRN_EVAL_BINARY is used as-is: whatever tags
// it was built with are the tags under test, and Instance records that in
// the run metadata rather than assuming.
const (
	BuildTagsRelease = "sqlite_fts5"
	BuildTagsEval    = "sqlite_fts5,cairn_testhooks"
)

// Binary is a cairn executable under test.
type Binary struct {
	Path string
	// Tags is the build-tag set the harness compiled this binary with, or
	// "supplied" when the path came from CAIRN_EVAL_BINARY.
	Tags string
	// Version is the output of `cairn --version`, recorded with results so
	// a run can be tied back to the code that produced it.
	Version string
}

var (
	buildOnce   sync.Once
	builtBinary Binary
	buildErr    error
)

// FindBinary resolves the cairn binary to evaluate: CAIRN_EVAL_BINARY if
// set, otherwise a build of the repository containing this harness. The
// build happens at most once per process; concurrent callers share it.
func FindBinary(ctx context.Context) (Binary, error) {
	if p := os.Getenv(BinaryEnvVar); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return Binary{}, fmt.Errorf("resolving %s=%q: %w", BinaryEnvVar, p, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return Binary{}, fmt.Errorf("%s=%q: %w", BinaryEnvVar, p, err)
		}
		b := Binary{Path: abs, Tags: "supplied"}
		b.Version, _ = binaryVersion(ctx, abs)
		return b, nil
	}
	buildOnce.Do(func() { builtBinary, buildErr = buildBinary(ctx) })
	return builtBinary, buildErr
}

// buildBinary compiles cmd/cairn from the enclosing repository into a
// process-lifetime temp dir.
func buildBinary(ctx context.Context) (Binary, error) {
	root, err := RepoRoot()
	if err != nil {
		return Binary{}, err
	}
	outDir, err := os.MkdirTemp("", "cairn-eval-bin-")
	if err != nil {
		return Binary{}, err
	}
	out := filepath.Join(outDir, "cairn")

	bctx, cancel := context.WithTimeout(ctx, tunables.BuildTimeout)
	defer cancel()
	cmd := exec.CommandContext(bctx, "go", "build", "-tags", BuildTagsEval, "-o", out, "./cmd/cairn")
	cmd.Dir = root
	cmd.Env = os.Environ()
	if blob, err := cmd.CombinedOutput(); err != nil {
		return Binary{}, fmt.Errorf("building cairn (tags %s) in %s: %w\n%s", BuildTagsEval, root, err, blob)
	}
	b := Binary{Path: out, Tags: BuildTagsEval}
	b.Version, _ = binaryVersion(ctx, out)
	return b, nil
}

func binaryVersion(ctx context.Context, path string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, tunables.CommandTimeout)
	defer cancel()
	// `cairn --version` also warns on stderr when a DIFFERENT daemon is
	// running; only stdout carries the version, so Output (not
	// CombinedOutput) is what we want here.
	blob, err := exec.CommandContext(cctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(blob)), nil
}

// RepoRoot walks up from the working directory to the directory holding the
// MAIN module (the one whose go.mod declares github.com/ggoosen/cairn).
// eval/ is a nested module, so its own go.mod is skipped.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		blob, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(blob), "module github.com/ggoosen/cairn\n") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no enclosing cairn module found above %s (set %s to a prebuilt binary)", dir, BinaryEnvVar)
		}
		dir = parent
	}
}
