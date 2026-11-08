package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/review"
	"github.com/spf13/cobra"
)

func reviewRead(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, review.MaxBytes+1))
	if e == nil && len(b) > review.MaxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", review.MaxBytes)
	}
	return b, e
}

// Resolve symlinks on existing parents before checking the destination. All
// outputs live outside the mesh to avoid outbox ingestion and source overwrite.
func reviewOutput(dirFlag *string, path string, data []byte) error {
	if path == "" {
		return fmt.Errorf("--out is required")
	}
	abs, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(abs))
	if e != nil {
		return e
	}
	dest := filepath.Join(parent, filepath.Base(abs))
	mesh, e := config.PortableDir(*dirFlag)
	if e != nil {
		return e
	}
	mesh, e = filepath.Abs(mesh)
	if e != nil {
		return e
	}
	if resolved, err := filepath.EvalSymlinks(mesh); err == nil {
		mesh = resolved
	}
	rel, e := filepath.Rel(mesh, dest)
	if e != nil {
		return e
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("review output must be outside the portable mesh")
	}
	f, e := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		_ = os.Remove(dest)
	}
	return e
}
func newReviewCmd(dirFlag *string) *cobra.Command {
	root := &cobra.Command{Use: "review", Short: "Prepare handoff review snapshots and validate local approval packets (never applies changes)"}
	groupGuard(root)
	var notes, memories []string
	var out, view string
	prepare := &cobra.Command{Use: "prepare", Short: "Snapshot explicitly selected handoffs and target memories for an agent or human reviewer", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if len(notes) == 0 || len(notes)+len(memories) > review.MaxSources {
			return fmt.Errorf("select at least one --handoff and at most %d sources", review.MaxSources)
		}
		b := review.Bundle{Version: 1, Created: timeNow().UTC().Format(time.RFC3339Nano)}
		seen := map[string]bool{}
		for _, role := range []string{"handoff", "memory"} {
			ids := notes
			if role == "memory" {
				ids = memories
			}
			for _, id := range ids {
				if seen[id] {
					return fmt.Errorf("duplicate source %q", id)
				}
				seen[id] = true
				resp, e := call(dirFlag, daemon.Request{Op: "fetch", MessageID: id, AgentView: view})
				if e != nil {
					return e
				}
				if !resp.OK {
					return fmt.Errorf("%s", resp.Error)
				}
				f := resp.Fetched
				if f == nil || f.Retracted || f.Expired || f.NotDelivered {
					return fmt.Errorf("source %s is unavailable or retracted", id)
				}
				body, e := reviewRead(f.BodyPath)
				if e != nil {
					return e
				}
				b.Sources = append(b.Sources, review.Source{ID: f.MessageID, Revision: f.RevisionID, Hash: f.BodyHash, Event: f.SourceEvent, Role: role, Body: string(body)})
			}
		}
		if e := b.Validate(); e != nil {
			return e
		}
		raw, e := json.MarshalIndent(b, "", "  ")
		if e != nil {
			return e
		}
		if len(raw) > review.MaxBytes {
			return fmt.Errorf("selected source bundle exceeds %d bytes; select fewer notes", review.MaxBytes)
		}
		if e = reviewOutput(dirFlag, out, raw); e != nil {
			return e
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Prepared %s (%d sources). Give the reviewer this bundle and these instructions:\n\n%s\nBundle hash: %s\n\nValidate the review with: cairn review packet --bundle <bundle.json> --proposals <proposals.json> --out <packet.md>\n", out, len(b.Sources), review.Instructions, review.Hash(raw))
		return nil
	}}
	prepare.Flags().StringSliceVar(&notes, "handoff", nil, "explicit handoff message IDs (repeatable/comma-separated)")
	prepare.Flags().StringSliceVar(&memories, "memory", nil, "optional existing memory IDs eligible for revision")
	prepare.Flags().StringVar(&out, "out", "", "new bundle JSON file outside the mesh")
	prepare.Flags().StringVar(&view, "view", "operator", "fetch view (existing session confinement still applies)")
	var bundlePath, proposalsPath, packetPath string
	packet := &cobra.Command{Use: "packet", Short: "Validate reviewer JSON against a snapshot and render a pending approval packet", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		raw, e := reviewRead(bundlePath)
		if e != nil {
			return e
		}
		var b review.Bundle
		if e = review.Decode(raw, &b); e != nil {
			return e
		}
		proposals, e := reviewRead(proposalsPath)
		if e != nil {
			return e
		}
		var r review.Response
		if e = review.Decode(proposals, &r); e != nil {
			return e
		}
		if e = review.Validate(b, raw, r); e != nil {
			return e
		}
		if e = reviewOutput(dirFlag, packetPath, []byte(review.Render(b, r))); e != nil {
			return e
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Pending approval packet: %s (%d proposals). No memory changes made.\n", packetPath, len(r.Proposals))
		return nil
	}}
	packet.Flags().StringVar(&bundlePath, "bundle", "", "prepared bundle JSON")
	packet.Flags().StringVar(&proposalsPath, "proposals", "", "reviewer response JSON")
	packet.Flags().StringVar(&packetPath, "out", "", "new Markdown approval packet outside the mesh")
	root.AddCommand(prepare, packet)
	return root
}
