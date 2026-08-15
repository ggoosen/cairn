// Command cairn-eval drives the evaluation apparatus.
//
// WHAT IT DELIBERATELY DOES NOT DO: compute a metric, compare backends, or
// print anything that reads as a verdict on Cairn. EVAL-PLAN §5-E1
// pre-registers falsification criteria BEFORE measurement so that results
// cannot influence the criteria, and every `signoff:` in eval/claims.yaml is
// still `pending`. Measurement verbs arrive with E4, after those signoffs.
//
// Today it can: list the memory conditions and what each of them models, and
// run a PLUMBING check that proves the apparatus itself works end to end.
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
	fmt.Fprint(os.Stderr, `cairn-eval — evaluation apparatus for Cairn (build/EVAL-PLAN.md)

usage:
  cairn-eval backends            list memory conditions and what each models
  cairn-eval smoke [flags]       plumbing check: provision, write, retrieve, record
  cairn-eval corpus verify DIR   check a corpus against its manifest checksums
  cairn-eval corpus info DIR     where a corpus came from and who made its labels
  cairn-eval corpus mine …       build a corpus from mined human relevance labels
                                 (github | stackoverflow | docs)

No measurement verbs exist yet. E4 adds them, once eval/claims.yaml carries
operator signoffs on the kill criteria those measurements would test.
`)
}
