package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
)

func newReindexCmd(dirFlag *string) *cobra.Command {
	var lexical, semantic bool
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild the derived SQLite projection from the event log (side-build + atomic swap)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// DEPLOY-E5: the README promise is "delete the index; cairn
			// reindex rebuilds it" — bare invocation means the lexical
			// rebuild, the operation that promise describes.
			if !lexical && !semantic {
				lexical = true
			}
			dir, err := config.PortableDir(*dirFlag)
			if err != nil {
				return err
			}
			loaded, err := identity.Load(dir)
			if err != nil {
				return err
			}
			if err := loaded.StartupCheck(nil, cmd.ErrOrStderr()); err != nil {
				return err
			}
			if semantic {
				resp, err := call(dirFlag, daemon.Request{Op: "reindex-semantic"})
				if err != nil {
					return fmt.Errorf("reindex --semantic runs through the daemon: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "semantic reindex embedded %v revisions\n", resp.Status["embedded"])
			}
			if lexical {
				// P2H2 (Codex P2 shakedown MAJOR-1): a lexical reindex side-builds
				// and ATOMICALLY SWAPS index.sqlite. A running daemon holds its own
				// open handle to the pre-swap file, so after the swap the daemon's
				// in-memory view and the on-disk projection diverge until restart —
				// any external read of index.sqlite in that window sees stale state.
				// Refuse against a live daemon rather than silently split state; the
				// daemon reconciles its own projection from the log on startup, so
				// the correct sequence is stop → reindex → restart.
				if _, err := call(dirFlag, daemon.Request{Op: "status"}); err == nil {
					return errors.New("a daemon is running for this cairn — a lexical reindex " +
						"atomically swaps index.sqlite and would split the running daemon's view " +
						"from disk until restart. Stop the daemon (stop the launchd/systemd service, " +
						"or kill `cairn daemon`), run `cairn reindex --lexical`, then start it again " +
						"(the daemon reconciles its projection from the log on startup)")
				}
				fsys := fsx.OS{}
				store := object.NewStore(fsys, dir)
				// FIX-J3 (R45): a reindex must be observable and bounded, never a
				// silent hang. A stall watchdog aborts if no event is applied for
				// ReindexStallTimeout (the observed WSL2 hang leaves only a
				// discardable .rebuild); a throttled heartbeat logs progress.
				report, err := reindexWithWatchdog(cmd, fsys, dir, store)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "lexical projection rebuilt at %s (%d events)\n", projection.DBPath(dir), report.Events)
				if report.Parked > 0 {
					terminal := report.Parked - report.ParkedRetryable
					if terminal > 0 {
						fmt.Fprintf(cmd.ErrOrStderr(), "\nATTENTION: %d event(s) PARKED (%d terminal, %d retryable). Run `cairn doctor` for details.\n", report.Parked, terminal, report.ParkedRetryable)
					} else {
						// R49: retryable parks are a missing intra-mesh reference
						// (e.g. a topic.link.add whose topic.create has not replicated
						// yet); they self-heal on the event that satisfies them.
						fmt.Fprintf(cmd.ErrOrStderr(), "\nNOTE: %d event(s) parked RETRYABLE (a dependency has not replicated yet; self-heals on sync). Run `cairn doctor` for details.\n", report.ParkedRetryable)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&lexical, "lexical", false, "rebuild the lexical projection (fast; the default when no flag is given)")
	cmd.Flags().BoolVar(&semantic, "semantic", false, "re-embed every head revision through the daemon (model migration: invalidate + full re-embed)")
	return cmd
}

// reindexWithWatchdog runs a lexical reindex under a progress heartbeat and a
// stall watchdog (FIX-J3, R45). It cancels the reindex context if no event is
// applied for config.ReindexStallTimeout, so a wedged filesystem produces a
// clear error and a discardable .rebuild rather than an indefinite hang. The
// heartbeat (throttled to config.ReindexProgressInterval) makes a slow-but-live
// reindex visible.
func reindexWithWatchdog(cmd *cobra.Command, fsys fsx.FS, dir string, store *object.Store) (*projection.ReindexReport, error) {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	var applied int64
	lastProgress := atomicTime{}
	lastProgress.set(time.Now())
	var lastLog time.Time

	progress := func(n int64) {
		atomic.StoreInt64(&applied, n)
		lastProgress.set(time.Now())
		if now := time.Now(); now.Sub(lastLog) >= config.ReindexProgressInterval {
			lastLog = now
			fmt.Fprintf(cmd.ErrOrStderr(), "reindex: %d events applied…\n", n)
		}
	}

	stalled := make(chan struct{})
	go func() {
		t := time.NewTicker(config.ReindexStallTimeout / 4)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(lastProgress.get()) >= config.ReindexStallTimeout {
					close(stalled)
					cancel()
					return
				}
			}
		}
	}()

	report, err := projection.ReindexLexicalCtx(ctx, fsys, dir, projection.DBPath(dir),
		nil, projection.StoreBodyFetch(store), progress)
	select {
	case <-stalled:
		return nil, fmt.Errorf("reindex STALLED: no progress for %v after %d events — aborted (the .rebuild is discardable). "+
			"A pathologically slow filesystem (e.g. WSL2 drvfs on a Windows-mounted path) is the usual cause; "+
			"move the cairn dir to a native Linux filesystem. Original: %w", config.ReindexStallTimeout, atomic.LoadInt64(&applied), err)
	default:
	}
	return report, err
}

// atomicTime is a tiny mutex-guarded time holder for the watchdog.
type atomicTime struct {
	mu sync.Mutex
	t  time.Time
}

func (a *atomicTime) set(t time.Time) { a.mu.Lock(); a.t = t; a.mu.Unlock() }
func (a *atomicTime) get() time.Time  { a.mu.Lock(); defer a.mu.Unlock(); return a.t }
