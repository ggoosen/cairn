package main

// D13 — a release binary must be able to name its own version.
//
// Before this, `cairn --version` printed "p1-<commit>" derived from Go build
// info and settable by nothing, so a tagged artifact could not report its tag
// and the release notes had to state the discrepancy instead of fixing it.
// The rule that replaces it has two halves, and both are load-bearing: a
// RELEASE build names its tag, and a DEVELOPMENT build never does.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestD13BuildVersionShape(t *testing.T) {
	const rev = "0123456789abcdef0123456789abcdef01234567"

	for _, tc := range []struct {
		name            string
		release, rev    string
		dirty, haveInfo bool
		want            string
	}{
		// Development builds: unchanged from FIX-G7.5 / R11.
		{name: "dev clean", rev: rev, haveInfo: true, want: "p1-0123456789ab"},
		{name: "dev dirty", rev: rev, dirty: true, haveInfo: true, want: "p1-0123456789ab-dirty"},
		{name: "no build info", want: "p1-dev"},
		{name: "build info but no vcs stamp", haveInfo: true, want: "p1-dev"},
		// Release builds: the tag leads, the commit survives in parentheses.
		{name: "release", release: "v0.3.0", rev: rev, haveInfo: true, want: "v0.3.0 (p1-0123456789ab)"},
		// A stamped build of a MODIFIED tree still says so. The release
		// workflow refuses to publish anything containing "dirty", and that
		// guard only works if the word survives the tag.
		{name: "release dirty", release: "v0.3.0", rev: rev, dirty: true, haveInfo: true,
			want: "v0.3.0 (p1-0123456789ab-dirty)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildVersion(tc.release, tc.rev, tc.dirty, tc.haveInfo); got != tc.want {
				t.Fatalf("buildVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

// A development build must not claim a tag. This is the half that cannot be
// checked by looking at a release: the binary under test is the one the suite
// itself built, unstamped, and it must name a commit and nothing more.
func TestD13DevelopmentBuildClaimsNoTag(t *testing.T) {
	if releaseVersion != "" {
		t.Fatalf("the test binary was built with a release stamp %q — the suite must never link one in", releaseVersion)
	}
	if !strings.HasPrefix(version, "p1") {
		t.Fatalf("an unstamped build must report the milestone-and-commit shape, got %q", version)
	}
	if strings.Contains(version, "(") {
		t.Fatalf("an unstamped build must not report a release tag, got %q", version)
	}
}

// The mechanism end to end: the Makefile's own linker flags, a real link, and
// the binary asked what it is. A unit test of buildVersion cannot catch the
// failure that actually ships — a renamed symbol or a mistyped -X path, which
// the linker accepts in silence and which leaves the tag simply absent.
func TestD13StampedBinaryReportsItsTag(t *testing.T) {
	if testing.Short() {
		t.Skip("links a second binary")
	}
	const tag = "v9.9.9-d13-test"
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	// Ask the Makefile for the flags rather than restating them: if the
	// stamped symbol is ever renamed, this test must fail with it, not pass
	// against a string nothing else uses.
	mk := exec.Command("make", "-s", "--no-print-directory", "print-ldflags", "CAIRN_VERSION="+tag)
	mk.Dir = repoRoot
	// This test is itself run BY make, and an inherited MAKEFLAGS otherwise
	// prints "Entering directory …" into the flags we are about to pass to the
	// linker. Start the sub-make from a clean slate.
	mk.Env = append(os.Environ(), "MAKEFLAGS=", "MAKELEVEL=")
	out, err := mk.Output()
	if err != nil {
		t.Skipf("make print-ldflags unavailable: %v", err)
	}
	ldflags := strings.TrimSpace(string(out))
	if ldflags == "" {
		t.Fatal("make print-ldflags produced nothing for a non-empty CAIRN_VERSION")
	}
	if !strings.Contains(ldflags, tag) {
		t.Fatalf("make print-ldflags does not carry the version: %q", ldflags)
	}

	bin := filepath.Join(t.TempDir(), "cairn")
	build := exec.Command("go", "build", "-tags", "sqlite_fts5,cairn_testhooks",
		ldflags, "-o", bin, ".")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building a stamped binary: %v\n%s", err, out)
	}

	printed, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("cairn --version: %v", err)
	}
	got := strings.TrimSpace(string(printed))
	if !strings.HasPrefix(got, tag) {
		t.Fatalf("a stamped binary does not name its tag: got %q, want a %q prefix", got, tag)
	}
	// And it still carries the build stamp the author triages with.
	if !strings.Contains(got, "p1") {
		t.Fatalf("the stamped version dropped the build stamp: %q", got)
	}
}
