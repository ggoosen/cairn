package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

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

	// Ephemeral housekeeping tunables (RULINGS.md R10, R13). Preferred form:
	// duration strings supporting s/m/h and a d suffix ("30s", "90m", "7d",
	// "1d12h"). The integer keys remain for backward compatibility; setting
	// BOTH forms for the same knob is a config error. Zero/empty → defaults
	// (EphemeralTTL = 7d, HousekeepInterval = 1h).
	EphemeralTTLStr      string `toml:"ephemeral_ttl,omitempty"`
	HousekeepIntervalStr string `toml:"housekeep_interval,omitempty"`
	EphemeralTTLHours    int64  `toml:"ephemeral_ttl_hours,omitempty"`
	HousekeepMinutes     int64  `toml:"housekeep_minutes,omitempty"`
}

// durationRe splits a leading day component ("7d", "1d12h") off a Go
// duration string; time.ParseDuration handles the rest.
var durationRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)d(.*)$`)

// ParseTTLDuration parses a duration string with day support (FIX-F8.1).
func ParseTTLDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	var days time.Duration
	if m := durationRe.FindStringSubmatch(s); m != nil {
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, fmt.Errorf("bad day component in %q: %w", s, err)
		}
		days = time.Duration(f * 24 * float64(time.Hour))
		s = m[2]
		if s == "" {
			return days, nil
		}
	}
	rest, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q (use Go durations plus optional d suffix, e.g. \"30s\", \"90m\", \"7d\", \"1d12h\"): %w", s, err)
	}
	if days+rest <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", s)
	}
	return days + rest, nil
}

// EphemeralTTL resolves the configured TTL: duration string wins, then the
// legacy integer key, then the default. Validation happens in LoadPortable.
func (c *PortableConfig) EphemeralTTL() time.Duration {
	if c.EphemeralTTLStr != "" {
		if d, err := ParseTTLDuration(c.EphemeralTTLStr); err == nil {
			return d
		}
	}
	if c.EphemeralTTLHours > 0 {
		return time.Duration(c.EphemeralTTLHours) * time.Hour
	}
	return EphemeralTTLDefault
}

// HousekeepInterval resolves the sweep cadence (same precedence).
func (c *PortableConfig) HousekeepInterval() time.Duration {
	if c.HousekeepIntervalStr != "" {
		if d, err := ParseTTLDuration(c.HousekeepIntervalStr); err == nil {
			return d
		}
	}
	if c.HousekeepMinutes > 0 {
		return time.Duration(c.HousekeepMinutes) * time.Minute
	}
	return HousekeepIntervalDefault
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

	// SyncListen (N5): tailnet host:port for the sync listener. Device-local
	// by design (networking is per-device). Empty = no listener.
	SyncListen string `toml:"sync_listen,omitempty"`

	// SyncPeers (N6): tailnet host:port addresses of known peers. The
	// anti-entropy sweep dials each (R29); a just-appended event kicks an
	// immediate sweep. Device-local (networking is per-device).
	SyncPeers []string `toml:"sync_peers,omitempty"`

	// Role (P3-3): the node's role in the mesh — RoleFull (default) or RoleThin.
	// Device-local (a role is a per-device operational choice, not a mesh fact).
	// A thin node holds only a recent window + selected objects, is NOT counted
	// toward durability, is never advertised as a normal node, and its universal
	// search is partial (spec §7). Empty = full.
	Role string `toml:"role,omitempty"`
}

// IsThin reports whether the device is configured as a thin node (spec §7).
// The empty/unset role is a full node.
func (c *DeviceConfig) IsThin() bool { return c.Role == RoleThin }

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
// DeviceStateBase returns the device-local root (all cairns).
func DeviceStateBase() (string, error) {
	d, err := DeviceStateDir("x")
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Dir(d)), nil
}

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
	// FIX-F8.1: validate duration strings up front; refuse ambiguous configs
	if cfg.EphemeralTTLStr != "" {
		if _, err := ParseTTLDuration(cfg.EphemeralTTLStr); err != nil {
			return nil, fmt.Errorf("cairn.toml ephemeral_ttl: %w", err)
		}
		if cfg.EphemeralTTLHours > 0 {
			return nil, errors.New("cairn.toml sets BOTH ephemeral_ttl and ephemeral_ttl_hours — keep one (the duration-string form is preferred)")
		}
	}
	if cfg.HousekeepIntervalStr != "" {
		if _, err := ParseTTLDuration(cfg.HousekeepIntervalStr); err != nil {
			return nil, fmt.Errorf("cairn.toml housekeep_interval: %w", err)
		}
		if cfg.HousekeepMinutes > 0 {
			return nil, errors.New("cairn.toml sets BOTH housekeep_interval and housekeep_minutes — keep one (the duration-string form is preferred)")
		}
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
