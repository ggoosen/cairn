package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// MigrateOptions parameterizes the P0 offline migrate ceremony (rulings §4).
type MigrateOptions struct {
	Dir              string // portable dir
	DisplayName      string
	FS               fsx.FS
	Now              func() time.Time
	Out              io.Writer
	AllowUnencrypted bool          // FIX-H2/R46: operator override for an unencrypted volume
	Checker          VolumeChecker // FIX-H2/R46: injectable for tests; defaults to system
}

// MigrateResult reports the outcome.
type MigrateResult struct {
	OldDeviceID string
	NewDeviceID string
	AddEventID  string
	RevokeEvent string
}

// migratePending is the staging file: the NEW identity is persisted BEFORE
// any event is appended, so a crash at any point is resumable (crash rows
// 14/15: between add and revoke both devices are valid and a rerun completes
// the revoke).
type migratePending struct {
	OldDeviceID string     `json:"old_device_id"`
	NewDeviceID string     `json:"new_device_id"`
	Cert        DeviceCert `json:"cert"`
}

// Migrate runs the offline ceremony against the local log:
//  1. generate + STAGE the new device identity (key + root-signed cert)
//  2. append device.add {cert} to the old origin (signed by the old device)
//  3. append device.revoke {old} to the old origin (signed by the ROOT key)
//  4. swap the device-local identity to the new device; old origin read-only
//
// Rerunning after a crash at any step completes the remaining steps.
func Migrate(opts MigrateOptions) (*MigrateResult, error) {
	if opts.FS == nil {
		opts.FS = fsx.OS{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}

	loaded, err := Load(opts.Dir)
	if err != nil {
		return nil, err
	}
	deviceDir := loaded.DeviceDir
	oldDeviceID := loaded.Device.DeviceID
	oldGen := loaded.Device.OriginGeneration

	// completion guard: a crash after the config swap but before staging
	// cleanup leaves pending.json pointing at the ALREADY-ACTIVE device; the
	// only remaining work is bookkeeping. Re-entering the ceremony here would
	// catastrophically migrate (and revoke) the new device.
	pendingDirEarly := filepath.Join(deviceDir, "migrate-pending")
	if blob, err := os.ReadFile(filepath.Join(pendingDirEarly, "pending.json")); err == nil {
		var pend migratePending
		if json.Unmarshal(blob, &pend) == nil && pend.NewDeviceID == loaded.Device.DeviceID {
			if _, err := ReconcileSeqState(opts.FS, deviceDir, pend.NewDeviceID, config.FirstGeneration, config.FirstSequence, opts.Out); err != nil {
				return nil, err
			}
			if err := os.RemoveAll(pendingDirEarly); err != nil {
				return nil, err
			}
			fmt.Fprintf(opts.Out, "migration to %s already complete; cleaned up staging\n", pend.NewDeviceID)
			return &MigrateResult{OldDeviceID: pend.OldDeviceID, NewDeviceID: pend.NewDeviceID}, nil
		}
	}

	// FIX-H2/R46: gate encryption BEFORE staging the new device key — the same
	// P0 invariant enroll/join enforce (G3). Placed after the completion guard
	// (which writes no key material, only bookkeeping) so a finished migration
	// can always clean up. Refuses on an unencrypted/unknown volume unless the
	// operator passes the override, which warns loudly.
	if err := GateEncryption(deviceDir, opts.Checker, opts.AllowUnencrypted, opts.Out); err != nil {
		return nil, err
	}

	rootPriv, err := LoadKey(filepath.Join(deviceDir, config.RootKeyName))
	if err != nil {
		return nil, fmt.Errorf("migrate needs the root key (device-local in P0): %w", err)
	}
	rootPub := rootPriv.Public().(ed25519.PublicKey)
	oldDevPriv, err := LoadKey(filepath.Join(deviceDir, config.DeviceKeyName))
	if err != nil {
		return nil, err
	}
	now := opts.Now().UTC().Format(config.WallTimeFormat)

	// --- step 1: stage (or resume) the new identity --------------------------
	pendingDir := filepath.Join(deviceDir, "migrate-pending")
	pendingFile := filepath.Join(pendingDir, "pending.json")
	var pending migratePending
	if blob, err := os.ReadFile(pendingFile); err == nil {
		if err := json.Unmarshal(blob, &pending); err != nil {
			return nil, fmt.Errorf("corrupt migrate staging (%s): %w — resolve manually before rerunning", pendingFile, err)
		}
		fmt.Fprintf(opts.Out, "resuming interrupted migration to device %s\n", pending.NewDeviceID)
	} else {
		newUUID, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		newPub, newPriv, err := GenerateKey()
		if err != nil {
			return nil, err
		}
		cert := DeviceCert{
			DeviceID:    newUUID.String(),
			Pubkey:      base64.StdEncoding.EncodeToString(newPub),
			Generation:  config.FirstGeneration,
			IssuedAt:    now,
			DisplayName: opts.DisplayName,
		}
		if err := cert.SignCert(rootPriv); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(pendingDir, config.DirPerm); err != nil {
			return nil, err
		}
		if err := SaveKey(filepath.Join(pendingDir, config.DeviceKeyName), newPriv); err != nil {
			return nil, err
		}
		pending = migratePending{OldDeviceID: oldDeviceID, NewDeviceID: newUUID.String(), Cert: cert}
		blob, err := json.Marshal(pending)
		if err != nil {
			return nil, err
		}
		if err := fsx.WriteFileAtomic(fsx.OS{}, pendingFile, blob, config.KeyFilePerm); err != nil {
			return nil, err
		}
	}

	// --- steps 2+3: append add + revoke to the OLD origin --------------------
	// FIX-F2: the current device's cert may live in an EARLIER origin's log
	// (repeated migrations), so verification uses mesh-level trust, never a
	// fresh per-origin chain.
	trust, err := MeshTrust(opts.FS, opts.Dir)
	if err != nil {
		return nil, err
	}
	lg, _, err := cairnlog.Open(opts.FS, opts.Dir, cairnlog.Origin{DeviceID: oldDeviceID, Generation: oldGen},
		trust.Verifier(), nil)
	if err != nil {
		return nil, fmt.Errorf("opening old origin: %w", err)
	}
	defer lg.Close()

	// resume detection: scan applied events for our staged cert / revoke
	addDone, revokeDone := "", ""
	{
		_, err := cairnlog.Walk(opts.FS, opts.Dir, cairnlog.Origin{DeviceID: oldDeviceID, Generation: oldGen}, trust.Verifier(),
			func(env *event.Envelope, _ []byte) error {
				switch env.EventType {
				case "device.add":
					var pl struct {
						Cert DeviceCert `json:"cert"`
					}
					if json.Unmarshal(env.Payload, &pl) == nil && pl.Cert.DeviceID == pending.NewDeviceID {
						addDone = env.EventID
					}
				case "device.revoke":
					var pl struct {
						DeviceID string `json:"device_id"`
					}
					if json.Unmarshal(env.Payload, &pl) == nil && pl.DeviceID == oldDeviceID {
						revokeDone = env.EventID
					}
				}
				return nil
			})
		if err != nil {
			return nil, err
		}
	}

	res := &MigrateResult{OldDeviceID: oldDeviceID, NewDeviceID: pending.NewDeviceID}

	appendEvent := func(eventType, objectID string, payload any, priv ed25519.PrivateKey, keyPub ed25519.PublicKey) (string, error) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		env := &event.Envelope{
			SchemaVersion:         config.EventSchemaVersion,
			CairnID:               loaded.Portable.CairnID,
			EventType:             eventType,
			OriginDeviceID:        oldDeviceID,
			OriginGeneration:      oldGen,
			OriginSequence:        lg.NextSeq(),
			PreviousOriginEventID: lg.LastEventID(),
			ActorPrincipalID:      "operator",
			ObjectType:            "device",
			ObjectID:              objectID,
			WallTime:              opts.Now().UTC().Format(config.WallTimeFormat),
			PayloadSchema:         config.PayloadSchemaID,
			Payload:               raw,
			SigningKeyID:          event.KeyID(keyPub),
		}
		rec, err := env.Sign(priv)
		if err != nil {
			return "", err
		}
		if err := lg.Append(rec, env); err != nil {
			return "", err
		}
		return env.EventID, nil
	}

	if addDone == "" {
		oldDevPub := oldDevPriv.Public().(ed25519.PublicKey)
		addDone, err = appendEvent("device.add", pending.NewDeviceID,
			map[string]any{"cert": pending.Cert}, oldDevPriv, oldDevPub)
		if err != nil {
			return nil, fmt.Errorf("appending device.add: %w", err)
		}
	}
	res.AddEventID = addDone

	if revokeDone == "" {
		revokeDone, err = appendEvent("device.revoke", oldDeviceID,
			map[string]any{"device_id": oldDeviceID, "reason": "migrated to " + pending.NewDeviceID},
			rootPriv, rootPub)
		if err != nil {
			return nil, fmt.Errorf("appending device.revoke (rerun `cairn migrate` to complete): %w", err)
		}
	}
	res.RevokeEvent = revokeDone

	// --- step 4: swap device-local identity. Order matters for crash
	// resumability: keys/cert/seq-state first, the config write is the
	// COMMIT MARKER (the completion guard above handles a crash between the
	// config write and staging cleanup).
	archived := filepath.Join(deviceDir, config.DeviceKeyName+".revoked-"+oldDeviceID)
	if _, err := os.Stat(filepath.Join(pendingDir, config.DeviceKeyName)); err == nil {
		os.Remove(archived)
		if err := os.Rename(filepath.Join(deviceDir, config.DeviceKeyName), archived); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := os.Rename(filepath.Join(pendingDir, config.DeviceKeyName), filepath.Join(deviceDir, config.DeviceKeyName)); err != nil {
			return nil, err
		}
	}
	certBlob, err := json.MarshalIndent(pending.Cert, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(deviceDir, config.DeviceCertName), certBlob, config.FilePerm); err != nil {
		return nil, err
	}
	if _, err := ReconcileSeqState(opts.FS, deviceDir, pending.NewDeviceID, config.FirstGeneration, config.FirstSequence, opts.Out); err != nil {
		return nil, err
	}
	devCfg := loaded.Device
	devCfg.DeviceID = pending.NewDeviceID
	devCfg.OriginGeneration = config.FirstGeneration
	if err := devCfg.SaveDevice(deviceDir); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(pendingDir); err != nil {
		return nil, err
	}

	fmt.Fprintf(opts.Out, "migrated: old device %s revoked (origin read-only), new device %s active\n",
		oldDeviceID, pending.NewDeviceID)
	return res, nil
}
