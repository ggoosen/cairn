package daemon

// N8 fork repair (spec §6.4). An OFFLINE operator ceremony (daemon stopped,
// like migrate/revoke): the operator chooses the canonical branch; the LOSING
// branch's useful (message) events are reissued under the operator's active
// origin — a recovery origin — each carrying recovered_from_event_id +
// fork_resolution_id; a root-signed device.fork.resolve records the decision;
// the fork is marked resolved. The losing branch is NEVER deleted (its frames
// stay in quarantine forever). Revoking the cloned certificate is the
// documented follow-up (`cairn device revoke`), because a self-clone requires
// `cairn migrate` first — see DOGFOOD §14. Durable-log internals are untouched:
// every append uses the public log API exactly as migrate/revoke do.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
)

// ResolveForkOptions parameterizes the offline repair ceremony.
type ResolveForkOptions struct {
	Dir           string    // portable dir
	OriginDevice  string    // forked origin device id
	Canonical     string    // "local" | "remote"
	RootKeyPath   string    // restored root key (verifies + signs device.fork.resolve)
	Now           func() time.Time
	Out           io.Writer
	newID         func() string // test hook for deterministic ids
}

// ResolveFork executes the repair. Returns the fork_resolution_id.
func ResolveFork(opts ResolveForkOptions) (string, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.newID == nil {
		opts.newID = func() string { return uuid.NewString() }
	}
	if opts.Canonical != "local" && opts.Canonical != "remote" {
		return "", fmt.Errorf("canonical must be local|remote, got %q", opts.Canonical)
	}
	loaded, err := identity.Load(opts.Dir)
	if err != nil {
		return "", err
	}
	// offline: hold the daemon write lock (fail if a daemon is running)
	lockPath := filepath.Join(loaded.DeviceDir, config.DaemonLockName)
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lf.Close()
	if err := flockExclusive(lf); err != nil {
		return "", fmt.Errorf("stop the daemon before resolving a fork: %w", err)
	}
	defer flockUnlock(lf)

	origin := cairnlog.Origin{DeviceID: opts.OriginDevice}
	forks := loadForks(opts.Dir)
	// generation is not on the CLI; pick the (single) fork for this device
	var f *ForkRecord
	for o, fr := range forks {
		if o.DeviceID == opts.OriginDevice && !fr.Resolved {
			f, origin = fr, o
			break
		}
	}
	if f == nil {
		return "", fmt.Errorf("no unresolved fork for origin %s", opts.OriginDevice)
	}

	rootPriv, err := identity.LoadKey(opts.RootKeyPath)
	if err != nil {
		return "", fmt.Errorf("root key (restore it from offline storage first): %w", err)
	}
	trust, err := identity.MeshTrust(fsx.OS{}, opts.Dir)
	if err != nil {
		return "", err
	}
	if !rootPriv.Public().(ed25519.PublicKey).Equal(trust.RootPub) {
		return "", errors.New("restored key is NOT this mesh's root key")
	}
	devPriv, err := identity.LoadKey(filepath.Join(loaded.DeviceDir, config.DeviceKeyName))
	if err != nil {
		return "", err
	}
	devKeyID := event.KeyID(devPriv.Public().(ed25519.PublicKey))

	active := cairnlog.Origin{DeviceID: loaded.Device.DeviceID, Generation: loaded.Device.OriginGeneration}
	lg, _, err := cairnlog.Open(fsx.OS{}, opts.Dir, active, trust.Verifier(), nil)
	if err != nil {
		return "", err
	}
	defer lg.Close()
	store := object.NewStore(fsx.OS{}, opts.Dir)

	resolutionID := opts.newID()
	canonicalHead, losingHead := f.LocalHead, f.RemoteHead
	if opts.Canonical == "remote" {
		canonicalHead, losingHead = f.RemoteHead, f.LocalHead
	}

	// 1. root-signed device.fork.resolve (the decision, in the operator's log)
	frPayload := map[string]any{
		"forked_origin_device":     f.OriginDevice,
		"forked_origin_generation": f.OriginGen,
		"divergence_seq":           f.DivergenceSeq,
		"canonical":                opts.Canonical,
		"canonical_head_event_id":  canonicalHead,
		"losing_head_event_id":     losingHead,
		"fork_resolution_id":       resolutionID,
	}
	rootPub := rootPriv.Public().(ed25519.PublicKey)
	if err := appendSigned(lg, &event.Envelope{
		SchemaVersion:    config.EventSchemaVersion,
		CairnID:          loaded.Portable.CairnID,
		EventType:        "device.fork.resolve",
		OriginDeviceID:   active.DeviceID,
		OriginGeneration: active.Generation,
		ActorPrincipalID: "operator",
		ObjectType:       "device",
		ObjectID:         f.OriginDevice,
		WallTime:         opts.Now().UTC().Format(config.WallTimeFormat),
		PayloadSchema:    config.PayloadSchemaID,
		SigningKeyID:     event.KeyID(rootPub),
	}, frPayload, rootPriv); err != nil {
		return "", fmt.Errorf("append device.fork.resolve: %w", err)
	}

	// 2. reissue the LOSING branch's message events under this recovery origin
	losing := losingBranchEvents(opts.Dir, origin, f, opts.Canonical)
	reissued := 0
	for _, le := range losing {
		body, textClass, ok := reissueBody(store, le.env)
		if !ok {
			fmt.Fprintf(opts.Out, "  note: losing event %s (%s) has no recoverable body — skipped (event preserved in quarantine)\n", short(le.env.EventID), le.env.EventType)
			continue
		}
		hash, err := store.Put(body)
		if err != nil {
			return "", err
		}
		msgID, revID := opts.newID(), opts.newID()
		payload := map[string]any{
			"message_id": msgID, "revision_id": revID,
			"body_hash": hash, "body_len": len(body), "body_mime": "text/markdown",
			"text_class":              textClass,
			"declared_priority":       0,
			"recovered_from_event_id": le.env.EventID,
			"fork_resolution_id":      resolutionID,
		}
		if len(body) <= config.InlineBodyLimit {
			payload["body_bytes"] = string(body)
		}
		if err := appendSigned(lg, &event.Envelope{
			SchemaVersion:    config.EventSchemaVersion,
			CairnID:          loaded.Portable.CairnID,
			EventType:        "message.publish",
			OriginDeviceID:   active.DeviceID,
			OriginGeneration: active.Generation,
			ActorPrincipalID: "operator",
			ObjectType:       "message",
			ObjectID:         msgID,
			WallTime:         opts.Now().UTC().Format(config.WallTimeFormat),
			PayloadSchema:    config.PayloadSchemaID,
			SigningKeyID:     devKeyID,
		}, payload, devPriv); err != nil {
			return "", fmt.Errorf("reissue losing event: %w", err)
		}
		reissued++
	}

	// RULING-NEEDED: §6.4's full ceremony ALSO re-enrols the physical device
	// under a new identity and revokes the cloned certificate. We do the branch
	// decision + losing-branch reissue + root-signed device.fork.resolve here;
	// the cloned-cert revocation is a documented follow-up (`cairn device
	// revoke`, or `cairn migrate` first for a self-clone) rather than automated
	// inside resolve, because RevokeDevice refuses a self-revoke by design.
	// Flagged for author confirmation (DOGFOOD §14 documents the follow-up).

	// 3. mark the fork resolved (both branches preserved: local in the log,
	// remote in quarantine — never deleted)
	f.Resolved = true
	f.Canonical = opts.Canonical
	f.ResolutionID = resolutionID
	if err := saveForkRecord(opts.Dir, f); err != nil {
		return "", err
	}
	fmt.Fprintf(opts.Out, "fork on origin %s/%d RESOLVED — canonical: %s; %d losing-branch event(s) reissued under the recovery origin (recovered_from_event_id + fork_resolution_id %s).\n",
		f.OriginDevice, f.OriginGen, opts.Canonical, reissued, resolutionID)
	fmt.Fprintf(opts.Out, "The losing branch is preserved in .cairn/quarantine (never deleted).\n")
	fmt.Fprintf(opts.Out, "NEXT: revoke the cloned certificate — `cairn device revoke %s --root-key <path>` (for a self-clone, `cairn migrate` first). Then REMOVE the restored root key and restart the daemon.\n", f.OriginDevice)
	return resolutionID, nil
}

// flockExclusive / flockUnlock guard the offline ceremony against a running
// daemon (same lock the daemon holds).
func flockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func flockUnlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// appendSigned signs an envelope's payload and appends it (chain position set
// from the log). Mirrors the daemon's write path but standalone (offline).
func appendSigned(lg *cairnlog.Log, env *event.Envelope, payload any, priv ed25519.PrivateKey) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env.Payload = raw
	env.OriginSequence = lg.NextSeq()
	env.PreviousOriginEventID = lg.LastEventID()
	rec, err := env.Sign(priv)
	if err != nil {
		return err
	}
	return lg.Append(rec, env)
}

// reissuedEvent pairs a losing-branch envelope with its source.
type reissuedEvent struct {
	env *event.Envelope
	rec []byte
}

// losingBranchEvents returns the losing branch's message events. For a remote
// losing branch they come from quarantine; for a local losing branch they come
// from the origin's log (read-only). Only message.publish/reply are reissued.
func losingBranchEvents(portableDir string, o cairnlog.Origin, f *ForkRecord, canonical string) []reissuedEvent {
	var out []reissuedEvent
	if canonical == "local" {
		// losing = remote = quarantined frames
		dir := quarantineDir(portableDir, o)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			rec, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			var env event.Envelope
			if json.Unmarshal(rec, &env) != nil {
				continue
			}
			if env.EventType == "message.publish" || env.EventType == "message.reply" {
				out = append(out, reissuedEvent{env: &env, rec: rec})
			}
		}
		return out
	}
	// losing = local = our own branch from divergence forward (read-only walk)
	trust, err := identity.MeshTrust(fsx.OS{}, portableDir)
	if err != nil {
		return nil
	}
	_, _ = cairnlog.Walk(fsx.OS{}, portableDir, o, trust.Verifier(), func(env *event.Envelope, rec []byte) error {
		if env.OriginSequence >= f.DivergenceSeq && (env.EventType == "message.publish" || env.EventType == "message.reply") {
			e := *env
			out = append(out, reissuedEvent{env: &e, rec: append([]byte(nil), rec...)})
		}
		return nil
	})
	return out
}

// reissueBody extracts a losing message's body + text class. Inline bodies
// come straight from the payload; otherwise the body object must be present
// locally. ok=false means the body is unrecoverable (skip, preserve original).
func reissueBody(store *object.Store, env *event.Envelope) (body []byte, textClass string, ok bool) {
	var pl struct {
		BodyBytes string `json:"body_bytes"`
		BodyHash  string `json:"body_hash"`
		TextClass string `json:"text_class"`
	}
	if json.Unmarshal(env.Payload, &pl) != nil {
		return nil, "", false
	}
	tc := pl.TextClass
	if tc == "" {
		tc = object.ClassCanonical
	}
	if pl.BodyBytes != "" {
		return []byte(pl.BodyBytes), tc, true
	}
	if pl.BodyHash != "" && store.Exists(pl.BodyHash) {
		if data, err := store.Get(pl.BodyHash); err == nil {
			return data, tc, true
		}
	}
	return nil, "", false
}
