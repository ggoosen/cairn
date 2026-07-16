// Package mcpinstall wires Cairn's MCP server into MCP client apps
// (Claude Desktop, Claude Code) without the operator hand-editing a config
// file. It is P3 onboarding plumbing — it never touches the event log or the
// daemon; it only reads and merges client config files (or drives a client's
// sanctioned MCP CLI).
//
// The supported apps live in a REGISTRY (Registry()): adding a new client is
// one App entry — {Name, detect, configPath, and optionally a managing CLI}.
//
// Two hard invariants (see RULINGS.md R54):
//   - MERGE, never overwrite. Only the "cairn" server entry is added/updated/
//     removed; every other MCP server and every unrelated setting is preserved.
//   - A malformed existing config is REFUSED, never clobbered — it is backed up
//     and that app is skipped.
package mcpinstall

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"
)

// Env carries the (injectable, for tests) environment the registry resolves
// paths and side effects against.
type Env struct {
	Home     string                                     // user home dir
	Cwd      string                                     // working dir (project-local .mcp.json probing)
	Self     string                                     // absolute path of the running cairn binary (os.Executable)
	LookPath func(string) (string, error)               // exec.LookPath, overridable
	Run      func(name string, args ...string) error    // run an external CLI, overridable
	Now      func() int64                               // unix seconds, for backup filenames
}

// DefaultEnv builds an Env from the real process environment. self MUST be the
// absolute path of the currently running cairn binary (resolve via
// os.Executable in the caller — never hardcode a path).
func DefaultEnv(self string) (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, err
	}
	cwd, _ := os.Getwd()
	return Env{
		Home:     home,
		Cwd:      cwd,
		Self:     self,
		LookPath: exec.LookPath,
		Run: func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
			}
			return nil
		},
		Now: func() int64 { return time.Now().Unix() },
	}, nil
}

// App is one supported MCP client in the registry.
type App struct {
	Name string
	// detect reports whether the app is present on this machine and a short detail.
	detect func(Env) (bool, string)
	// configPath is the file the app reads its MCP servers from.
	configPath func(Env) (string, error)
	// cliName, when non-empty and found on PATH, is the sanctioned CLI used for
	// WRITES instead of hand-editing the config file. Reads/detection still go
	// through configPath (the CLI writes the same store).
	cliName string
	cliScope string // scope argument for the CLI (e.g. "user")
}

// Registry is the ordered list of supported apps. Add an app here — nothing
// else — to support a new client.
func Registry() []App {
	return []App{
		{
			Name: "claude-desktop",
			detect: func(e Env) (bool, string) {
				dir := filepath.Join(e.Home, "Library", "Application Support", "Claude")
				if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
					return true, "app dir present"
				}
				if _, err := os.Stat(claudeDesktopPath(e)); err == nil {
					return true, "config present"
				}
				return false, "not installed"
			},
			configPath: func(e Env) (string, error) { return claudeDesktopPath(e), nil },
			// Claude Desktop has no MCP CLI: direct config-file merge.
		},
		{
			Name: "claude-code",
			detect: func(e Env) (bool, string) {
				if _, err := e.LookPath("claude"); err == nil {
					return true, "claude CLI on PATH"
				}
				if _, err := os.Stat(claudeCodePath(e)); err == nil {
					return true, "user config present"
				}
				return false, "not installed"
			},
			configPath: func(e Env) (string, error) { return claudeCodePath(e), nil },
			cliName:    "claude",
			cliScope:   "user",
		},
	}
}

func claudeDesktopPath(e Env) string {
	return filepath.Join(e.Home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
}

func claudeCodePath(e Env) string {
	// User-scope Claude Code MCP servers live in ~/.claude.json under top-level
	// "mcpServers" — the same store `claude mcp add --scope user` writes.
	return filepath.Join(e.Home, ".claude.json")
}

// ---- pure config-merge core (regression-tested in mcpinstall_test.go) ----

// DesiredEntry is the cairn server entry we install: the current binary + ["mcp"].
// The args slice is []any{"mcp"} so it compares equal (reflect.DeepEqual) to the
// same entry after a JSON round-trip.
func DesiredEntry(self string) map[string]any {
	return map[string]any{"command": self, "args": []any{"mcp"}}
}

// ParseConfig parses a client config. A nil/empty input yields an empty config
// (create-from-absent). Malformed JSON returns an error so callers can refuse
// rather than clobber.
func ParseConfig(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("malformed JSON: %w", err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg, nil
}

// MarshalConfig renders a config as 2-space-indented JSON with a trailing
// newline. (Go maps reorder keys; RULINGS.md R54 accepts reserialization.)
func MarshalConfig(cfg map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// CairnCommand returns the command currently configured for the cairn server.
func CairnCommand(cfg map[string]any) (cmd string, present bool) {
	servers, _ := cfg["mcpServers"].(map[string]any)
	entry, ok := servers["cairn"].(map[string]any)
	if !ok {
		return "", false
	}
	c, _ := entry["command"].(string)
	return c, true
}

// MergeCairn adds or updates ONLY the cairn server entry, preserving every
// other server and setting. It reports whether anything changed and the
// previous cairn command (empty if none), so callers can detect stale paths.
func MergeCairn(cfg map[string]any, self string) (changed bool, prevCmd string) {
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	prevCmd, _ = CairnCommand(cfg)
	desired := DesiredEntry(self)
	if reflect.DeepEqual(servers["cairn"], desired) {
		return false, prevCmd
	}
	servers["cairn"] = desired
	cfg["mcpServers"] = servers
	return true, prevCmd
}

// RemoveCairn removes ONLY the cairn server entry, leaving everything else.
func RemoveCairn(cfg map[string]any) (changed bool) {
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		return false
	}
	if _, ok := servers["cairn"]; !ok {
		return false
	}
	delete(servers, "cairn")
	return true
}

// ---- read / write / backup ----

func readConfig(path string) (raw []byte, exists bool, err error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// backup copies an existing config to <path>.cairn-backup-<unix-ts> before any
// write. It is a no-op (empty return) when the file does not exist.
func backup(e Env, path string) (string, error) {
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	dst := fmt.Sprintf("%s.cairn-backup-%d", path, e.Now())
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(dst, src, mode); err != nil {
		return "", err
	}
	return dst, nil
}

// writeConfigFile atomically writes cfg, validating that the rendered bytes
// re-parse as JSON before rename (belt-and-suspenders on the marshal).
func writeConfigFile(path string, cfg map[string]any) error {
	out, err := MarshalConfig(cfg)
	if err != nil {
		return err
	}
	if _, err := ParseConfig(out); err != nil {
		return fmt.Errorf("refusing to write config that does not re-parse: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".cairn-tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- status / results ----

// Status is a read-only snapshot for `mcp-install --list` (Inspect mutates nothing).
type Status struct {
	App        string
	Installed  bool   // app detected on this machine
	Detail     string // detection detail
	ConfigPath string // config file, or "via CLI: <path>"
	ViaCLI     bool   // writes go through the app's MCP CLI
	Exists     bool   // config file exists
	Malformed  bool   // config file is present but not valid JSON
	Configured bool   // a cairn server entry is present
	CurrentCmd string // the command that entry points at
	Stale      bool   // configured but pointing at a different binary than Self
}

// Result is the outcome of an Install/Uninstall action.
type Result struct {
	App        string
	Changed    bool
	Method     string // "cli" or "file"
	ConfigPath string
	BackupPath string // "" when nothing was backed up
	PrevCmd    string // previous cairn command, when a stale path was fixed
	Message    string
	NextStep   string
}

func (a App) viaCLI(e Env) bool {
	if a.cliName == "" {
		return false
	}
	_, err := e.LookPath(a.cliName)
	return err == nil
}

// Inspect returns a read-only status. It never writes.
func (a App) Inspect(e Env) Status {
	s := Status{App: a.Name}
	s.Installed, s.Detail = a.detect(e)
	path, _ := a.configPath(e)
	s.ViaCLI = a.viaCLI(e)
	if s.ViaCLI {
		s.ConfigPath = "via CLI (" + a.cliName + "): " + path
	} else {
		s.ConfigPath = path
	}
	raw, exists, err := readConfig(path)
	if err != nil {
		return s
	}
	s.Exists = exists
	if !exists {
		return s
	}
	cfg, perr := ParseConfig(raw)
	if perr != nil {
		s.Malformed = true
		return s
	}
	cmd, present := CairnCommand(cfg)
	s.Configured = present
	s.CurrentCmd = cmd
	s.Stale = present && cmd != e.Self
	return s
}

// Install adds/updates only the cairn entry for this app. It is idempotent, backs
// up before any write, and refuses (without clobbering) a malformed config.
func (a App) Install(e Env) (Result, error) {
	r := Result{App: a.Name, NextStep: a.nextStep()}
	path, err := a.configPath(e)
	if err != nil {
		return r, err
	}
	r.ConfigPath = path

	raw, exists, err := readConfig(path)
	if err != nil {
		return r, err
	}
	cfg, perr := ParseConfig(raw)
	if perr != nil {
		// refuse: back up the malformed file and skip (never clobber)
		bp, _ := backup(e, path)
		r.BackupPath = bp
		return r, fmt.Errorf("existing config is malformed, backed up to %s and skipped: %w", bp, perr)
	}

	changed, prevCmd := MergeCairn(cfg, e.Self)
	if !changed {
		r.Message = "already up to date"
		return r, nil
	}
	r.Changed = true
	r.PrevCmd = prevCmd

	if exists {
		if bp, err := backup(e, path); err != nil {
			return r, err
		} else {
			r.BackupPath = bp
		}
	}

	if a.viaCLI(e) {
		r.Method = "cli"
		// The CLI errors if the name already exists, so replace: remove-then-add.
		if prevCmd != "" {
			_ = e.Run(a.cliName, "mcp", "remove", "cairn", "--scope", a.cliScope)
		}
		if err := e.Run(a.cliName, "mcp", "add", "cairn", "--scope", a.cliScope, "--", e.Self, "mcp"); err != nil {
			return r, err
		}
	} else {
		r.Method = "file"
		if err := writeConfigFile(path, cfg); err != nil {
			return r, err
		}
	}
	if prevCmd != "" && prevCmd != e.Self {
		r.Message = fmt.Sprintf("updated stale command (%s → %s)", prevCmd, e.Self)
	} else {
		r.Message = "installed"
	}
	return r, nil
}

// Uninstall removes only the cairn entry, leaving everything else intact.
func (a App) Uninstall(e Env) (Result, error) {
	r := Result{App: a.Name}
	path, err := a.configPath(e)
	if err != nil {
		return r, err
	}
	r.ConfigPath = path

	raw, exists, err := readConfig(path)
	if err != nil {
		return r, err
	}
	if !exists {
		r.Message = "not configured, nothing to remove"
		return r, nil
	}
	cfg, perr := ParseConfig(raw)
	if perr != nil {
		bp, _ := backup(e, path)
		r.BackupPath = bp
		return r, fmt.Errorf("existing config is malformed, backed up to %s and skipped: %w", bp, perr)
	}
	if !RemoveCairn(cfg) {
		r.Message = "not configured, nothing to remove"
		return r, nil
	}
	r.Changed = true
	if bp, err := backup(e, path); err != nil {
		return r, err
	} else {
		r.BackupPath = bp
	}
	if a.viaCLI(e) {
		r.Method = "cli"
		if err := e.Run(a.cliName, "mcp", "remove", "cairn", "--scope", a.cliScope); err != nil {
			return r, err
		}
	} else {
		r.Method = "file"
		if err := writeConfigFile(path, cfg); err != nil {
			return r, err
		}
	}
	r.Message = "removed"
	return r, nil
}

func (a App) nextStep() string {
	switch a.Name {
	case "claude-desktop":
		return "Restart Claude Desktop to load Cairn."
	case "claude-code":
		return "Cairn is available in new Claude Code sessions (run `claude mcp list` to confirm)."
	default:
		return "Restart the app to load Cairn."
	}
}

// Lookup returns the registered app by name.
func Lookup(name string) (App, bool) {
	for _, a := range Registry() {
		if a.Name == name {
			return a, true
		}
	}
	return App{}, false
}
