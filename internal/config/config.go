package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

// PortableConfig lives at <cairn dir>/cairn.toml. It travels with the data
// and must never contain key material or device-local flags.
type PortableConfig struct {
	ConfigVersion int    `toml:"config_version"`
	CairnID       string `toml:"cairn_id"`
	CreatedAt     string `toml:"created_at"` // RFC 3339 UTC, WallTimeFormat

	// Text-class ceilings (rulings §5); zero means "use the default constant".
	DailyCanonicalBytes      int64 `toml:"daily_canonical_bytes,omitempty"`
	PerMessageCanonicalBytes int64 `toml:"per_message_canonical_bytes,omitempty"`
}

// DeviceConfig lives in device-local state, never in the portable directory
// (identity/data separation, spec §3, rulings §9).
type DeviceConfig struct {
	ConfigVersion    int    `toml:"config_version"`
	CairnID          string `toml:"cairn_id"`
	DeviceID         string `toml:"device_id"`
	OriginGeneration int    `toml:"origin_generation"`
	CreatedAt        string `toml:"created_at"`

	// AllowUnencrypted persists the --allow-unencrypted operator override.
	// Device-local by ruling §9; surfaced with a warning on every startup.
	AllowUnencrypted bool `toml:"allow_unencrypted"`
}

// PortableDir resolves the portable cairn directory: explicit flag value,
// then $CAIRN_DIR, then ~/cairn.
func PortableDir(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	if env := os.Getenv("CAIRN_DIR"); env != "" {
		return filepath.Abs(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, DefaultDirName), nil
}

// DeviceStateDir returns the device-local state directory for a cairn:
// macOS ~/Library/Application Support/cairn/<cairn_id>/device,
// otherwise $XDG_DATA_HOME (default ~/.local/share)/cairn/<cairn_id>/device.
// Overridable via $CAIRN_DEVICE_STATE_DIR (primarily for tests).
func DeviceStateDir(cairnID string) (string, error) {
	if env := os.Getenv("CAIRN_DEVICE_STATE_DIR"); env != "" {
		return filepath.Join(env, cairnID, "device"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	var base string
	if runtime.GOOS == "darwin" {
		base = filepath.Join(home, "Library", "Application Support", "cairn")
	} else {
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			base = filepath.Join(xdg, "cairn")
		} else {
			base = filepath.Join(home, ".local", "share", "cairn")
		}
	}
	return filepath.Join(base, cairnID, "device"), nil
}

// LoadPortable reads and validates <dir>/cairn.toml.
func LoadPortable(dir string) (*PortableConfig, error) {
	var cfg PortableConfig
	if err := loadTOML(filepath.Join(dir, PortableConfigName), &cfg); err != nil {
		return nil, err
	}
	if cfg.ConfigVersion != PortableConfigVersion {
		return nil, fmt.Errorf("portable config version %d not supported (want %d)", cfg.ConfigVersion, PortableConfigVersion)
	}
	if cfg.CairnID == "" {
		return nil, errors.New("portable config missing cairn_id")
	}
	return &cfg, nil
}

// LoadDevice reads and validates config-device.toml from the device state dir.
func LoadDevice(deviceDir string) (*DeviceConfig, error) {
	var cfg DeviceConfig
	if err := loadTOML(filepath.Join(deviceDir, DeviceConfigName), &cfg); err != nil {
		return nil, err
	}
	if cfg.ConfigVersion != DeviceConfigVersion {
		return nil, fmt.Errorf("device config version %d not supported (want %d)", cfg.ConfigVersion, DeviceConfigVersion)
	}
	if cfg.CairnID == "" || cfg.DeviceID == "" {
		return nil, errors.New("device config missing cairn_id or device_id")
	}
	return &cfg, nil
}

// SavePortable writes cairn.toml (world-readable data, no secrets).
func (c *PortableConfig) SavePortable(dir string) error {
	return saveTOML(filepath.Join(dir, PortableConfigName), c, FilePerm)
}

// SaveDevice writes config-device.toml with restrictive permissions.
func (c *DeviceConfig) SaveDevice(deviceDir string) error {
	return saveTOML(filepath.Join(deviceDir, DeviceConfigName), c, KeyFilePerm)
}

func loadTOML(path string, v any) error {
	meta, err := toml.DecodeFile(path, v)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if undec := meta.Undecoded(); len(undec) > 0 {
		return fmt.Errorf("%s: unknown key %q (strict schema, config_version mismatch?)", path, undec[0].String())
	}
	return nil
}

func saveTOML(path string, v any, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	enc := toml.NewEncoder(f)
	if err := enc.Encode(v); err != nil {
		f.Close()
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
