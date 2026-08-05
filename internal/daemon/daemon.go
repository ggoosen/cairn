// Package daemon implements the resident single-writer daemon (rulings §6):
// it owns appends, object commits, the SQLite projection, outbox ingestion,
// and ephemeral-TTL housekeeping. CLI mutations go through the daemon or
// fail — they never append independently. Ownership = OS file lock + unix
// socket, never PID files alone.
package daemon

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/maintenance"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/peer"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/telemetry"
)

// Options parameterizes daemon startup (fs/clock/db path injectable for the
// fault tests).
type Options struct {
	Dir      string // portable dir (resolved)
	DBPath   string // projection db ("" → DBPath(Dir))
	FS       fsx.FS // "" → fsx.OS{}
	Checker  identity.VolumeChecker
	Now      func() time.Time
	Warn     io.Writer
	Embedder embed.Embedder // nil → embed.Detect(Dir); lexical_only if none
	Version  string         // FIX-H7: build version string of THIS binary (stale-binary detection)
}

// Daemon is the single writer for one cairn.
type Daemon struct {
	fs      fsx.FS
	dir     string
	loaded  *identity.Loaded
	devPriv ed25519.PrivateKey
	keyID   string

	mu sync.Mutex // serializes ALL mutations (single writer)
	// embMu guards the embedder POINTER only (swapped by SetEmbedderForTest
	// while the enricher goroutine reads it); a dedicated lock so snapshot
	// reads never interact with d.mu. The embedder itself serializes its own
	// worker I/O internally.
	embMu    sync.RWMutex
	embedder embed.Embedder
	trust    *identity.Trust
	lg       *cairnlog.Log
	// logs holds append handles for FOREIGN origins (N6 replication ingest +
	// frontier). The active origin is d.lg; the map excludes it.
	logs          map[cairnlog.Origin]*cairnlog.Log
	bootstrapMode bool // joined node running on grant-chain trust until genesis replicates (R37)
	proj          *projection.Projection
	store         *object.Store
	usage         *object.UsageTracker
	verifier      *identity.ChainVerifier
	now           func() time.Time

	lockFile *os.File
	warn     io.Writer
	tel      *telemetry.Store

	readOnly bool   // portable-only restore: reads allowed, writes refused (R9)
	sockDir  string // where daemon.sock.path is registered
	version  string // FIX-H7: build version this daemon is RUNNING (for stale-binary detection)

	sessions *sessions    // N2 capability handles (guarded by mu via dispatch)
	syncSrv  *peer.Server // N5 sync listener (nil unless configured)

	syncListen string // R44/R45: human-readable listener state (guarded by mu)
	manualSync bool   // G7.1: a background `sync now` sweep is in flight (guarded by mu)

	degradeLevel     maintenance.Level      // P2-1: current degradation rung (guarded by mu)
	ladderThresholds maintenance.Thresholds // P2-1: ladder thresholds (default from config; test-overridable)
	rankP2           bool                   // P2-3: use the full P2 additive ranking profile (opt-in; guarded by mu)
	heavyDerive      bool                   // P2-7: run opt-in heavy derivatives (OCR/etc.) for image/audio blobs

	syncBulkThreshold int           // N6: overrides config.SyncBulkCatchupThreshold in tests
	syncKick          chan struct{} // N6: push-on-append nudges the anti-entropy sweep (R29)

	// R50: ephemeral live-delivery windows (0 → config defaults; test-overridable)
	ephSendWindow   time.Duration // sender: offer ephemeral bodies only this soon after publish
	ephAcceptWindow time.Duration // receiver: accept ephemeral bodies only this close to the event's wall time

	durab *durabilityRegistry // N7: per-blob peer-holder registry (guarded by d.mu)

	forks map[cairnlog.Origin]*ForkRecord // N8: detected equivocations (guarded by d.mu)

	// admittedPairings records pairing invite_ids (and dev:<device_id>) admitted
	// THIS session (P3-2b, guarded by d.mu), so a replay within one daemon
	// lifetime is refused even before d.trust reflects the durable device.add
	// (which happens on the next restart, like every membership change today).
	admittedPairings map[string]bool

	// peerRoles records each peer's advertised node role (P3-3), learned at the
	// sync frontier exchange (runtime, non-replicated — like blob holdership). A
	// device known to be RoleThin is excluded from the durability target
	// (spec §7). Guarded by d.mu.
	peerRoles map[string]string

	// transport (P3-4) is the resolved sync transport. nil means the configured
	// transport is unavailable (e.g. iroh, deferred) → sync is disabled but local
	// reads/writes continue (R45). transportName is the configured name, for
	// status. Set once at Start, read-only thereafter.
	transport     peer.Transport
	transportName string
}

// syncIdentity returns this node's peer identity for dialing. Caller holds
// d.mu (reads device fields + key).
func (d *Daemon) syncIdentity() peer.Identity {
	return peer.Identity{
		CairnID:  d.loaded.Portable.CairnID,
		DeviceID: d.loaded.Device.DeviceID,
		Priv:     d.devPriv,
	}
}

// SyncAddr returns the bound sync listener address ("" if not listening).
func (d *Daemon) SyncAddr() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.syncSrv == nil {
		return ""
	}
	return d.syncSrv.Addr()
}

// SyncListenState returns the human-readable sync listener state for
// `cairn sync status` (R44/R45): "listening on <addr>" or "disabled: <reason>".
func (d *Daemon) SyncListenState() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.syncListen == "" {
		return "not started"
	}
	return d.syncListen
}

func (d *Daemon) setSyncListenState(s string) {
	d.mu.Lock()
	d.syncListen = s
	d.mu.Unlock()
}

// beginManualSync claims the single-in-flight `sync now` slot (G7.1). Returns
// false if a background sweep is already running.
func (d *Daemon) beginManualSync() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.manualSync {
		return false
	}
	d.manualSync = true
	return true
}

func (d *Daemon) endManualSync() {
	d.mu.Lock()
	d.manualSync = false
	d.mu.Unlock()
}

// resolveSyncListen maps the configured sync_listen value to the address the
// listener should bind (R44). It returns ("", reason) when the listener should
// NOT start — the reason is logged loudly (R45) and surfaced in sync status.
// "off" disables deliberately; "" / "auto" auto-detect a tailnet interface via
// detect; anything else is a literal address passed through to NewServer
// (which validates it is a tailnet address, never 0.0.0.0).
func resolveSyncListen(configured string, detect func() (string, bool)) (addr, reason string) {
	switch configured {
	case config.SyncListenOff:
		return "", `disabled (sync_listen = "off") — set sync_listen to a tailnet address or "auto" to enable`
	case "", config.SyncListenAuto:
		if ip, ok := detect(); ok {
			return net.JoinHostPort(ip, strconv.Itoa(config.SyncDefaultPort)), ""
		}
		return "", "no tailnet interface found — sync disabled (set sync_listen to override)"
	default:
		return configured, ""
	}
}

// ErrReadOnly: the mesh is a portable-only restore (spec §3.2 / RULINGS.md
// R9) — every event-appending or object-mutating operation is refused.
var ErrReadOnly = errors.New("restored copy: writes are refused (no device identity); run `cairn init --adopt` to create a new origin identity, or use read-only commands")

func (d *Daemon) writable() error {
	if d.readOnly {
		return ErrReadOnly
	}
	return nil
}

// ClientDir resolves where a CLI client finds the daemon's socket
// registration: the device state dir normally, the derived dir for a
// read-only restore daemon.
func ClientDir(portableDir string) (string, error) {
	loaded, err := identity.Load(portableDir)
	if err == nil {
		return loaded.DeviceDir, nil
	}
	if errors.Is(err, identity.ErrRestoredCopy) {
		return filepath.Join(portableDir, config.DerivedDirName), nil
	}
	return "", err
}

// Start loads identity, acquires the single-writer lock, recovers the log
// (repairing the active origin, replaying every origin into the projection
// past its checkpoint), reconciles the sequence cache, and returns a running
// daemon (IPC serving is Serve; outbox/housekeeping loops are Run).
func Start(opts Options) (*Daemon, error) {
	if opts.FS == nil {
		opts.FS = fsx.OS{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Warn == nil {
		opts.Warn = io.Discard
	}
	if opts.DBPath == "" {
		opts.DBPath = projection.DBPath(opts.Dir)
	}

	readOnly := false
	loaded, err := identity.Load(opts.Dir)
	if errors.Is(err, identity.ErrRestoredCopy) {
		// R9: read-only daemon over restored portable data
		readOnly = true
		portable, perr := config.LoadPortable(opts.Dir)
		if perr != nil {
			return nil, perr
		}
		loaded = &identity.Loaded{Dir: opts.Dir, Portable: portable}
		if err := identity.EnsureEncrypted(opts.Dir, opts.Checker, opts.Warn); err != nil {
			return nil, err
		}
		fmt.Fprintln(opts.Warn, "NOTICE: restored copy — serving READ-ONLY (search/digest/fetch); writes refused; `cairn init --adopt` creates a new origin")
	} else if err != nil {
		return nil, err
	} else {
		if err := loaded.StartupCheck(opts.Checker, opts.Warn); err != nil {
			return nil, err
		}
	}
	var devPriv ed25519.PrivateKey
	if !readOnly {
		devPriv, err = identity.LoadKey(filepath.Join(loaded.DeviceDir, config.DeviceKeyName))
		if err != nil {
			return nil, fmt.Errorf("loading device key: %w", err)
		}
	}

	// single-writer lock (device-local; derived dir in read-only mode)
	lockDir := loaded.DeviceDir
	if readOnly {
		lockDir = filepath.Join(opts.Dir, config.DerivedDirName)
		os.MkdirAll(lockDir, 0o700)
	}
	lockPath := filepath.Join(lockDir, config.DaemonLockName)
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("another daemon holds the write lock for this cairn: %w", err)
	}

	// N6: a joined node (R37) starts with only portable config + identity —
	// its derived/object/view scaffold is created on first daemon start so
	// replicated events, bodies, and the projection have somewhere to land.
	if !readOnly {
		for _, sub := range []string{config.EventsDirName, config.ObjectsDirName, config.ExportsDirName, config.ViewsDirName, config.DerivedDirName} {
			if err := os.MkdirAll(filepath.Join(opts.Dir, sub), config.DirPerm); err != nil {
				syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
				lockFile.Close()
				return nil, fmt.Errorf("creating portable scaffold: %w", err)
			}
		}
	}

	if opts.Embedder == nil {
		var reason string
		opts.Embedder, reason = embed.DetectVerbose(opts.Dir)
		// R45 (FIX-G5): never fall back to lexical-only SILENTLY. Say so at
		// startup, on every platform, with the remedy. A loaded embedder is
		// also announced so the operator can confirm cross-node parity.
		if w := opts.Warn; w != nil {
			if opts.Embedder == nil {
				fmt.Fprintf(w, "embeddings: %s\n", reason)
			} else {
				fmt.Fprintf(w, "embeddings: semantic search enabled (%s)\n", opts.Embedder.ModelID())
			}
		}
	}
	d := &Daemon{
		fs:       opts.FS,
		dir:      opts.Dir,
		loaded:   loaded,
		devPriv:  devPriv,
		store:    object.NewStore(opts.FS, opts.Dir),
		verifier: identity.NewChainVerifier(),
		now:      opts.Now,
		version:  opts.Version,
		lockFile: lockFile,
		warn:     opts.Warn,
		embedder: opts.Embedder,
		readOnly: readOnly,
		sockDir:  lockDir,
		logs:     map[cairnlog.Origin]*cairnlog.Log{},
		syncKick: make(chan struct{}, 1),
		durab:    loadDurability(opts.FS, opts.Dir),
		forks:    loadForks(opts.Dir),

		ladderThresholds: maintenance.DefaultThresholds(),
		rankP2:           os.Getenv("CAIRN_RANK_PROFILE") == "p2",     // P2-3 opt-in until §9.3 calibration
		heavyDerive:      os.Getenv("CAIRN_HEAVY_DERIVATIVES") == "1", // P2-7 opt-in (may shell out)
	}
	if devPriv != nil {
		d.keyID = event.KeyID(devPriv.Public().(ed25519.PublicKey))
	}

	proj, err := projection.Open(opts.DBPath, projection.StoreBodyFetch(d.store))
	if errors.Is(err, projection.ErrSchemaVersion) {
		// the projection is DERIVED: on schema drift, rebuild from the log
		fmt.Fprintf(opts.Warn, "WARNING: %v — rebuilding the derived projection from the log\n", err)
		for _, f := range []string{opts.DBPath, opts.DBPath + "-wal", opts.DBPath + "-shm"} {
			os.Remove(f)
		}
		proj, err = projection.Open(opts.DBPath, projection.StoreBodyFetch(d.store))
	}
	if err != nil {
		d.releaseLock()
		return nil, err
	}
	d.proj = proj
	// FIX-F8.3 (R4.3 "logged loudly"): announce parking the moment it happens
	proj.SetParkLogger(func(pe projection.ParkedEvent) {
		fmt.Fprintf(d.warn, "PARKED EVENT: %s (%s, origin %s seq %d) failed projection: %s — the stream continues; run `cairn doctor` for details\n",
			pe.EventID, pe.EventType, pe.Origin, pe.Sequence, pe.Error)
	})

	tel, err := telemetry.Open(telemetry.Path(opts.Dir))
	if err != nil {
		d.Close()
		return nil, err
	}
	d.tel = tel

	// N2: capability profiles (device-local TOML + builtins) and the
	// session-handle table. A bad profiles.toml fails startup loudly.
	profiles, err := LoadProfiles(lockDir)
	if err != nil {
		d.Close()
		return nil, err
	}
	sess, err := loadSessions(lockDir, profiles, d.now())
	if err != nil {
		d.Close()
		return nil, err
	}
	d.sessions = sess

	usage, err := object.LoadUsage(opts.FS, opts.Dir, loaded.Portable.DailyCanonicalBytes)
	if err != nil {
		d.Close()
		return nil, err
	}
	d.usage = usage

	if err := d.recover(); err != nil {
		d.Close()
		return nil, err
	}
	// P3-4: resolve the sync transport (P3-1 seam). An unavailable transport
	// (iroh, deferred) disables sync LOUDLY (R45) without taking the daemon down.
	d.transportName = config.TransportTCPTailnet
	if d.loaded.Device != nil {
		if d.loaded.Device.Transport != "" {
			d.transportName = d.loaded.Device.Transport
		}
		if tr, terr := peer.TransportByName(d.loaded.Device.Transport); terr == nil {
			d.transport = tr
		} else if !d.readOnly {
			fmt.Fprintf(d.warn, "sync transport unavailable: %v — sync DISABLED (local reads/writes unaffected)\n", terr)
			d.setSyncListenState("disabled: " + terr.Error())
		}
	}
	if !d.readOnly {
		if err := d.EnsureReserve(); err != nil {
			fmt.Fprintf(d.warn, "WARNING: emergency reserve not preallocated: %v\n", err)
		}
	}
	d.store.SetTTL(loaded.Portable.EphemeralTTL())
	if !d.readOnly {
		// one housekeeping sweep at startup (RULINGS.md R10)
		if deleted, err := d.Housekeep(); err != nil {
			fmt.Fprintf(d.warn, "WARNING: startup housekeeping: %v\n", err)
		} else if len(deleted) > 0 {
			fmt.Fprintf(d.warn, "housekeeping: removed %d expired ephemeral object(s)\n", len(deleted))
		}
	}
	return d, nil
}

// recover establishes mesh-level trust (FIX-F2 pass 1 over all origins),
// then opens/repairs the ACTIVE origin and replays every other origin
// against the seeded key set, projecting as it goes (pass 2).
func (d *Daemon) recover() error {
	trust, err := identity.MeshTrust(d.fs, d.dir)
	// R37: a freshly-joined node has no local genesis yet — its origin log
	// arrives via N6 replication. Until then it authenticates peers and
	// verifies replicated records from the grant chain (BootstrapTrust), which
	// is itself genesis-rooted and root-verified. This also covers a node
	// whose genesis-bearing foreign origin was lost (a deleted-segment drill):
	// MeshTrust cannot resolve the local chain, so we fall back to the grant
	// chain and let N6 re-replicate the missing segments. A later start, once
	// A's origin (with genesis) is local again, resolves via MeshTrust and
	// this branch is skipped.
	// RULING-NEEDED: R37 says a FRESHLY-JOINED node uses grant-chain bootstrap
	// trust until segments replicate, and that N6 must "delete the crutch"
	// once replicated. We additionally fall back to bootstrap trust when a
	// previously-complete local chain becomes UNRESOLVABLE (its genesis-bearing
	// foreign origin was lost — the deleted-segment drill), and we RETAIN
	// bootstrap trust as a resilience fallback rather than deleting it. This is
	// safe (bootstrap trust is genesis-rooted and root-verified, and MeshTrust
	// wins whenever the local chain resolves), but it is broader than R37's
	// literal wording; flagged for author confirmation.
	needBootstrap := !d.readOnly && (err != nil || trust == nil || trust.GenesisEnv == nil)
	if needBootstrap {
		if bt, berr := identity.BootstrapTrust(d.loaded.DeviceDir); berr == nil && bt.GenesisEnv != nil {
			trust = bt
			d.bootstrapMode = true
			if err != nil {
				fmt.Fprintf(d.warn, "sync: local chain incomplete (%v) — running on grant-chain bootstrap trust; N6 re-replicates the log (R37)\n", err)
			} else {
				fmt.Fprintln(d.warn, "sync: no local genesis yet — running on grant-chain bootstrap trust until the log replicates (N6/R37)")
			}
			err = nil
		}
	}
	if err != nil {
		return err
	}
	d.trust = trust
	apply := func(env *event.Envelope, rec []byte) error { return d.proj.Apply(env, rec) }

	origins, err := cairnlog.Origins(d.fs, d.dir)
	if err != nil {
		return err
	}

	if d.readOnly {
		// R9: project everything; no append handle, no seq reconcile
		for _, o := range origins {
			if _, err := cairnlog.Walk(d.fs, d.dir, o, trust.Verifier(), apply); err != nil {
				return fmt.Errorf("replaying origin %s/%d: %w", o.DeviceID, o.Generation, err)
			}
		}
		// R49.2: heal cross-origin parks once every origin is projected.
		_, err := d.proj.RetryParked()
		return err
	}

	active := cairnlog.Origin{DeviceID: d.loaded.Device.DeviceID, Generation: d.loaded.Device.OriginGeneration}
	for _, o := range origins {
		if o == active {
			continue
		}
		// N6: foreign origins get an APPEND handle (replication ingest +
		// frontier), not just a read-only Walk. Open projects as it scans,
		// exactly as Walk did, and repairs a torn tail from a crash-mid-sync.
		lg, _, err := cairnlog.Open(d.fs, d.dir, o, trust.Verifier(), apply)
		if err != nil {
			return fmt.Errorf("replaying origin %s/%d: %w", o.DeviceID, o.Generation, err)
		}
		d.logs[o] = lg
	}
	lg, report, err := cairnlog.Open(d.fs, d.dir, active, trust.Verifier(), apply)
	if err != nil {
		return err
	}
	d.lg = lg
	if _, err := identity.ReconcileSeqState(d.fs, d.loaded.DeviceDir, active.DeviceID, active.Generation, report.NextSeq, d.warn); err != nil {
		return err
	}

	// R49.2: heal cross-origin parks once every origin (foreign + active) is
	// projected — a topic.link.add on one origin that parked ahead of its
	// topic.create on another clears now that both are applied.
	if _, err := d.proj.RetryParked(); err != nil {
		return err
	}

	// A revoked device must never write (row 15: old origin read-only).
	// Revocation gates WRITES only; historical events remain valid (F2 r.3).
	if trust.Revoked(active.DeviceID) {
		return fmt.Errorf("device %s is revoked; this origin is read-only (complete the migrate ceremony or use the new device identity)", active.DeviceID)
	}
	return nil
}

func isKeyOrderError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "before genesis") || strings.Contains(msg, "unknown signing key")
}

func (d *Daemon) releaseLock() {
	if d.lockFile != nil {
		syscall.Flock(int(d.lockFile.Fd()), syscall.LOCK_UN)
		d.lockFile.Close()
		d.lockFile = nil
	}
}

// Close releases everything (does not seal the open segment).
func (d *Daemon) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var first error
	if d.lg != nil {
		if err := d.lg.Close(); err != nil && first == nil {
			first = err
		}
		d.lg = nil
	}
	for o, lg := range d.logs {
		if err := lg.Close(); err != nil && first == nil {
			first = err
		}
		delete(d.logs, o)
	}
	if d.proj != nil {
		if err := d.proj.Close(); err != nil && first == nil {
			first = err
		}
		d.proj = nil
	}
	if d.tel != nil {
		d.tel.Close()
		d.tel = nil
	}
	d.releaseLock()
	return first
}

// --- the write path (rulings §3; ack only after Append returns) -------------

// PublishRequest is one send/reply mutation.
type PublishRequest struct {
	Actor            string   `json:"actor"` // principal (agent view name or "operator")
	TaskID           string   `json:"task_id,omitempty"`
	AgentInstanceID  string   `json:"agent_instance_id,omitempty"`
	CorrelationID    string   `json:"correlation_id,omitempty"` // outbox request_id
	Body             string   `json:"body"`
	BodyMime         string   `json:"body_mime,omitempty"`
	TextClass        string   `json:"text_class,omitempty"`
	DeclaredPriority int      `json:"declared_priority,omitempty"`
	OperatorOverride bool     `json:"operator_override,omitempty"` // CLI-only; never settable via outbox
	MessageID        string   `json:"message_id,omitempty"`        // caller-supplied logical id (idempotency)
	RevisionID       string   `json:"revision_id,omitempty"`
	ThreadID         string   `json:"thread_id,omitempty"`
	ReplyToMessageID string   `json:"reply_to_message_id,omitempty"`
	Recipients       []string `json:"recipients,omitempty"`
	// Topics: initial topic links (separate events, SAME request, all durable
	// before the single ack — FIX-F1). Each entry resolves as topic_id, then
	// topic name; unresolved entries auto-create when AutoCreateTopics (the
	// operator CLI path) or reject BEFORE ack otherwise.
	Topics           []string `json:"topics,omitempty"`
	AutoCreateTopics bool     `json:"auto_create_topics,omitempty"`

	// M9 ingest hooks (schema: optional fields on message.publish)
	SourceRef *SourceRef `json:"source_ref,omitempty"`
	RelatesTo []string   `json:"relates_to,omitempty"`

	// N4: attachments (content-addressed blobs; derivatives make them
	// searchable) and the sender's UNTRUSTED summary claim (spec §8.4).
	Attachments   []AttachmentIn `json:"attachments,omitempty"`
	SenderSummary string         `json:"sender_summary,omitempty"`

	// N7: blob durability class for this message's attachments (spec §6.3).
	// ephemeral | normal (default) | important | pinned. Empty → normal.
	Durability string `json:"durability,omitempty"`
}

// AttachMeta is the header for a streamed attachment stage (G6): filename +
// mime hint + exact byte length. The raw bytes follow the JSON request line on
// the same connection.
type AttachMeta struct {
	Filename string `json:"filename,omitempty"`
	Mime     string `json:"mime,omitempty"`
	ByteLen  int    `json:"byte_len"`
}

// AttachmentIn carries one attachment's bytes into Publish (base64 over
// IPC via encoding/json's []byte handling).
type AttachmentIn struct {
	Data     []byte `json:"data"`
	Filename string `json:"filename,omitempty"`
	Mime     string `json:"mime,omitempty"` // optional hint; content is SNIFFED regardless

	// G6: a pre-staged attachment references its object by hash (streamed via
	// stage-attachment) instead of carrying inline bytes. When set with empty
	// Data, the publish path uses the existing object rather than Put-ing bytes.
	ObjectHash string `json:"object_hash,omitempty"`
	ByteLen    int    `json:"byte_len,omitempty"`
}

// SourceRef is the ingest provenance payload (schema §message.publish).
type SourceRef struct {
	Path        string `json:"path"`
	Repo        string `json:"repo,omitempty"`
	ContentHash string `json:"content_hash"`
	ImportedAt  string `json:"imported_at"`
}

// PublishResult is the acknowledgement (and receipt body — deterministic
// for a given request, so receipt retries regenerate identical bytes).
type PublishResult struct {
	EventIDs        []string `json:"event_ids"`
	MessageID       string   `json:"message_id"`
	RevisionID      string   `json:"revision_id"`
	BodyHash        string   `json:"body_hash"`
	TextClass       string   `json:"text_class"`
	Downgraded      bool     `json:"downgraded,omitempty"`
	DowngradeReason string   `json:"downgrade_reason,omitempty"`

	// N7: blob replication acknowledgement (spec §6.3). Present only when the
	// message carries attachments. accepted_locally is always true (the send
	// path never blocks on replication); Pending is true until durability is
	// satisfied by enough peers.
	Replication *ReplicationState `json:"replication,omitempty"`
}

// ReplicationState is the durability acknowledgement for a message's blobs.
type ReplicationState struct {
	Durability string `json:"durability"` // class
	Target     int    `json:"target"`     // required replica nodes
	Have       int    `json:"have"`       // nodes known to hold it (self + peers)
	Pending    bool   `json:"pending"`    // Have < Target
}

// Publish runs the full send path: policy → object → event(s) → durable
// append (ACK) → synchronous projection/FTS. Reply is Publish with
// ReplyToMessageID set (atomic publish variant, rulings §2).
func (d *Daemon) Publish(req PublishRequest) (*PublishResult, error) {
	if err := d.writable(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lg == nil {
		return nil, errors.New("daemon is closed")
	}
	if req.Body == "" {
		return nil, errors.New("empty body")
	}
	if req.TextClass == "" {
		req.TextClass = object.ClassCanonical
	}
	if req.BodyMime == "" {
		req.BodyMime = "text/markdown"
	}
	if req.Actor == "" {
		req.Actor = "operator"
	}
	if req.DeclaredPriority < 0 || req.DeclaredPriority > config.DeclaredPriorityMax {
		return nil, fmt.Errorf("declared_priority %d out of range [0,%d]", req.DeclaredPriority, config.DeclaredPriorityMax)
	}
	if req.Durability == "" {
		req.Durability = config.DurabilityDefault
	}
	if !validDurability(req.Durability) {
		return nil, fmt.Errorf("invalid durability %q (want ephemeral|normal|important|pinned)", req.Durability)
	}

	// idempotency: caller-supplied correlation id already applied?
	if req.CorrelationID != "" {
		if res, err := d.resultByCorrelation(req.CorrelationID); err != nil {
			return nil, err
		} else if res != nil {
			return res, nil
		}
	}

	body := []byte(req.Body)
	decision, err := object.ApplyPolicy(req.TextClass, int64(len(body)), req.OperatorOverride,
		d.usage, d.loaded.Portable.PerMessageCanonicalBytes, d.now())
	if err != nil {
		return nil, err
	}

	// reply semantics: resolve target head + thread BEFORE building payload
	var replyToRevision, threadID string
	if req.ReplyToMessageID != "" {
		info, err := d.proj.MessageInfo(req.ReplyToMessageID)
		if err != nil {
			return nil, fmt.Errorf("reply target: %w", err)
		}
		if info.Retracted {
			return nil, fmt.Errorf("reply target %s is retracted", req.ReplyToMessageID)
		}
		replyToRevision = info.HeadRevisionID
		threadID = info.ThreadID
		if threadID == "" {
			threadID = req.ReplyToMessageID // thread emerges at the root message
		}
	}
	if req.ThreadID != "" {
		threadID = req.ThreadID
	}

	// 1. objects first (body, then every attachment — durability ordering)
	hash, err := d.store.Put(body)
	if err != nil {
		return nil, err
	}
	type attachOut struct {
		ObjectHash string `json:"object_hash"`
		ByteLen    int    `json:"byte_len"`
		Mime       string `json:"mime"`
		Filename   string `json:"filename,omitempty"`
	}
	var attachments []attachOut
	for i, a := range req.Attachments {
		// G6: a pre-staged attachment (streamed via stage-attachment) arrives as
		// an object hash, not inline bytes. Use the already-stored object.
		if a.ObjectHash != "" && len(a.Data) == 0 {
			data, err := d.store.Get(a.ObjectHash)
			if err != nil {
				return nil, fmt.Errorf("attachment %d references unstaged object %s: %w", i, a.ObjectHash, err)
			}
			if len(data) > config.DeriveMaxBytes {
				return nil, fmt.Errorf("attachment %d is %d bytes (cap %d)", i, len(data), config.DeriveMaxBytes)
			}
			mime := a.Mime
			if mime == "" {
				mime = sniffMime(data)
			}
			attachments = append(attachments, attachOut{
				ObjectHash: a.ObjectHash, ByteLen: len(data), Mime: mime, Filename: a.Filename,
			})
			continue
		}
		if len(a.Data) == 0 {
			return nil, fmt.Errorf("attachment %d is empty", i)
		}
		if len(a.Data) > config.DeriveMaxBytes {
			return nil, fmt.Errorf("attachment %d is %d bytes (cap %d)", i, len(a.Data), config.DeriveMaxBytes)
		}
		mime := a.Mime
		if mime == "" {
			mime = sniffMime(a.Data)
		}
		ahash, err := d.store.Put(a.Data)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachOut{
			ObjectHash: ahash, ByteLen: len(a.Data), Mime: mime, Filename: a.Filename,
		})
	}

	msgID := req.MessageID
	if msgID == "" {
		msgID = d.newUUID()
	}
	revID := req.RevisionID
	if revID == "" {
		revID = d.newUUID()
	}

	payload := map[string]any{
		"message_id": msgID, "revision_id": revID,
		"body_hash": hash, "body_len": len(body), "body_mime": req.BodyMime,
		"text_class": decision.Effective, "declared_priority": req.DeclaredPriority,
	}
	// R42: ephemeral bodies are NEVER inlined. An inline ≤64 KiB body
	// replicates as chain data to every full node, making the ephemeral
	// guarantee structurally unenforceable (searchable on peers, un-purgeable
	// at TTL). Ephemeral lives only as an object; the inline optimization
	// stays available for canonical / eager-searchable classes.
	if len(body) <= config.InlineBodyLimit && decision.Effective != object.ClassEphemeral {
		payload["body_bytes"] = req.Body
	}
	if threadID != "" {
		payload["thread_id"] = threadID
	}
	if req.ReplyToMessageID != "" {
		payload["reply_to_message_id"] = req.ReplyToMessageID
		payload["reply_to_revision_id"] = replyToRevision
	}
	if len(req.Recipients) > 0 {
		payload["recipients"] = req.Recipients
	}
	if req.SourceRef != nil {
		if req.SourceRef.Path == "" || req.SourceRef.ContentHash == "" || req.SourceRef.ImportedAt == "" {
			return nil, errors.New("source_ref requires path, content_hash, imported_at")
		}
		payload["source_ref"] = req.SourceRef
	}
	if len(req.RelatesTo) > 0 {
		payload["relates_to"] = req.RelatesTo
	}
	if len(attachments) > 0 {
		payload["attachments"] = attachments
		// N7: the durability class applies to this message's attachment blobs.
		payload["durability"] = req.Durability
	}
	if req.SenderSummary != "" {
		payload["sender_summary"] = req.SenderSummary
	}
	eventType := "message.publish"
	if req.ReplyToMessageID != "" {
		eventType = "message.reply"
	}

	// FIX-F1 ruling 2: resolve EVERY intra-mesh reference BEFORE anything is
	// appended — an event that cannot project must never be acked.
	type topicPlan struct {
		id     string
		create bool
		name   string
	}
	var topicPlans []topicPlan
	seenTopics := map[string]bool{}
	for _, ref := range req.Topics {
		id, name, err := d.resolveTopic(ref)
		if err != nil {
			if !req.AutoCreateTopics {
				return nil, fmt.Errorf("rejected before ack: %w (create it first, or use `cairn send --topic` which auto-creates)", err)
			}
			// auto-create: the ref is the new topic NAME (validated, R53)
			if err := validateTopicName(ref); err != nil {
				return nil, err
			}
			id, name = d.newUUID(), ref
			topicPlans = append(topicPlans, topicPlan{id: id, create: true, name: name})
		} else {
			topicPlans = append(topicPlans, topicPlan{id: id, name: name})
		}
		if seenTopics[id] {
			return nil, fmt.Errorf("rejected before ack: duplicate topic %q in request", ref)
		}
		seenTopics[id] = true
	}

	res := &PublishResult{
		MessageID:       msgID,
		RevisionID:      revID,
		BodyHash:        hash,
		TextClass:       decision.Effective,
		Downgraded:      decision.Downgraded,
		DowngradeReason: decision.Reason,
	}

	// FIX-A6: topic.create events append BEFORE message.publish. The old
	// order (publish first) meant a mid-sequence topic append failure
	// returned an error to a client whose message was already durable and
	// searchable — and a CLI/MCP retry (no correlation_id) then duplicated
	// the message. Creates-first shrinks that window to the link appends:
	// a failure before the publish leaves only idempotent topic creations
	// durable (a retry resolves them instead of re-creating), and a
	// re-published message mints a new ID so links never duplicate.
	//
	// RULING-NEEDED: FIX-F1 ruling 1 ("ALL events durable before the single
	// ack") does not say what to report when a LINK append fails after the
	// publish is durable. Conservative choice implemented: return the error
	// (never claim success for an incomplete request), accepting that a
	// client retry duplicates the message body under a new ID. Alternative:
	// success-with-warning naming the unlinked topics. Author to confirm.
	for _, tp := range topicPlans {
		if !tp.create {
			continue
		}
		cenv, crec, err := d.buildEvent("topic.create", "topic", tp.id,
			map[string]any{"topic_id": tp.id, "name": tp.name}, req)
		if err != nil {
			return nil, err
		}
		if err := d.lg.Append(crec, cenv); err != nil {
			return nil, fmt.Errorf("topic.create append failed before ack: %w", err)
		}
		res.EventIDs = append(res.EventIDs, cenv.EventID)
		d.applyProjection(cenv, crec)
	}

	env, rec, err := d.buildEvent(eventType, "message", msgID, payload, req)
	if err != nil {
		return nil, err
	}
	// R42 pre-ack guard (F1: reject before acknowledgement, never after): an
	// ephemeral publish must not carry inline body_bytes. Our own path never
	// builds one; this makes the invariant enforced, not merely observed.
	if err := ValidateNoInlineEphemeral(env); err != nil {
		return nil, err
	}
	if err := d.lg.Append(rec, env); err != nil {
		return nil, err
	}
	ackAt := time.Now()
	res.EventIDs = append(res.EventIDs, env.EventID)
	d.applyProjection(env, rec)

	// topic.link.add in order, same request — ALL durable before the single
	// ack (FIX-F1 ruling 1)
	for _, tp := range topicPlans {
		linkID := d.newUUID()
		lenv, lrec, err := d.buildEvent("topic.link.add", "link", linkID,
			map[string]any{"link_id": linkID, "message_id": msgID, "topic_id": tp.id}, req)
		if err != nil {
			return nil, err
		}
		if err := d.lg.Append(lrec, lenv); err != nil {
			return nil, fmt.Errorf("topic.link.add append failed before ack: %w", err)
		}
		res.EventIDs = append(res.EventIDs, lenv.EventID)
		d.applyProjection(lenv, lrec)
	}

	// === ACK POINT: the COMPLETE request is durable ===
	if d.tel != nil {
		d.tel.RecordLatency("ack_to_lexical_visible", time.Since(ackAt), d.now())
	}

	// N7: blob replication acknowledgement (spec §6.3). accepted_locally is
	// implicit (we returned durably). This is the ACK-TIME snapshot: the
	// sender holds every blob it just created (have=1), pending until peers
	// replicate it. It is DETERMINISTIC (target derives from the durability
	// class), so a regenerated receipt is byte-identical (M4 idempotency); the
	// LIVE replica state (2/2 etc.) is queried via `cairn sync status`, gates,
	// and deep doctor. The send NEVER blocks on replication.
	if len(attachments) > 0 {
		res.Replication = ackReplication(req.Durability, d.memberCount())
	}

	d.kickSync() // R29: push-on-append nudges the sweep toward connected peers
	return res, nil
}

// ackReplication is the deterministic ack-time replication snapshot: the
// origin holds the blob (have=1), pending if the class needs more nodes.
func ackReplication(class string, memberCount int) *ReplicationState {
	target := durabilityTarget(class, memberCount)
	return &ReplicationState{Durability: class, Target: target, Have: 1, Pending: target > 1}
}

// kickSync signals the anti-entropy sweep to run now (push-on-append, R29).
// Non-blocking: a pending kick already covers this append.
func (d *Daemon) kickSync() {
	if d.syncKick == nil {
		return
	}
	select {
	case d.syncKick <- struct{}{}:
	default:
	}
}

// validDurability reports whether c is a known N7 durability class.
func validDurability(c string) bool {
	switch c {
	case "ephemeral", "normal", "important", "pinned":
		return true
	}
	return false
}

// topicNamePattern is the schema's topic-name rule (single source of truth in
// internal/event, so every write/ingest boundary gates on the same pattern —
// R53). validateTopicName is the pre-ack rejection guard shared by every path
// that creates a topic name (message `--topic` auto-create, `topic-create`,
// `topic-ensure`/TopicEnsure); the projection re-enforces it at the sync-ingest
// boundary (terminal park), and renderMap escapes at render (defense in depth).
const topicNamePattern = event.TopicNamePattern

// validateTopicName rejects a non-conforming topic name BEFORE ack. Untrusted,
// peer-authorable data must never become durable if it violates the schema.
func validateTopicName(name string) error {
	if !event.ValidTopicName(name) {
		return fmt.Errorf("rejected before ack: %q is not a valid topic name (%s)", name, topicNamePattern)
	}
	return nil
}

// resolveTopic resolves a CLI/agent topic reference: exact topic_id first,
// then exact name. Caller holds d.mu.
func (d *Daemon) resolveTopic(ref string) (topicID, name string, err error) {
	if id, err := d.proj.TopicIDByName(ref); err != nil {
		return "", "", err
	} else if id != "" {
		return id, ref, nil
	}
	if name, err := d.proj.TopicNameByID(ref); err != nil {
		return "", "", err
	} else if name != "" {
		return ref, name, nil
	}
	return "", "", fmt.Errorf("unknown topic %q", ref)
}

// buildEvent constructs and signs a chained envelope for the active origin.
func (d *Daemon) buildEvent(eventType, objectType, objectID string, payload any, req PublishRequest) (*event.Envelope, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	env := &event.Envelope{
		SchemaVersion:         config.EventSchemaVersion,
		CairnID:               d.loaded.Portable.CairnID,
		EventType:             eventType,
		OriginDeviceID:        d.loaded.Device.DeviceID,
		OriginGeneration:      d.loaded.Device.OriginGeneration,
		OriginSequence:        d.lg.NextSeq(),
		PreviousOriginEventID: d.lg.LastEventID(),
		ActorPrincipalID:      req.Actor,
		ActorTaskID:           req.TaskID,
		ActorAgentInstanceID:  req.AgentInstanceID,
		CorrelationID:         req.CorrelationID,
		ObjectType:            objectType,
		ObjectID:              objectID,
		WallTime:              d.now().UTC().Format(config.WallTimeFormat),
		PayloadSchema:         config.PayloadSchemaID,
		Payload:               raw,
		SigningKeyID:          d.keyID,
	}
	rec, err := env.Sign(d.devPriv)
	if err != nil {
		return nil, nil, err
	}
	return env, rec, nil
}

// applyProjection: synchronous lexical enrichment (rulings §6). Projection
// failure after ack NEVER surfaces to the sender — it degrades (the
// projection is derived; startup replay heals it).
func (d *Daemon) applyProjection(env *event.Envelope, rec []byte) {
	if err := d.proj.Apply(env, rec); err != nil {
		fmt.Fprintf(d.warn, "WARNING: projection lag on %s (%v); reindex will heal\n", env.EventID, err)
	}
}

// resultByCorrelation regenerates a PublishResult from already-applied
// events (receipt idempotency, rulings §8).
func (d *Daemon) resultByCorrelation(correlationID string) (*PublishResult, error) {
	evs, err := d.proj.EventsByCorrelation(correlationID)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	res := &PublishResult{}
	for _, e := range evs {
		res.EventIDs = append(res.EventIDs, e.EventID)
		if e.EventType == "message.publish" || e.EventType == "message.reply" {
			var pl struct {
				MessageID   string           `json:"message_id"`
				RevisionID  string           `json:"revision_id"`
				BodyHash    string           `json:"body_hash"`
				TextClass   string           `json:"text_class"`
				Durability  string           `json:"durability"`
				Attachments []map[string]any `json:"attachments"`
			}
			if err := json.Unmarshal(e.Payload, &pl); err != nil {
				return nil, err
			}
			res.MessageID, res.RevisionID, res.BodyHash, res.TextClass = pl.MessageID, pl.RevisionID, pl.BodyHash, pl.TextClass
			// N7: reproduce the deterministic ack-time replication snapshot so
			// the regenerated receipt is byte-identical (M4 idempotency).
			if len(pl.Attachments) > 0 {
				class := pl.Durability
				if class == "" {
					class = config.DurabilityDefault
				}
				res.Replication = ackReplication(class, d.memberCount())
			}
		}
	}
	return res, nil
}

// SimpleEvent appends one non-publish mutation (retract, link, pin, signal).
func (d *Daemon) SimpleEvent(eventType, objectType, objectID string, payload map[string]any, req PublishRequest) (string, error) {
	if err := d.writable(); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lg == nil {
		return "", errors.New("daemon is closed")
	}
	env, rec, err := d.buildEvent(eventType, objectType, objectID, payload, req)
	if err != nil {
		return "", err
	}
	if err := d.lg.Append(rec, env); err != nil {
		return "", err
	}
	d.applyProjection(env, rec)
	d.kickSync()
	return env.EventID, nil
}

// ValidateNoInlineEphemeral enforces R42: a message.publish / message.reply
// event whose text_class is ephemeral MUST NOT carry inline body_bytes.
// Inlining replicates the body as chain data to every full node, making the
// ephemeral guarantee (never backfilled, purged at TTL) structurally
// unenforceable. Applied pre-ack on the creation path (F1). Non-publish events
// and canonical/eager inline bodies pass unchanged.
func ValidateNoInlineEphemeral(env *event.Envelope) error {
	switch env.EventType {
	case "message.publish", "message.reply":
		var pl struct {
			TextClass string `json:"text_class"`
			BodyBytes string `json:"body_bytes"`
		}
		if json.Unmarshal(env.Payload, &pl) == nil &&
			pl.TextClass == object.ClassEphemeral && pl.BodyBytes != "" {
			return fmt.Errorf("rejected before ack: ephemeral publish carries inline body_bytes (R42: ephemeral bodies live only as objects)")
		}
	}
	return nil
}

// ValidateNoInlineEphemeralRevisions enforces R42 on the revise/merge path
// (R46 invariant sweep): when the target message is ephemeral, NO revision in a
// message.revise_body event may carry inline body_bytes. The revise_body event
// is not self-describing (text_class lives on the message row, not the payload),
// so the caller passes the message's class. Applied pre-ack in appendRevision —
// the structural guard that makes the invariant enforced on this path, not
// merely observed, exactly as ValidateNoInlineEphemeral does for publish/reply.
func ValidateNoInlineEphemeralRevisions(env *event.Envelope, textClass string) error {
	if env.EventType != "message.revise_body" || textClass != object.ClassEphemeral {
		return nil
	}
	var pl struct {
		Revisions []struct {
			BodyBytes string `json:"body_bytes"`
		} `json:"revisions"`
	}
	if json.Unmarshal(env.Payload, &pl) == nil {
		for _, r := range pl.Revisions {
			if r.BodyBytes != "" {
				return fmt.Errorf("rejected before ack: ephemeral message revision carries inline body_bytes (R42/R46: ephemeral bodies live only as objects, on every write path)")
			}
		}
	}
	return nil
}

// Housekeep runs one ephemeral-TTL sweep (refs from the projection).
func (d *Daemon) Housekeep() ([]string, error) {
	if err := d.writable(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	refs, err := d.proj.ObjectRefs()
	if err != nil {
		return nil, err
	}
	return d.store.HousekeepEphemeral(refs, d.now())
}

// Projection exposes read-only queries (search/peek) — reads need no mutex;
// SQLite serializes on the single connection.
func (d *Daemon) Projection() *projection.Projection { return d.proj }
func (d *Daemon) Store() *object.Store               { return d.store }
func (d *Daemon) Dir() string                        { return d.dir }

func (d *Daemon) newUUID() string {
	u, err := uuid.NewV7()
	if err != nil {
		// NewV7 fails only if the entropy source is broken; fall back to v4
		return uuid.NewString()
	}
	return u.String()
}
