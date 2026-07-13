package identity

import (
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

// SeqState is the sequence-state CACHE (device-local). The verified log is
// authoritative; recovery never trusts a cached value above the log.
type SeqState struct {
	OriginDeviceID   string `json:"origin_device_id"`
	OriginGeneration int    `json:"origin_generation"`
	NextSequence     int64  `json:"next_sequence"`
}

// InitOptions parameterizes `cairn init`. Checker/Now/Out are injectable for
// the fault tests.
type InitOptions struct {
	Dir              string // resolved portable dir
	DisplayName      string // optional device display name
	AllowUnencrypted bool   // operator override; persisted device-local
	SyncListen       string // R44: sync listener (default "auto"); "off" or a tailnet host:port
	Checker          VolumeChecker
	Now              func() time.Time
	Out              io.Writer
}

// InitResult reports what was created.
type InitResult struct {
	CairnID        string
	DeviceID       string
	DeviceDir      string
	SegmentPath    string
	GenesisEventID string
	RootKeyID      string
	DeviceKeyID    string
}

// Initialize performs the genesis ceremony (BUILD-PLAN M0, rulings §2/§4):
// encryption check (fail closed) → keygen (root + device) → root-signed
// device cert → device-signed cairn.genesis → device-local state → portable
// layout with the genesis appended to the first segment. cairn.toml is
// written LAST as the initialized marker.
func Initialize(opts InitOptions) (*InitResult, error) {
	if opts.Checker == nil {
		opts.Checker = SystemVolumeChecker{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	dir := opts.Dir
	if dir == "" {
		return nil, errors.New("portable directory not resolved")
	}

	if _, err := os.Stat(filepath.Join(dir, config.PortableConfigName)); err == nil {
		return nil, fmt.Errorf("%s is already an initialized cairn directory", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, config.EventsDirName)); err == nil {
		return nil, fmt.Errorf("%s contains cairn data without %s — looks like a restored copy; refusing to re-initialize over it", dir, config.PortableConfigName)
	}
	if err := os.MkdirAll(dir, config.DirPerm); err != nil {
		return nil, err
	}

	if err := checkEncryption(dir, opts.Checker, opts.AllowUnencrypted, opts.Out); err != nil {
		return nil, err
	}

	now := opts.Now().UTC().Format(config.WallTimeFormat)
	cairnUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	deviceUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	cairnID, deviceID := cairnUUID.String(), deviceUUID.String()

	rootPub, rootPriv, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	devicePub, devicePriv, err := GenerateKey()
	if err != nil {
		return nil, err
	}

	cert := DeviceCert{
		DeviceID:    deviceID,
		Pubkey:      base64.StdEncoding.EncodeToString(devicePub),
		Generation:  config.FirstGeneration,
		IssuedAt:    now,
		DisplayName: opts.DisplayName,
	}
	if err := cert.SignCert(rootPriv); err != nil {
		return nil, err
	}

	env, record, err := BuildGenesis(cairnID, rootPub, cert, devicePriv, now)
	if err != nil {
		return nil, err
	}
	// Self-check before anything touches disk: the genesis we persist must verify.
	if _, _, err := VerifyGenesis(record); err != nil {
		return nil, fmt.Errorf("genesis failed self-verification: %w", err)
	}

	// Device-local state (keys NEVER under the portable dir).
	deviceDir, err := config.DeviceStateDir(cairnID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(deviceDir, config.DirPerm); err != nil {
		return nil, err
	}
	if err := SaveKey(filepath.Join(deviceDir, config.DeviceKeyName), devicePriv); err != nil {
		return nil, err
	}
	// RULING-NEEDED: root-key storage. P0 keeps the root key device-local
	// (0600) so device.add / migrate can root-sign offline; spec §3.1 wants
	// it in separate operator recovery storage. Conservative interpretation
	// + explicit backup instruction; see PROGRESS.md "Author rulings needed".
	if err := SaveKey(filepath.Join(deviceDir, config.RootKeyName), rootPriv); err != nil {
		return nil, err
	}
	certBlob, err := json.MarshalIndent(cert, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(deviceDir, config.DeviceCertName), certBlob, config.FilePerm); err != nil {
		return nil, err
	}
	syncListen := opts.SyncListen
	if syncListen == "" {
		syncListen = config.SyncListenAuto // R44: default is auto-detect the tailnet
	}
	devCfg := &config.DeviceConfig{
		ConfigVersion:    config.DeviceConfigVersion,
		CairnID:          cairnID,
		DeviceID:         deviceID,
		OriginGeneration: config.FirstGeneration,
		CreatedAt:        now,
		AllowUnencrypted: opts.AllowUnencrypted,
		SyncListen:       syncListen,
	}
	if err := devCfg.SaveDevice(deviceDir); err != nil {
		return nil, err
	}
	seq, err := json.Marshal(SeqState{
		OriginDeviceID:   deviceID,
		OriginGeneration: config.FirstGeneration,
		NextSequence:     config.FirstSequence + 1,
	})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(deviceDir, config.SeqStateName), seq, config.KeyFilePerm); err != nil {
		return nil, err
	}

	// Portable layout + genesis segment.
	for _, sub := range []string{config.ObjectsDirName, config.ExportsDirName, config.ViewsDirName, config.DerivedDirName} {
		if err := os.MkdirAll(filepath.Join(dir, sub), config.DirPerm); err != nil {
			return nil, err
		}
	}
	lg, err := cairnlog.Create(fsx.OS{}, dir, cairnlog.Origin{DeviceID: deviceID, Generation: config.FirstGeneration})
	if err != nil {
		return nil, err
	}
	if err := lg.Append(record, env); err != nil {
		lg.Close()
		return nil, fmt.Errorf("appending genesis: %w", err)
	}
	if err := lg.Close(); err != nil {
		return nil, err
	}
	segPath := filepath.Join(
		cairnlog.SegmentDir(dir, deviceID, config.FirstGeneration),
		cairnlog.SegmentName(config.FirstSequence))

	portable := &config.PortableConfig{
		ConfigVersion: config.PortableConfigVersion,
		CairnID:       cairnID,
		CreatedAt:     now,
	}
	if err := portable.SavePortable(dir); err != nil {
		return nil, err
	}

	fmt.Fprintf(opts.Out, "Initialized cairn %s\n  portable dir:  %s\n  device state:  %s\n  device id:     %s (generation %d)\n  genesis event: %s\n",
		cairnID, dir, deviceDir, deviceID, config.FirstGeneration, env.EventID)
	fmt.Fprintf(opts.Out, "IMPORTANT: back up the root key (%s) to offline operator recovery storage.\n",
		filepath.Join(deviceDir, config.RootKeyName))

	return &InitResult{
		CairnID:        cairnID,
		DeviceID:       deviceID,
		DeviceDir:      deviceDir,
		SegmentPath:    segPath,
		GenesisEventID: env.EventID,
		RootKeyID:      event.KeyID(rootPub),
		DeviceKeyID:    event.KeyID(devicePub),
	}, nil
}

// EnsureEncrypted is the exported encryption gate for flows without a
// device config (read-only restore mode, RULINGS.md R9): no override
// available — unknown/unencrypted fail closed.
func EnsureEncrypted(dir string, checker VolumeChecker, out io.Writer) error {
	if checker == nil {
		checker = SystemVolumeChecker{}
	}
	return checkEncryption(dir, checker, false, out)
}

// GateEncryption is the exported encryption gate WITH the operator override
// (unlike EnsureEncrypted, which fails closed). It enforces the P0 invariant
// that device key material never lands on unencrypted storage: callers that
// write a private key (init, enroll, join — FIX-G3) MUST call it on the volume
// holding the key BEFORE the write. allow=true warns loudly and proceeds;
// allow=false refuses on anything but an encrypted volume.
func GateEncryption(dir string, checker VolumeChecker, allow bool, out io.Writer) error {
	if checker == nil {
		checker = SystemVolumeChecker{}
	}
	if out == nil {
		out = io.Discard
	}
	return checkEncryption(dir, checker, allow, out)
}

// checkEncryption enforces rulings §9: unknown/indeterminate FAILS CLOSED;
// the override warns loudly whenever used.
func checkEncryption(dir string, checker VolumeChecker, allow bool, out io.Writer) error {
	status, detail, err := checker.Status(dir)
	if err != nil {
		status = VolumeUnknown
		detail = fmt.Sprintf("%s (%v)", detail, err)
	}
	if status == VolumeEncrypted {
		return nil
	}
	if allow {
		fmt.Fprintf(out, "WARNING: volume is %s (%s); continuing because --allow-unencrypted is set. The cairn directory should live on an encrypted volume.\n", status, detail)
		return nil
	}
	return fmt.Errorf("volume holding %s is %s (%s); refusing to start — move the cairn directory to an encrypted volume or pass --allow-unencrypted to persist the operator override", dir, status, detail)
}

// Loaded describes an initialized cairn as seen from one device.
type Loaded struct {
	Dir       string
	DeviceDir string
	Portable  *config.PortableConfig
	Device    *config.DeviceConfig
	Cert      DeviceCert
}

// ErrRestoredCopy: portable data without device-local identity. WRITES are
// refused (spec §3.2); read-only operations are permitted (RULINGS.md R9).
var ErrRestoredCopy = errors.New("restored copy: no device-local identity")

// Load opens an initialized cairn directory and its device-local state.
// Portable data without matching device-local identity is the restore case:
// this device must not write under the old origin (spec §3.2).
func Load(dir string) (*Loaded, error) {
	portable, err := config.LoadPortable(dir)
	if err != nil {
		return nil, fmt.Errorf("not an initialized cairn directory: %w", err)
	}
	deviceDir, err := config.DeviceStateDir(portable.CairnID)
	if err != nil {
		return nil, err
	}
	device, err := config.LoadDevice(deviceDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: portable data found at %s but no device-local identity for cairn %s — writes are refused; read-only commands (doctor, search, digest, fetch, identity show) still work, and `cairn init --adopt` creates a new origin identity here", ErrRestoredCopy, dir, portable.CairnID)
		}
		return nil, err
	}
	var cert DeviceCert
	certBlob, err := os.ReadFile(filepath.Join(deviceDir, config.DeviceCertName))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(certBlob, &cert); err != nil {
		return nil, fmt.Errorf("parsing device certificate: %w", err)
	}
	return &Loaded{Dir: dir, DeviceDir: deviceDir, Portable: portable, Device: device, Cert: cert}, nil
}

// StartupCheck re-runs the encryption check for any command that starts
// against an initialized cairn; the persisted override warns on EVERY start.
func (l *Loaded) StartupCheck(checker VolumeChecker, out io.Writer) error {
	if checker == nil {
		checker = SystemVolumeChecker{}
	}
	return checkEncryption(l.Dir, checker, l.Device.AllowUnencrypted, out)
}

// GenesisRecord verifies MESH-LEVEL trust (FIX-F2 ruling 2): genesis is
// mesh-wide, not per-origin — post-migrate the current device's cert lives
// in an earlier origin's log. Returns the verified genesis and asserts the
// current device is an admitted, unrevoked member.
func (l *Loaded) GenesisRecord() (*event.Envelope, *GenesisPayload, error) {
	trust, err := MeshTrust(fsx.OS{}, l.Dir)
	if err != nil {
		return nil, nil, err
	}
	if trust.GenesisEnv == nil {
		return nil, nil, errors.New("no genesis event found in any origin")
	}
	if trust.CairnID != l.Portable.CairnID {
		return nil, nil, fmt.Errorf("genesis cairn_id %s != portable config %s", trust.CairnID, l.Portable.CairnID)
	}
	if !trust.Member(l.Device.DeviceID) {
		return nil, nil, fmt.Errorf("current device %s is not an admitted mesh member", l.Device.DeviceID)
	}
	if trust.Revoked(l.Device.DeviceID) {
		return nil, nil, fmt.Errorf("current device %s is REVOKED (writes refused; complete the migrate ceremony)", l.Device.DeviceID)
	}
	return trust.GenesisEnv, trust.GenesisPayload, nil
}

// Adopt handles restored portable data WITHOUT device-local identity
// (TESTING.md; spec §3.2: a raw folder restore ALWAYS creates a new origin
// identity). The old event history is preserved read-only under
// events-preadopt-<old-cairn-id>/ (never deleted), objects are retained
// (content-addressed, shared), stale derived state is dropped, and a fresh
// identity (new cairn_id, root, device, genesis) is created in place.
func Adopt(opts InitOptions) (*InitResult, error) {
	dir := opts.Dir
	old, err := config.LoadPortable(dir)
	if err != nil {
		return nil, fmt.Errorf("adopt needs an existing portable directory: %w", err)
	}
	// refuse adopt when the device identity is actually present
	if devDir, err := config.DeviceStateDir(old.CairnID); err == nil {
		if _, err := config.LoadDevice(devDir); err == nil {
			return nil, fmt.Errorf("device-local identity for cairn %s exists — adopt is only for restored data without identity", old.CairnID)
		}
	}

	// quarantine the old history (preserved, read-only, never silently deleted)
	archive := filepath.Join(dir, "events-preadopt-"+old.CairnID)
	if _, err := os.Stat(archive); err == nil {
		return nil, fmt.Errorf("adopt archive %s already exists — resolve manually", archive)
	}
	if _, err := os.Stat(filepath.Join(dir, config.EventsDirName)); err == nil {
		if err := os.Rename(filepath.Join(dir, config.EventsDirName), archive); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(filepath.Join(dir, config.PortableConfigName),
		filepath.Join(dir, config.PortableConfigName+".preadopt-"+old.CairnID)); err != nil {
		return nil, err
	}
	// stale derived state is rebuildable and belongs to the old identity
	os.Remove(filepath.Join(dir, config.DerivedDirName, "index.sqlite"))
	os.Remove(filepath.Join(dir, config.DerivedDirName, "index.sqlite-wal"))
	os.Remove(filepath.Join(dir, config.DerivedDirName, "index.sqlite-shm"))
	os.Remove(filepath.Join(dir, config.DerivedDirName, "telemetry.sqlite"))

	res, err := Initialize(opts)
	if err != nil {
		return nil, fmt.Errorf("adopt re-initialization failed (old data archived at %s): %w", archive, err)
	}
	fmt.Fprintf(opts.Out, "adopted: new origin %s created; old history preserved read-only at %s\n", res.CairnID, archive)
	return res, nil
}
