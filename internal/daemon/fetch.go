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
	"github.com/ggoosen/cairn/internal/peer"
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
	if d.readOnly {
		// R9: no outbox ingestion, housekeeping, or enrichment appends —
		// reads only
		return d.Serve(ctx)
	}
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
		ticker := time.NewTicker(d.loaded.Portable.HousekeepInterval())
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
	// N5 sync listener (tailnet-only, mutual app-layer auth — R27). Trust is
	// the recover-time mesh snapshot; membership changes happen via OFFLINE
	// ceremonies (approve/revoke), so a daemon restart follows.
	//
	// R44: sync_listen defaults to AUTO — detect a tailnet interface and bind
	// <tailnet-ip>:9700, never 0.0.0.0. "off" disables deliberately; any other
	// value pins a literal tailnet address. R45: whatever the outcome, a core
	// subsystem that declines to start says so LOUDLY, with the remedy —
	// silence is never acceptable.
	if !d.readOnly {
		addr, reason := resolveSyncListen(d.loaded.Device.SyncListen, peer.DetectTailnetIP)
		if addr == "" {
			d.setSyncListenState("disabled: " + reason)
			fmt.Fprintf(d.warn, "sync listener: %s\n", reason)
		} else if srv, err := peer.NewServer(addr, peer.Identity{
			CairnID:  d.loaded.Portable.CairnID,
			DeviceID: d.loaded.Device.DeviceID,
			Priv:     d.devPriv,
		}, d.trust, d.warn); err != nil {
			// A bad bind is loud but not fatal (R45): the daemon still serves
			// local reads/writes; the operator fixes sync_listen and restarts.
			d.setSyncListenState(fmt.Sprintf("disabled: cannot bind %s (%v)", addr, err))
			fmt.Fprintf(d.warn, "sync listener: cannot bind %s — sync disabled: %v (fix sync_listen or set it to \"auto\"/\"off\")\n", addr, err)
		} else {
			// N6: reconciliation runs over each authenticated connection.
			srv.OnPeer = d.serveSync
			fmt.Fprintf(d.warn, "sync: listening on %s (tailnet-only; membership = root-chained certs)\n", srv.Addr())
			d.mu.Lock()
			d.syncSrv = srv
			d.mu.Unlock()
			d.setSyncListenState("listening on " + srv.Addr())
			go srv.Serve()
			go func() {
				<-ctx.Done()
				srv.Close()
			}()
		}
	}

	// N6: anti-entropy sweep (R29) — dial every configured peer on a timer
	// and on every push-on-append kick, running one bidirectional reconcile
	// per peer. A peer that is offline is logged and retried next tick.
	if len(d.loaded.Device.SyncPeers) > 0 && !d.readOnly {
		go d.antiEntropyLoop(ctx)
	}

	// background enricher (rulings §6: embeddings on an in-process
	// background thread; agents NEVER wait on it)
	go func() {
		ticker := time.NewTicker(config.EnrichInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := d.EnrichOnce(config.EnrichBatch); err != nil {
					fmt.Fprintf(d.warn, "WARNING: enricher: %v\n", err)
				}
				// N4: derivatives + summary checks share the enricher cadence
				if _, err := d.DeriveOnce(config.EnrichBatch); err != nil {
					fmt.Fprintf(d.warn, "WARNING: derivatives: %v\n", err)
				}
				if _, err := d.SummaryCheckOnce(config.EnrichBatch); err != nil {
					fmt.Fprintf(d.warn, "WARNING: summary check: %v\n", err)
				}
			}
		}
	}()
	return d.Serve(ctx)
}
