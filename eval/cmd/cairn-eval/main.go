// Command cairn-eval drives the evaluation apparatus.
//
// WHAT IT DELIBERATELY DOES NOT DO: print a metric, rank two conditions
// against each other, or say anything that reads as a verdict on Cairn.
//
// The standing rule (BUILD-PLAN §5-E1) is: apparatus may be built ahead of
// sign-off; no measurement may be reported as evidence before its kill
// criterion is signed. So the measurement verbs below DO run, DO compute nDCG,
// MRR, Recall@k and Precision@k, and DO write structured scorecards — because
// an apparatus nobody can exercise is an apparatus nobody has debugged. What
// they will not do is render any of it as a comparison while
// eval/claims.yaml's criteria are unsigned, or over a corpus this project
// authored. Both halves are enforced in internal/score, and `cairn-eval
// claims` is the readout.
//
// An unfalsifiable number is worse than no number, because it looks like
// evidence.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "backends":
		err = runBackends(args[1:])
	case "smoke":
		err = runSmoke(ctx, args[1:])
	case "corpus":
		err = runCorpus(args[1:])
	case "claims":
		err = runClaims(args[1:])
	case "ablations":
		err = runAblations(args[1:])
	case "measure":
		err = runMeasure(ctx, args[1:])
	case "growth":
		err = runGrowth(ctx, args[1:])
	case "adversarial":
		err = runAdversarial(ctx, args[1:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cairn-eval — evaluation apparatus for Cairn (build/BUILD-PLAN.md)

usage:
  cairn-eval claims [-v]         the gate readout: which kill criteria are signed
  cairn-eval backends            list memory conditions and what each models
  cairn-eval ablations [-v]      list E4's ablation arms, their fidelity and limits
  cairn-eval smoke [flags]       plumbing check: provision, write, retrieve, record

  cairn-eval measure     -corpus DIR [flags]   E4: baselines × ablations
  cairn-eval growth      -corpus DIR [flags]   E9: recall under corpus growth
  cairn-eval adversarial [-list] [flags]       E6: planted prompt injections

  cairn-eval corpus verify DIR   check a corpus against its manifest checksums
  cairn-eval corpus info DIR     where a corpus came from and who made its labels
  cairn-eval corpus mine …       build a corpus from mined human relevance labels
                                 (github | stackoverflow | docs)

The measurement verbs compute metrics and write scorecards, and print NONE of
them: every kill criterion in eval/claims.yaml is still unsigned, so no result
may be reported as evidence. Run "cairn-eval claims" to see the gate.
`)
}
