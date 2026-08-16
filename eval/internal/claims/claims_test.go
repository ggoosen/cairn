package claims

import (
	"path/filepath"
	"strings"
	"testing"
)

func loadReal(t *testing.T) *Register {
	t.Helper()
	reg, err := Load(filepath.Join("..", "..", DefaultPath))
	if err != nil {
		t.Fatalf("the real register must parse: %v", err)
	}
	return reg
}

// The register the repository actually ships must parse with this parser. If
// it does not, the gate that stops measurements being reported cannot be
// evaluated at all, which fails open.
func TestRealRegisterParses(t *testing.T) {
	reg := loadReal(t)
	if reg.Version != 1 {
		t.Fatalf("version %d", reg.Version)
	}
	if len(reg.Claims) < 20 {
		t.Fatalf("parsed %d claims — the register has more than that", len(reg.Claims))
	}
	for _, want := range []string{
		"RET-success5", "RET-hybrid-earns-place", "RET-dumb-ranking-suffices",
		"PROD-beats-alternatives", "LONG-scales-with-corpus",
		"SAFE-untrusted-content", "SAFE-onboarding-authorship",
	} {
		if _, ok := reg.Get(want); !ok {
			t.Fatalf("claim %q missing from the parsed register", want)
		}
	}
}

// S4 is "dark" precisely because nothing is signed. If this ever fails it is
// not a broken test — it means the sprint's premise changed, and the harness's
// reporting gate should be re-read before the assertion is edited.
func TestEveryCriterionIsStillUnsigned(t *testing.T) {
	reg := loadReal(t)
	if n := len(reg.Unsigned()); n != len(reg.Claims) {
		t.Fatalf("%d of %d claims are signed off; the harness's dark-mode assumptions need re-reading, not this test relaxing",
			len(reg.Claims)-n, len(reg.Claims))
	}
}

func TestSignoffMustBeADate(t *testing.T) {
	cases := map[string]bool{
		"pending":    false,
		"":           false,
		"yes":        false,
		"true":       false,
		"signed":     false,
		"2026-08-16": true,
		"2026-8-16":  false, // not ISO; a date nobody can sort is not a date
	}
	for v, want := range cases {
		if got := (Claim{Signoff: v}).Signed(); got != want {
			t.Fatalf("signoff %q: Signed()=%v, want %v", v, got, want)
		}
	}
}

// An id the register has never heard of must BLOCK, not pass. Otherwise a typo
// in a measurement's claim list opens the gate.
func TestUnknownClaimIDBlocks(t *testing.T) {
	reg := loadReal(t)
	ok, blocked := reg.Signed("RET-success5", "NO-SUCH-CLAIM")
	if ok {
		t.Fatal("an unknown claim id passed the gate")
	}
	joined := strings.Join(blocked, " ")
	if !strings.Contains(joined, "NOT IN THE REGISTER") {
		t.Fatalf("the unknown id was not called out: %v", blocked)
	}
}

func TestParserRefusesWhatItDoesNotUnderstand(t *testing.T) {
	bad := map[string]string{
		"unknown top-level key": "version: 1\nsurprise: 3\nclaims:\n  - id: x\n    kill_criterion: y\n    signoff: pending\n",
		"unknown claim field":   "version: 1\nclaims:\n  - id: x\n    novel_field: q\n    kill_criterion: y\n    signoff: pending\n",
		"bad indent":            "version: 1\nclaims:\n   - id: x\n",
		"claim without signoff": "version: 1\nclaims:\n  - id: x\n    kill_criterion: y\n",
		"duplicate id":          "version: 1\nclaims:\n  - id: x\n    kill_criterion: y\n    signoff: pending\n  - id: x\n    kill_criterion: y\n    signoff: pending\n",
		"empty register":        "version: 1\n",
	}
	for name, src := range bad {
		if _, err := Parse(strings.NewReader(src)); err == nil {
			t.Fatalf("%s: parsed without error; a lenient parser drops the very edits this file exists to record", name)
		}
	}
}

func TestParserReadsAQuotedSignoff(t *testing.T) {
	reg, err := Parse(strings.NewReader("version: 1\nclaims:\n  - id: x\n    kill_criterion: \"kill it\"\n    signoff: 2026-08-16\n"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Get("x")
	if !c.Signed() || c.KillCriterion != "kill it" {
		t.Fatalf("parsed claim wrong: %+v", c)
	}
	if ok, _ := reg.Signed("x"); !ok {
		t.Fatal("a dated signoff did not open the gate")
	}
}
