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
	"os"
	"path/filepath"
	"regexp"
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
	"github.com/ggoosen/cairn/internal/object"
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
}

// Daemon is the single writer for one cairn.
type Daemon struct {
	fs      fsx.FS
	dir     string
	loaded  *identity.Loaded
	devPriv ed25519.PrivateKey
	keyID   string

	mu       sync.Mutex // serializes ALL mutations (single writer)
	embedder embed.Embedder
	trust    *identity.Trust
	lg       *cairnlog.Log
	proj     *projection.Projection
	store    *object.Store
	usage    *object.UsageTracker
	verifier *identity.ChainVerifier
	now      func() time.Time

	lockFile *os.File
	warn     io.Writer
	tel      *telemetry.Store

	readOnly bool   // portable-only restore: reads allowed, writes refused (R9)
	sockDir  string // where daemon.sock.path is registered

	sessions *sessions // N2 capability handles (guarded by mu via dispatch)
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

	if opts.Embedder == nil {
		opts.Embedder = embed.Detect(opts.Dir)
	}
	d := &Daemon{
		fs:       opts.FS,
		dir:      opts.Dir,
		loaded:   loaded,
		devPriv:  devPriv,
		store:    object.NewStore(opts.FS, opts.Dir),
		verifier: identity.NewChainVerifier(),
		now:      opts.Now,
		lockFile: lockFile,
		warn:     opts.Warn,
		embedder: opts.Embedder,
		readOnly: readOnly,
		sockDir:  lockDir,
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
		return nil
	}

	active := cairnlog.Origin{DeviceID: d.loaded.Device.DeviceID, Generation: d.loaded.Device.OriginGeneration}
	for _, o := range origins {
		if o == active {
			continue
		}
		if _, err := cairnlog.Walk(d.fs, d.dir, o, trust.Verifier(), apply); err != nil {
			return fmt.Errorf("replaying origin %s/%d: %w", o.DeviceID, o.Generation, err)
		}
	}
	lg, report, err := cairnlog.Open(d.fs, d.dir, active, trust.Verifier(), apply)
	if err != nil {
		return err
	}
	d.lg = lg
	if _, err := identity.ReconcileSeqState(d.fs, d.loaded.DeviceDir, active.DeviceID, active.Generation, report.NextSeq, d.warn); err != nil {
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

	// 1. object first
	hash, err := d.store.Put(body)
	if err != nil {
		return nil, err
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
	if len(body) <= config.InlineBodyLimit {
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
			// auto-create: the ref is the new topic NAME (validated)
			if !topicNameRe.MatchString(ref) {
				return nil, fmt.Errorf("rejected before ack: %q is not a valid topic name (%s)", ref, topicNamePattern)
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

	env, rec, err := d.buildEvent(eventType, "message", msgID, payload, req)
	if err != nil {
		return nil, err
	}
	if err := d.lg.Append(rec, env); err != nil {
		return nil, err
	}
	ackAt := time.Now()
	res.EventIDs = append(res.EventIDs, env.EventID)
	d.applyProjection(env, rec)

	// topic.create then topic.link.add, in order, same request — ALL durable
	// before the single ack (FIX-F1 ruling 1)
	for _, tp := range topicPlans {
		if tp.create {
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
	return res, nil
}

// topicNamePattern is the schema's topic-name rule.
const topicNamePattern = "^[a-z0-9][a-z0-9/_-]*$"

var topicNameRe = regexp.MustCompile(topicNamePattern)

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
				MessageID  string `json:"message_id"`
				RevisionID string `json:"revision_id"`
				BodyHash   string `json:"body_hash"`
				TextClass  string `json:"text_class"`
			}
			if err := json.Unmarshal(e.Payload, &pl); err != nil {
				return nil, err
			}
			res.MessageID, res.RevisionID, res.BodyHash, res.TextClass = pl.MessageID, pl.RevisionID, pl.BodyHash, pl.TextClass
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
	return env.EventID, nil
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
