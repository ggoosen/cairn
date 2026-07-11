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
	// P0 keeps the root key device-local (0600) so device.add / migrate can
	// root-sign offline. Spec §3.1 wants it in separate operator recovery
	// storage; recorded in PROGRESS.md ("Author rulings needed").
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
	devCfg := &config.DeviceConfig{
		ConfigVersion:    config.DeviceConfigVersion,
		CairnID:          cairnID,
		DeviceID:         deviceID,
		OriginGeneration: config.FirstGeneration,
		CreatedAt:        now,
		AllowUnencrypted: opts.AllowUnencrypted,
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
	segPath, err := cairnlog.WriteInitialSegment(dir, deviceID, config.FirstGeneration, config.FirstSequence, [][]byte{record})
	if err != nil {
		return nil, err
	}

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
			return nil, fmt.Errorf("portable data found at %s but no device-local identity for cairn %s: this looks like a restored copy; this device cannot write under the old origin (adopt path lands in a later milestone)", dir, portable.CairnID)
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

// GenesisRecord reads and fully verifies the genesis event from the log.
func (l *Loaded) GenesisRecord() (*event.Envelope, *GenesisPayload, error) {
	segPath := filepath.Join(
		cairnlog.SegmentDir(l.Dir, l.Device.DeviceID, l.Device.OriginGeneration),
		cairnlog.SegmentName(config.FirstSequence))
	records, err := cairnlog.ReadSegment(segPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading genesis segment: %w", err)
	}
	if len(records) == 0 {
		return nil, nil, errors.New("genesis segment is empty")
	}
	env, pl, err := VerifyGenesis(records[0])
	if err != nil {
		return nil, nil, fmt.Errorf("genesis verification failed: %w", err)
	}
	if env.CairnID != l.Portable.CairnID {
		return nil, nil, fmt.Errorf("genesis cairn_id %s != portable config %s", env.CairnID, l.Portable.CairnID)
	}
	return env, pl, nil
}
