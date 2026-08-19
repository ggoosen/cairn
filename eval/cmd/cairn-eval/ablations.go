package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ggoosen/cairn/eval/internal/ablation"
)

// runAblations prints the E4 catalogue: what each arm changes, how faithfully
// it is produced, and what its numbers therefore cannot show.
//
// It prints no result and never will. The purpose is to make the fidelity
// column readable BEFORE a run, so nobody discovers after the fact that the
// arm they most wanted was a lower bound.
func runAblations(args []string) error {
	fs := flag.NewFlagSet("ablations", flag.ContinueOnError)
	full := fs.Bool("v", false, "print each arm's mechanism and limits in full")
	if err := fs.Parse(args); err != nil {
		return err
	}
	out := os.Stdout
	fmt.Fprintln(out, "ablation arms (BUILD-PLAN §3.4 E4):")
	fmt.Fprintln(out)
	for _, a := range ablation.Catalogue() {
		surfaces := make([]string, 0, len(a.Surfaces))
		for _, s := range a.Surfaces {
			surfaces = append(surfaces, string(s))
		}
		fmt.Fprintf(out, "  %-20s %-12s surfaces=%-14s bears on %s\n",
			a.ID, a.Fidelity, strings.Join(surfaces, ","), strings.Join(a.BearsOn, ", "))
		fmt.Fprintf(out, "      %s\n", a.Title)
		if *full {
			fmt.Fprintf(out, "      mechanism: %s\n", wrap(a.Mechanism, 6))
			if a.Fidelity == ablation.Unavailable {
				fmt.Fprintf(out, "      UNAVAILABLE: %s\n", wrap(a.Why, 6))
			} else {
				fmt.Fprintf(out, "      limits:    %s\n", wrap(a.Limits, 6))
			}
			fmt.Fprintln(out)
		}
	}
	fmt.Fprint(out, `
fidelity:
  native       the system under test was configured this way and measured.
  recomputed   the returned results were re-ranked from the published
               `+"`cairn why-ranked`"+` arithmetic. A LOWER BOUND: it cannot surface a
               document retrieval never returned, so Recall@K cannot move.
  unavailable  no black-box route exists. These arms FAIL LOUDLY rather than
               running the default condition under the arm's name.
`)
	return nil
}

// wrap re-flows a long sentence under an indent, so the limits are readable
// rather than a wall nobody reads.
func wrap(s string, indent int) string {
	const width = 72
	pad := strings.Repeat(" ", indent+11)
	var b strings.Builder
	line := 0
	for _, w := range strings.Fields(s) {
		if line+len(w)+1 > width {
			b.WriteString("\n" + pad)
			line = 0
		} else if line > 0 {
			b.WriteString(" ")
			line++
		}
		b.WriteString(w)
		line += len(w)
	}
	return b.String()
}
