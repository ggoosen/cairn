package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ggoosen/cairn/eval/internal/claims"
)

// ClaimsEnvVar overrides where the register is read from.
const ClaimsEnvVar = "CAIRN_EVAL_CLAIMS"

// loadRegister finds and parses eval/claims.yaml.
//
// The register is the AUTHORITY on whether any measurement may be reported
// (BUILD-PLAN §5-E1), so a missing one is a hard failure rather than a
// permissive default. A harness that could not find its own kill criteria and
// carried on would be a harness with no gate at all.
func loadRegister(path string) (*claims.Register, error) {
	if path == "" {
		path = os.Getenv(ClaimsEnvVar)
	}
	if path == "" {
		found, err := findUpwards(claims.DefaultPath)
		if err != nil {
			return nil, fmt.Errorf("cannot find %s (set %s or pass -claims): %w. Without the register the harness cannot tell whether a result may be reported, and it will not guess",
				claims.DefaultPath, ClaimsEnvVar, err)
		}
		path = found
	}
	return claims.Load(path)
}

func findUpwards(name string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s in any parent of the working directory", name)
		}
		dir = parent
	}
}

// runClaims prints the register's signoff state. This is the gate readout: it
// is the answer to "may anything be reported yet", and it is the first thing
// to check before believing any artifact this harness produced.
func runClaims(args []string) error {
	fs := flag.NewFlagSet("claims", flag.ContinueOnError)
	path := fs.String("claims", "", "path to claims.yaml (default: found upwards from the working directory)")
	verbose := fs.Bool("v", false, "print each claim's kill criterion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	reg, err := loadRegister(*path)
	if err != nil {
		return err
	}
	out := os.Stdout
	unsigned := reg.Unsigned()

	fmt.Fprintf(out, "claims register v%d, registered %s — %d claims, %d awaiting operator sign-off\n\n",
		reg.Version, reg.RegisteredAt, len(reg.Claims), len(unsigned))
	for _, c := range reg.Claims {
		state := "SIGNED " + c.Signoff
		if !c.Signed() {
			state = "unsigned (" + c.Signoff + ")"
		}
		fmt.Fprintf(out, "  %-28s %-12s %s\n", c.ID, c.Class, state)
		if *verbose {
			fmt.Fprintf(out, "        kill: %s\n", c.KillCriterion)
		}
	}
	if len(unsigned) > 0 {
		fmt.Fprintf(out, `
%d kill criteria are unsigned, so NO MEASUREMENT MAY BE REPORTED AS EVIDENCE.
The apparatus still runs and still writes structured results; those results
carry evidence=false and the harness will not render them as a comparison.

BUILD-PLAN §5-E1: criteria are fixed before results exist to be tempted by. A
threshold chosen after seeing the number is not a threshold. To open the gate,
an operator reviews each kill_criterion above and replaces `+"`signoff: pending`"+`
with an ISO date.
`, len(unsigned))
	}
	return nil
}
