// Package mcpinstall wires Cairn's MCP server into MCP client apps
// (Claude Desktop, Claude Code, Codex CLI) without the operator hand-editing a
// config file. It is P3 onboarding plumbing — it never touches the event log or
// the daemon; it only reads and merges client config files (or drives a
// client's sanctioned MCP CLI).
//
// The supported apps live in a REGISTRY (Registry()): adding a new client is
// one App entry — {Name, detect, configPath, a config codec, and optionally a
// managing CLI}.
//
// Config formats differ per client — Claude uses JSON (top-level "mcpServers"),
// Codex uses TOML ([mcp_servers.<name>] tables). Both decode to the same
// map[string]any shape, so the merge/remove/read logic is format-agnostic; only
// the servers key and the parse/marshal codec vary (see codec).
//
// Two hard invariants (see RULINGS.md R54), identical across JSON and TOML:
//   - MERGE, never overwrite. Only the "cairn" server entry is added/updated/
//     removed; every other MCP server and every unrelated setting/table is
//     preserved.
//   - A malformed existing config is REFUSED, never clobbered — it is backed up
//     and that app is skipped.
package mcpinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"time"

	"github.com/BurntSushi/toml"
)

// Env carries the (injectable, for tests) environment the registry resolves
// paths and side effects against.
type Env struct {
	Home          string                                  // user home dir
	Cwd           string                                  // working dir (project-local .mcp.json probing)
	Self          string                                  // absolute path of the running cairn binary (os.Executable)
	GOOS          string                                  // platform override; "" means runtime.GOOS
	XDGConfigHome string                                  // $XDG_CONFIG_HOME; "" means ~/.config
	LookPath      func(string) (string, error)            // exec.LookPath, overridable
	Run           func(name string, args ...string) error // run an external CLI, overridable
	Now           func() int64                            // unix seconds, for backup filenames
}

// goos is the platform the registry resolves paths for. An Env built by hand
// (tests, callers that only set Home) leaves it empty and gets the real one.
func (e Env) goos() string {
	if e.GOOS != "" {
		return e.GOOS
	}
	return runtime.GOOS
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
		Home:          home,
		Cwd:           cwd,
		Self:          self,
		GOOS:          runtime.GOOS,
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		LookPath:      exec.LookPath,
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
	// codec is the config format (JSON for Claude, TOML for Codex): the servers
	// key plus parse/marshal. Reads and the file-merge fallback go through it.
	codec codec
	// cliName, when non-empty and found on PATH, is the sanctioned CLI used for
	// WRITES instead of hand-editing the config file. Reads/detection still go
	// through configPath (the CLI writes the same store).
	cliName string
	// cliAdd / cliRemove build the CLI argument vector (after cliName) for the
	// add/remove of the cairn server. They differ per client (e.g. Claude takes
	// --scope user; Codex is global with no scope).
	cliAdd    func(self, view string) []string
	cliRemove func() []string
}

// codec captures a config format: which top-level key holds the MCP servers,
// and how the file is parsed/serialized. JSON (Claude) and TOML (Codex) both
// decode to map[string]any, so the merge core is shared; only this varies.
type codec struct {
	serversKey string
	parse      func([]byte) (map[string]any, error)
	marshal    func(map[string]any) ([]byte, error)
}

func jsonCodec() codec {
	return codec{serversKey: "mcpServers", parse: parseJSON, marshal: marshalJSON}
}

func tomlCodec() codec {
	return codec{serversKey: "mcp_servers", parse: parseTOML, marshal: marshalTOML}
}

// Registry is the ordered list of supported apps. Add an app here — nothing
// else — to support a new client.
func Registry() []App {
	return []App{
		{
			Name: "claude-desktop",
			detect: func(e Env) (bool, string) {
				if fi, err := os.Stat(claudeDesktopDir(e)); err == nil && fi.IsDir() {
					return true, "app dir present"
				}
				if _, err := os.Stat(claudeDesktopPath(e)); err == nil {
					return true, "config present"
				}
				return false, "not installed"
			},
			configPath: func(e Env) (string, error) { return claudeDesktopPath(e), nil },
			codec:      jsonCodec(),
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
			codec:      jsonCodec(),
			cliName:    "claude",
			cliAdd: func(self, view string) []string {
				return []string{"mcp", "add", "cairn", "--scope", "user", "--", self, "mcp", "--view", view, "--actor", view}
			},
			cliRemove: func() []string {
				return []string{"mcp", "remove", "cairn", "--scope", "user"}
			},
		},
		{
			// Codex CLI (OpenAI). Config is TOML at ~/.codex/config.toml, one
			// [mcp_servers.<name>] table per server. Codex ships a sanctioned
			// MCP CLI (`codex mcp add/remove/list`) which we PREFER for writes,
			// exactly as with Claude Code; the TOML file-merge is the fallback
			// when the codex binary is not on PATH. `codex mcp add` overwrites an
			// existing entry cleanly, so the remove-then-add replace works too.
			Name: "codex",
			detect: func(e Env) (bool, string) {
				if _, err := e.LookPath("codex"); err == nil {
					return true, "codex CLI on PATH"
				}
				dir := filepath.Join(e.Home, ".codex")
				if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
					return true, "~/.codex dir present"
				}
				return false, "not installed"
			},
			configPath: func(e Env) (string, error) { return codexPath(e), nil },
			codec:      tomlCodec(),
			cliName:    "codex",
			cliAdd: func(self, view string) []string {
				// codex mcp add cairn -- <self> mcp ...   (global; no scope flag)
				return []string{"mcp", "add", "cairn", "--", self, "mcp", "--view", view, "--actor", view}
			},
			cliRemove: func() []string {
				return []string{"mcp", "remove", "cairn"}
			},
		},
	}
}

// claudeDesktopDir is Claude Desktop's Electron userData directory — the app
// dir that holds claude_desktop_config.json, and the presence of which is what
// "installed" means for a GUI app with no CLI to probe.
//
// macOS is `~/Library/Application Support/Claude`. On Linux, Electron's
// userData resolves to `$XDG_CONFIG_HOME/<app>`, defaulting to `~/.config/<app>`
// — so the Linux builds read `~/.config/Claude/claude_desktop_config.json`,
// and honouring XDG_CONFIG_HOME is what makes a relocated config dir work
// rather than silently writing a file the app never reads.
//
// Non-darwin falls through to the XDG shape deliberately: Linux is the only
// other supported platform (CLAUDE.md — macOS primary, Linux best-effort, no
// Windows), so there is no %APPDATA% branch to get subtly wrong.
func claudeDesktopDir(e Env) string {
	if e.goos() == "darwin" {
		return filepath.Join(e.Home, "Library", "Application Support", "Claude")
	}
	base := e.XDGConfigHome
	if base == "" {
		base = filepath.Join(e.Home, ".config")
	}
	return filepath.Join(base, "Claude")
}

func claudeDesktopPath(e Env) string {
	return filepath.Join(claudeDesktopDir(e), "claude_desktop_config.json")
}

func claudeCodePath(e Env) string {
	// User-scope Claude Code MCP servers live in ~/.claude.json under top-level
	// "mcpServers" — the same store `claude mcp add --scope user` writes.
	return filepath.Join(e.Home, ".claude.json")
}

func codexPath(e Env) string {
	// Codex reads global MCP servers from ~/.codex/config.toml under
	// [mcp_servers.<name>] tables — the same store `codex mcp add` writes.
	return filepath.Join(e.Home, ".codex", "config.toml")
}

// ---- config codecs (JSON for Claude, TOML for Codex) ----

// parseJSON parses a JSON client config. A nil/empty input yields an empty
// config (create-from-absent). Malformed JSON returns an error so callers can
// refuse rather than clobber.
func parseJSON(raw []byte) (map[string]any, error) {
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

// marshalJSON renders a config as 2-space-indented JSON with a trailing
// newline. (Go maps reorder keys; RULINGS.md R54 accepts reserialization.)
func marshalJSON(cfg map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// parseTOML parses a TOML client config (Codex). Empty input yields an empty
// config; malformed TOML errors so callers refuse rather than clobber. Nested
// tables ([mcp_servers.cairn]) decode to nested map[string]any, so the shared
// merge core operates on them exactly as on JSON.
func parseTOML(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var cfg map[string]any
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("malformed TOML: %w", err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg, nil
}

// marshalTOML renders a config as TOML. BurntSushi emits top-level scalars
// before table headers (required for valid TOML) and sorts keys, so the output
// is deterministic. RULINGS.md R54 accepts reserialization (comments/formatting
// of the file-merge fallback are not preserved; the CLI path is preferred).
func marshalTOML(cfg map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- pure config-merge core (regression-tested in mcpinstall_test.go) ----

// DesiredEntry is the cairn server entry we install: the current binary +
// ["mcp", "--view", <view>, "--actor", <view>]. DEPLOY-E1: every client
// used to share the single default view "mcp", collapsing per-view
// interest, onboarding records and attributable telemetry into one bucket
// (DOGFOOD's "one view per client" was only true for hand-written
// configs). The args are []any so the entry compares equal
// (reflect.DeepEqual) after a JSON *or* TOML round-trip.
func DesiredEntry(self, view string) map[string]any {
	return map[string]any{"command": self, "args": []any{"mcp", "--view", view, "--actor", view}}
}

// ParseConfig parses a JSON client config. Retained as the JSON entry point used
// by the CLI-surface tests; format-aware callers use App.codec.parse.
func ParseConfig(raw []byte) (map[string]any, error) { return parseJSON(raw) }

// MarshalConfig renders a config as JSON (see ParseConfig).
func MarshalConfig(cfg map[string]any) ([]byte, error) { return marshalJSON(cfg) }

// cairnCommand returns the command configured for the cairn server under the
// given servers key ("mcpServers" for JSON, "mcp_servers" for TOML).
func cairnCommand(cfg map[string]any, serversKey string) (cmd string, present bool) {
	servers, _ := cfg[serversKey].(map[string]any)
	entry, ok := servers["cairn"].(map[string]any)
	if !ok {
		return "", false
	}
	c, _ := entry["command"].(string)
	return c, true
}

// CairnCommand reads the cairn command from a JSON config ("mcpServers"). Kept
// for the CLI-surface tests; format-aware callers use cairnCommand.
func CairnCommand(cfg map[string]any) (cmd string, present bool) {
	return cairnCommand(cfg, "mcpServers")
}

// mergeCairn adds or updates ONLY the cairn server entry under serversKey,
// preserving every other server, table, and setting. It reports whether
// anything changed and the previous cairn command (empty if none), so callers
// can detect stale paths.
func mergeCairn(cfg map[string]any, self, view, serversKey string) (changed bool, prevCmd string) {
	servers, _ := cfg[serversKey].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	prevCmd, _ = cairnCommand(cfg, serversKey)
	desired := DesiredEntry(self, view)
	if reflect.DeepEqual(servers["cairn"], desired) {
		return false, prevCmd
	}
	servers["cairn"] = desired
	cfg[serversKey] = servers
	return true, prevCmd
}

// MergeCairn merges into a JSON config ("mcpServers"). Kept for the CLI-surface
// tests; format-aware callers use mergeCairn.
func MergeCairn(cfg map[string]any, self, view string) (changed bool, prevCmd string) {
	return mergeCairn(cfg, self, view, "mcpServers")
}

// removeCairn removes ONLY the cairn server entry under serversKey, leaving
// everything else.
func removeCairn(cfg map[string]any, serversKey string) (changed bool) {
	servers, _ := cfg[serversKey].(map[string]any)
	if servers == nil {
		return false
	}
	if _, ok := servers["cairn"]; !ok {
		return false
	}
	delete(servers, "cairn")
	return true
}

// RemoveCairn removes from a JSON config ("mcpServers"). Kept for the CLI-surface
// tests; format-aware callers use removeCairn.
func RemoveCairn(cfg map[string]any) (changed bool) {
	return removeCairn(cfg, "mcpServers")
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

// writeConfigFile atomically writes cfg via the app's codec, validating that
// the rendered bytes re-parse before rename (belt-and-suspenders on the marshal).
func writeConfigFile(path string, cfg map[string]any, c codec) error {
	out, err := c.marshal(cfg)
	if err != nil {
		return err
	}
	if _, err := c.parse(out); err != nil {
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

// ConfigPath is the file this app reads its MCP servers from, resolved for
// e's platform. Exported so callers (and tests) ask the registry where a
// config lives instead of hardcoding a path that is only right on one OS.
func (a App) ConfigPath(e Env) (string, error) { return a.configPath(e) }

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
	cfg, perr := a.codec.parse(raw)
	if perr != nil {
		s.Malformed = true
		return s
	}
	cmd, present := cairnCommand(cfg, a.codec.serversKey)
	s.Configured = present
	s.CurrentCmd = cmd
	s.Stale = present && cmd != e.Self
	return s
}

// Install adds/updates only the cairn entry for this app. It is idempotent, backs
// up before any write, and refuses (without clobbering) a malformed config.
// view "" defaults to the app's name (DEPLOY-E1: one view per client keeps
// digests, interest and telemetry attributable).
func (a App) Install(e Env, view string) (Result, error) {
	if view == "" {
		view = a.Name
	}
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
	cfg, perr := a.codec.parse(raw)
	if perr != nil {
		// refuse: back up the malformed file and skip (never clobber)
		bp, _ := backup(e, path)
		r.BackupPath = bp
		return r, fmt.Errorf("existing config is malformed, backed up to %s and skipped: %w", bp, perr)
	}

	changed, prevCmd := mergeCairn(cfg, e.Self, view, a.codec.serversKey)
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
		// The CLI errors (Claude) or overwrites (Codex) if the name already
		// exists, so replace uniformly: remove-then-add. remove is best-effort.
		if prevCmd != "" {
			_ = e.Run(a.cliName, a.cliRemove()...)
		}
		if err := e.Run(a.cliName, a.cliAdd(e.Self, view)...); err != nil {
			return r, err
		}
	} else {
		r.Method = "file"
		if err := writeConfigFile(path, cfg, a.codec); err != nil {
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
	cfg, perr := a.codec.parse(raw)
	if perr != nil {
		bp, _ := backup(e, path)
		r.BackupPath = bp
		return r, fmt.Errorf("existing config is malformed, backed up to %s and skipped: %w", bp, perr)
	}
	if !removeCairn(cfg, a.codec.serversKey) {
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
		if err := e.Run(a.cliName, a.cliRemove()...); err != nil {
			return r, err
		}
	} else {
		r.Method = "file"
		if err := writeConfigFile(path, cfg, a.codec); err != nil {
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
	case "codex":
		return "Cairn is available in new Codex sessions (run `codex mcp list` to confirm)."
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
