package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ggoosen/cairn/eval/internal/backend"
)

func runBackends(args []string) error {
	fs := flag.NewFlagSet("backends", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	out := os.Stdout
	fmt.Fprintln(out, "memory conditions (BUILD-PLAN §5-E4):")
	for _, id := range backend.All() {
		b, err := backend.New(id)
		if err != nil {
			return err
		}
		caps := b.Capabilities()
		surfaces := "none"
		if len(caps.Surfaces) > 0 {
			parts := make([]string, 0, len(caps.Surfaces))
			for _, s := range caps.Surfaces {
				parts = append(parts, string(s))
			}
			surfaces = strings.Join(parts, ",")
		}
		status := "implemented"
		if backend.IsStub(b) {
			status = "STUB"
		}
		fmt.Fprintf(out, "  %-3s %-12s surfaces=%-14s chronological=%-5v\n       %s\n",
			id, status, surfaces, caps.Chronological, caps.Notes)
	}
	return nil
}
