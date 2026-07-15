package projection

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
)

// ReindexReport summarizes a rebuild: reindex COMPLETES (exit 0) even with
// parked events — the report makes them loud (FIX-F1 ruling 3). Parked splits
// into Retryable (R49: a missing intra-mesh reference whose dependency has not
// replicated to this node yet — expected during active sync) and terminal
// (Parked-Retryable: genuine corruption).
type ReindexReport struct {
	Events          int64 `json:"events"`
	Parked          int   `json:"parked"`
	ParkedRetryable int   `json:"parked_retryable"`
}

// DBPath is the live projection database location (derived state).
func DBPath(portableDir string) string {
	return filepath.Join(portableDir, config.DerivedDirName, "index.sqlite")
}

// StoreBodyFetch adapts the object store as the projection's body resolver:
// missing/expired/corrupt objects simply do not get lexically indexed.
func StoreBodyFetch(store *object.Store) BodyFetch {
	return func(hash string) ([]byte, bool) {
		data, err := store.Get(hash)
		return data, err == nil
	}
}

// Replay walks every origin read-only (log.Walk, Strict) and applies each
// verified event past the projection checkpoint. Idempotent: run it any
// number of times, after any crash — already-applied events are skipped
// inside Apply's checkpoint transaction. verifyFor supplies a FRESH chain
// verifier per call (key learning starts from genesis).
func Replay(p *Projection, fsys fsx.FS, portableDir string, verifyFor func() cairnlog.VerifyFunc) error {
	if verifyFor == nil {
		// FIX-F2 ruling 1: two-pass replay. Pass 1 establishes the mesh-wide
		// membership/key set across ALL origins (the security log is one
		// stream class); pass 2 verifies each origin against that set — so a
		// device admitted in another origin's log (migration) verifies.
		trust, err := identity.MeshTrust(fsys, portableDir)
		if err != nil {
			return err
		}
		verifyFor = func() cairnlog.VerifyFunc { return trust.Verifier() }
	}
	origins, err := cairnlog.Origins(fsys, portableDir)
	if err != nil {
		return err
	}
	for _, origin := range origins {
		verify := verifyFor()
		_, err := cairnlog.Walk(fsys, portableDir, origin, verify,
			func(env *event.Envelope, rec []byte) error {
				return p.Apply(env, rec)
			})
		if err != nil {
			return fmt.Errorf("replaying origin %s/%d: %w", origin.DeviceID, origin.Generation, err)
		}
	}
	// R49.2: a final self-heal sweep after every origin is applied. Cross-node
	// replication has no global ordering (an origin whose topic.create replicated
	// LATER than a peer's topic.link.add is applied after it here), so a retryable
	// park from an earlier origin heals once its dependency's origin is walked.
	if _, err := p.RetryParked(); err != nil {
		return err
	}
	return nil
}

// ReindexLexical rebuilds the ENTIRE projection beside the live database and
// atomically swaps it in (rulings §6: side-build + atomic rename; the
// projection is derived, so mid-rebuild crashes just leave a stale .rebuild
// to overwrite next time).
// dbPath is the live projection database (a REAL filesystem path — SQLite
// runs outside fsx; the projection is derived state, crash-safe by rebuild).
// Production callers pass DBPath(portableDir).
func ReindexLexical(fsys fsx.FS, portableDir, dbPath string, verifyFor func() cairnlog.VerifyFunc, bodyFetch BodyFetch) (*ReindexReport, error) {
	live := dbPath
	if err := os.MkdirAll(filepath.Dir(live), config.DirPerm); err != nil {
		return nil, err
	}
	rebuild := live + ".rebuild"
	for _, stale := range []string{rebuild, rebuild + "-wal", rebuild + "-shm"} {
		os.Remove(stale)
	}

	p, err := Open(rebuild, bodyFetch)
	if err != nil {
		return nil, fmt.Errorf("opening rebuild db: %w", err)
	}
	if err := Replay(p, fsys, portableDir, verifyFor); err != nil {
		p.Close()
		return nil, err
	}
	report := &ReindexReport{}
	parked, err := p.ParkedEvents()
	if err != nil {
		p.Close()
		return nil, err
	}
	report.Parked = len(parked)
	for _, pe := range parked {
		if pe.Retryable {
			report.ParkedRetryable++
		}
	}
	p.db.QueryRow(`SELECT count(*) FROM events`).Scan(&report.Events)
	// fold WAL into the main file so the rename moves ONE complete db
	if _, err := p.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		p.Close()
		return nil, err
	}
	if err := p.Close(); err != nil {
		return nil, err
	}

	// atomic swap; stale live WAL/SHM must not shadow the new file
	for _, stale := range []string{live + "-wal", live + "-shm"} {
		os.Remove(stale)
	}
	if err := os.Rename(rebuild, live); err != nil {
		return nil, fmt.Errorf("swapping rebuilt projection: %w", err)
	}
	return report, nil
}

// ReindexSemantic is the M3 stub (BUILD-PLAN): embeddings and the vector
// backfill land in M6.
func ReindexSemantic() error {
	return fmt.Errorf("reindex --semantic is not available until M6 (embeddings); the lexical projection is unchanged")
}
