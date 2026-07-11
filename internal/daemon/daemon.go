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
	lg       *cairnlog.Log
	proj     *projection.Projection
	store    *object.Store
	usage    *object.UsageTracker
	verifier *identity.ChainVerifier
	now      func() time.Time

	lockFile *os.File
	warn     io.Writer
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

	loaded, err := identity.Load(opts.Dir)
	if err != nil {
		return nil, err
	}
	if err := loaded.StartupCheck(opts.Checker, opts.Warn); err != nil {
		return nil, err
	}
	devPriv, err := identity.LoadKey(filepath.Join(loaded.DeviceDir, config.DeviceKeyName))
	if err != nil {
		return nil, fmt.Errorf("loading device key: %w", err)
	}

	// single-writer lock (device-local; flock released on process exit)
	lockPath := filepath.Join(loaded.DeviceDir, config.DaemonLockName)
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
		keyID:    event.KeyID(devPriv.Public().(ed25519.PublicKey)),
		store:    object.NewStore(opts.FS, opts.Dir),
		verifier: identity.NewChainVerifier(),
		now:      opts.Now,
		lockFile: lockFile,
		warn:     opts.Warn,
		embedder: opts.Embedder,
	}

	proj, err := projection.Open(opts.DBPath, projection.StoreBodyFetch(d.store))
	if err != nil {
		d.releaseLock()
		return nil, err
	}
	d.proj = proj

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
	return d, nil
}

// recover opens/repairs the ACTIVE origin (with projection catch-up wired
// into the scan) and replays every other origin read-only. Origins whose
// keys are admitted by another origin's chain are retried until fixpoint.
func (d *Daemon) recover() error {
	active := cairnlog.Origin{DeviceID: d.loaded.Device.DeviceID, Generation: d.loaded.Device.OriginGeneration}

	origins, err := cairnlog.Origins(d.fs, d.dir)
	if err != nil {
		return err
	}
	pending := map[cairnlog.Origin]bool{}
	for _, o := range origins {
		if o != active {
			pending[o] = true
		}
	}

	apply := func(env *event.Envelope, rec []byte) error { return d.proj.Apply(env, rec) }

	// active origin first when it holds genesis; otherwise fixpoint below
	openActive := func() error {
		lg, report, err := cairnlog.Open(d.fs, d.dir, active, d.verifier.Verify, apply)
		if err != nil {
			return err
		}
		d.lg = lg
		if _, err := identity.ReconcileSeqState(d.fs, d.loaded.DeviceDir, active.DeviceID, active.Generation, report.NextSeq, d.warn); err != nil {
			return err
		}
		return nil
	}

	tryOrigin := func(o cairnlog.Origin) error {
		_, err := cairnlog.Walk(d.fs, d.dir, o, d.verifier.Verify, apply)
		return err
	}

	// Fixpoint over {active + pending}: key-dependency order is unknowable
	// up front (migrate creates origins admitted by earlier ones).
	activeDone := false
	remaining := len(pending) + 1
	for remaining > 0 {
		progressed := false
		if !activeDone {
			if err := openActive(); err == nil {
				activeDone, progressed = true, true
				remaining--
			} else if !isKeyOrderError(err) {
				return err
			}
		}
		for o := range pending {
			if err := tryOrigin(o); err == nil {
				delete(pending, o)
				progressed = true
				remaining--
			} else if !isKeyOrderError(err) {
				return fmt.Errorf("replaying origin %s/%d: %w", o.DeviceID, o.Generation, err)
			}
		}
		if !progressed {
			return fmt.Errorf("could not establish key chain for %d origin(s) (unresolved dependency or corruption)", remaining)
		}
	}

	// A revoked device must never write (row 15: old origin read-only).
	if d.verifier.Revoked(active.DeviceID) {
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
	TopicIDs         []string `json:"topic_ids,omitempty"` // initial links: separate events, same request
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
	eventType := "message.publish"
	if req.ReplyToMessageID != "" {
		eventType = "message.reply"
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
	// === ACK POINT: the publish event is durable ===
	res.EventIDs = append(res.EventIDs, env.EventID)
	d.applyProjection(env, rec)

	// initial topic links: separate events sharing the request (rulings §2)
	for _, topicID := range req.TopicIDs {
		linkPayload := map[string]any{"link_id": d.newUUID(), "message_id": msgID, "topic_id": topicID}
		lenv, lrec, err := d.buildEvent("topic.link.add", "link", linkPayload["link_id"].(string), linkPayload, req)
		if err != nil {
			return res, fmt.Errorf("publish acked but link event failed: %w", err)
		}
		if err := d.lg.Append(lrec, lenv); err != nil {
			return res, fmt.Errorf("publish acked but link append failed: %w", err)
		}
		res.EventIDs = append(res.EventIDs, lenv.EventID)
		d.applyProjection(lenv, lrec)
	}
	return res, nil
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
