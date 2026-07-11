package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/object"
)

// FetchResult materializes a deliberate fetch (spec §7.3): manifest and body
// are SEPARATE files — authoritative metadata and retrieved content never
// share a document. The body file contains ONLY the body.
type FetchResult struct {
	MessageID    string `json:"message_id"`
	RevisionID   string `json:"revision_id"`
	BodyHash     string `json:"body_hash"`
	SourceEvent  string `json:"source_event_id"`
	Trust        string `json:"trust"` // always "untrusted"
	Retracted    bool   `json:"retracted,omitempty"`
	ManifestPath string `json:"manifest_path"`
	BodyPath     string `json:"body_path"`
	Expired      bool   `json:"content_expired,omitempty"`
}

// Fetch writes views/<agent>/fetched/{<id>.manifest.json, <id>.body.md}.
// Expired ephemeral content returns the typed content_expired result (a
// manifest is still written so provenance of the miss is inspectable).
func (d *Daemon) Fetch(messageID, agentView string) (*FetchResult, error) {
	if agentView == "" {
		agentView = "operator"
	}
	info, err := d.proj.MessageInfo(messageID)
	if err != nil {
		return nil, err
	}

	fetchedDir := filepath.Join(d.dir, config.ViewsDirName, agentView, "fetched")
	if err := d.fs.MkdirAll(fetchedDir, config.DirPerm); err != nil {
		return nil, err
	}
	res := &FetchResult{
		MessageID:    info.MessageID,
		RevisionID:   info.HeadRevisionID,
		BodyHash:     info.BodyHash,
		SourceEvent:  info.CreatedEventID,
		Trust:        "untrusted",
		Retracted:    info.Retracted,
		ManifestPath: filepath.Join(fetchedDir, messageID+".manifest.json"),
		BodyPath:     filepath.Join(fetchedDir, messageID+".body.md"),
	}

	refs := []object.Ref{{Hash: info.BodyHash, TextClass: info.TextClass, CreatedAt: parseWall(info.CreatedAt)}}
	body, err := d.store.Fetch(info.BodyHash, refs, d.now())
	var expired *object.ExpiredError
	if errors.As(err, &expired) {
		res.Expired = true
	} else if err != nil {
		return nil, fmt.Errorf("fetching body: %w", err)
	}

	manifest, err := json.MarshalIndent(map[string]any{
		"message_id":      res.MessageID,
		"revision_id":     res.RevisionID,
		"body_hash":       res.BodyHash,
		"source_event_id": res.SourceEvent,
		"trust":           "untrusted",
		"retracted":       res.Retracted,
		"content_expired": res.Expired,
		"text_class":      info.TextClass,
		"retrieved_at":    d.now().UTC().Format(config.WallTimeFormat),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	// overwrite-by-replace: fetched/ files are regenerated views
	d.fs.Remove(res.ManifestPath)
	if err := fsx.WriteFileAtomic(d.fs, res.ManifestPath, manifest, config.FilePerm); err != nil {
		return nil, err
	}
	if !res.Expired {
		d.fs.Remove(res.BodyPath)
		if err := fsx.WriteFileAtomic(d.fs, res.BodyPath, body, config.FilePerm); err != nil {
			return nil, err
		}
	} else {
		d.fs.Remove(res.BodyPath)
		res.BodyPath = ""
	}
	if res.Expired {
		return res, nil
	}
	return res, nil
}

func parseWall(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Run starts the background loops (outbox watcher + housekeeping) and the
// IPC listener; blocks until ctx is done.
func (d *Daemon) Run(ctx context.Context, processOutbox func() error) error {
	go func() {
		ticker := time.NewTicker(config.OutboxPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if processOutbox != nil {
					if err := processOutbox(); err != nil {
						fmt.Fprintf(d.warn, "WARNING: outbox pass: %v\n", err)
					}
				}
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(config.HousekeepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := d.Housekeep(); err != nil {
					fmt.Fprintf(d.warn, "WARNING: housekeeping: %v\n", err)
				}
			}
		}
	}()
	return d.Serve(ctx)
}
