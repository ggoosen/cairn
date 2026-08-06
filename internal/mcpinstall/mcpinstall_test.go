package mcpinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testEnv builds a hermetic Env rooted at a temp HOME, with a fixed clock and a
// captured CLI runner. cliAvailable toggles whether `claude` is "on PATH".
func testEnv(t *testing.T, cliAvailable bool, calls *[][]string) Env {
	t.Helper()
	home := t.TempDir()
	return Env{
		Home: home,
		Cwd:  home,
		Self: "/opt/cairn/bin/cairn",
		LookPath: func(name string) (string, error) {
			if name == "claude" && cliAvailable {
				return "/usr/local/bin/claude", nil
			}
			return "", os.ErrNotExist
		},
		Run: func(name string, args ...string) error {
			if calls != nil {
				*calls = append(*calls, append([]string{name}, args...))
			}
			return nil
		},
		Now: func() int64 { return 1700000000 },
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseConfig(b)
	if err != nil {
		t.Fatalf("re-read config is not valid JSON: %v", err)
	}
	return m
}

func desktopApp(t *testing.T) App {
	a, ok := Lookup("claude-desktop")
	if !ok {
		t.Fatal("claude-desktop not registered")
	}
	return a
}

func codeApp(t *testing.T) App {
	a, ok := Lookup("claude-code")
	if !ok {
		t.Fatal("claude-code not registered")
	}
	return a
}

// ---- pure merge core ----

// TEST: merge preserves existing servers and unrelated settings.
func TestMergePreservesEverythingElse(t *testing.T) {
	cfg := map[string]any{
		"theme": "dark",
		"mcpServers": map[string]any{
			"linkedin":  map[string]any{"command": "node", "args": []any{"li.js"}},
			"dataforseo": map[string]any{"command": "npx", "args": []any{"dfs"}},
		},
	}
	changed, prev := MergeCairn(cfg, "/opt/cairn/bin/cairn", "claude-desktop")
	if !changed {
		t.Fatal("expected changed=true when adding a new cairn entry")
	}
	if prev != "" {
		t.Fatalf("expected no previous command, got %q", prev)
	}
	if cfg["theme"] != "dark" {
		t.Error("unrelated top-level setting was dropped")
	}
	servers := cfg["mcpServers"].(map[string]any)
	for _, name := range []string{"linkedin", "dataforseo", "cairn"} {
		if _, ok := servers[name]; !ok {
			t.Errorf("server %q missing after merge", name)
		}
	}
	if got := servers["cairn"]; !reflect.DeepEqual(got, DesiredEntry("/opt/cairn/bin/cairn", "claude-desktop")) {
		t.Errorf("cairn entry = %#v", got)
	}
}

// TEST: idempotent second merge = no change.
func TestMergeIdempotent(t *testing.T) {
	cfg := map[string]any{}
	if c, _ := MergeCairn(cfg, "/opt/cairn/bin/cairn", "claude-desktop"); !c {
		t.Fatal("first merge should change")
	}
	// round-trip through JSON to mimic a real re-read (args become []any).
	b, _ := MarshalConfig(cfg)
	reparsed, _ := ParseConfig(b)
	if c, _ := MergeCairn(reparsed, "/opt/cairn/bin/cairn", "claude-desktop"); c {
		t.Fatal("second identical merge should be a no-op after JSON round-trip")
	}
}

// TEST: stale command path is detected via prevCmd and updated.
func TestMergeUpdatesStalePath(t *testing.T) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"cairn": map[string]any{"command": "/old/path/cairn", "args": []any{"mcp"}},
		},
	}
	changed, prev := MergeCairn(cfg, "/opt/cairn/bin/cairn", "claude-desktop")
	if !changed {
		t.Fatal("stale path should change")
	}
	if prev != "/old/path/cairn" {
		t.Fatalf("prevCmd = %q, want /old/path/cairn", prev)
	}
	cmd, _ := CairnCommand(cfg)
	if cmd != "/opt/cairn/bin/cairn" {
		t.Fatalf("command not updated: %q", cmd)
	}
}

// TEST: RemoveCairn removes only cairn.
func TestRemoveCairnOnly(t *testing.T) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"cairn":    map[string]any{"command": "x"},
			"linkedin": map[string]any{"command": "node"},
		},
	}
	if !RemoveCairn(cfg) {
		t.Fatal("expected removal")
	}
	servers := cfg["mcpServers"].(map[string]any)
	if _, ok := servers["cairn"]; ok {
		t.Error("cairn not removed")
	}
	if _, ok := servers["linkedin"]; !ok {
		t.Error("linkedin should survive")
	}
	if RemoveCairn(cfg) {
		t.Error("second removal should be a no-op")
	}
}

// TEST: malformed JSON is refused by ParseConfig.
func TestParseConfigRejectsMalformed(t *testing.T) {
	if _, err := ParseConfig([]byte("{not json")); err == nil {
		t.Fatal("malformed JSON should error")
	}
	if m, err := ParseConfig(nil); err != nil || len(m) != 0 {
		t.Fatalf("empty input should yield empty config, got %v %v", m, err)
	}
}

// ---- registry orchestration (file-merge path) ----

// TEST: install then idempotent second run = no change, correct message; and
// backup file is created before the (first, mutating) write.
func TestInstallIdempotentAndBackup(t *testing.T) {
	e := testEnv(t, false, nil) // desktop: no CLI
	app := desktopApp(t)
	path, _ := app.configPath(e)

	// seed an existing config with another server + unrelated setting
	writeJSON(t, path, map[string]any{
		"foo":        "bar",
		"mcpServers": map[string]any{"linkedin": map[string]any{"command": "node"}},
	})

	r, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Changed || r.Method != "file" {
		t.Fatalf("expected file change, got %+v", r)
	}
	if r.BackupPath == "" {
		t.Fatal("no backup path reported before write")
	}
	if _, err := os.Stat(r.BackupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	// backup holds the ORIGINAL (pre-write) content: no cairn entry
	if _, present := CairnCommand(readJSON(t, r.BackupPath)); present {
		t.Error("backup should predate the cairn entry")
	}
	// merged file preserves other server + unrelated setting
	merged := readJSON(t, path)
	if merged["foo"] != "bar" {
		t.Error("unrelated setting lost")
	}
	servers := merged["mcpServers"].(map[string]any)
	if _, ok := servers["linkedin"]; !ok {
		t.Error("linkedin lost")
	}
	if cmd, _ := CairnCommand(merged); cmd != e.Self {
		t.Errorf("cairn command = %q, want %q", cmd, e.Self)
	}

	// second run: idempotent
	r2, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if r2.Changed {
		t.Error("second identical install should not change")
	}
	if !strings.Contains(r2.Message, "already up to date") {
		t.Errorf("message = %q, want 'already up to date'", r2.Message)
	}
	if r2.BackupPath != "" {
		t.Error("no-op run should not create a backup")
	}
}

// TEST: a malformed existing config is refused, not clobbered, and backed up.
func TestInstallRefusesMalformed(t *testing.T) {
	e := testEnv(t, false, nil)
	app := desktopApp(t)
	path, _ := app.configPath(e)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{ this is : not json ")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := app.Install(e, "")
	if err == nil {
		t.Fatal("expected refusal on malformed config")
	}
	// original untouched
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Error("malformed config was clobbered")
	}
	// backed up
	if r.BackupPath == "" {
		t.Fatal("malformed config not backed up")
	}
	if b, _ := os.ReadFile(r.BackupPath); string(b) != string(original) {
		t.Error("backup does not match original malformed content")
	}
}

// TEST: stale command path gets updated to the current binary (file path).
func TestInstallFixesStalePath(t *testing.T) {
	e := testEnv(t, false, nil)
	app := desktopApp(t)
	path, _ := app.configPath(e)
	writeJSON(t, path, map[string]any{
		"mcpServers": map[string]any{
			"cairn": map[string]any{"command": "/stale/cairn", "args": []any{"mcp"}},
		},
	})
	r, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Changed {
		t.Fatal("stale path should be updated")
	}
	if r.PrevCmd != "/stale/cairn" {
		t.Errorf("PrevCmd = %q", r.PrevCmd)
	}
	if !strings.Contains(r.Message, "/stale/cairn") || !strings.Contains(r.Message, e.Self) {
		t.Errorf("message should report the stale fix: %q", r.Message)
	}
	if cmd, _ := CairnCommand(readJSON(t, path)); cmd != e.Self {
		t.Errorf("command not updated on disk: %q", cmd)
	}
}

// TEST: --list (Inspect) detects correctly and mutates nothing.
func TestInspectDetectsAndDoesNotMutate(t *testing.T) {
	e := testEnv(t, false, nil)
	app := desktopApp(t)
	path, _ := app.configPath(e)
	seed := map[string]any{
		"mcpServers": map[string]any{
			"cairn": map[string]any{"command": "/stale/cairn", "args": []any{"mcp"}},
		},
	}
	writeJSON(t, path, seed)
	before, _ := os.ReadFile(path)

	s := app.Inspect(e)
	if !s.Installed {
		t.Error("app dir/config present but reported not installed")
	}
	if !s.Configured {
		t.Error("cairn present but reported not configured")
	}
	if !s.Stale {
		t.Error("stale command not flagged")
	}
	if s.CurrentCmd != "/stale/cairn" {
		t.Errorf("CurrentCmd = %q", s.CurrentCmd)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("Inspect mutated the config file")
	}
}

// TEST: create-from-absent produces a schema-valid config for each app
// (file-merge path).
func TestInstallCreatesFromAbsent(t *testing.T) {
	for _, name := range []string{"claude-desktop", "claude-code"} {
		t.Run(name, func(t *testing.T) {
			e := testEnv(t, false, nil) // force file-merge fallback for claude-code
			app, _ := Lookup(name)
			path, _ := app.configPath(e)
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("precondition: config should be absent")
			}
			r, err := app.Install(e, "")
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			if !r.Changed || r.Method != "file" {
				t.Fatalf("expected fresh file write, got %+v", r)
			}
			if r.BackupPath != "" {
				t.Error("create-from-absent should not back up (nothing existed)")
			}
			cfg := readJSON(t, path) // fails the test if not valid JSON
			if cmd, present := CairnCommand(cfg); !present || cmd != e.Self {
				t.Errorf("fresh config missing correct cairn entry: %q %v", cmd, present)
			}
		})
	}
}

// ---- registry orchestration (CLI path for claude-code) ----

// TEST: when the claude CLI is available, claude-code writes are driven through
// it with the sanctioned args (never a hand-edited file).
func TestClaudeCodeDrivesCLI(t *testing.T) {
	var calls [][]string
	e := testEnv(t, true, &calls) // claude on PATH
	app := codeApp(t)

	// fresh install → single `claude mcp add ... -- <self> mcp`
	r, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if r.Method != "cli" {
		t.Fatalf("expected CLI method, got %q", r.Method)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 CLI call, got %d: %v", len(calls), calls)
	}
	want := []string{"claude", "mcp", "add", "cairn", "--scope", "user", "--", e.Self, "mcp", "--view", "claude-code", "--actor", "claude-code"}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("CLI args = %v, want %v", calls[0], want)
	}

	// Inspect (via CLI) reports the store path label.
	if s := app.Inspect(e); !s.ViaCLI || !strings.Contains(s.ConfigPath, "via CLI") {
		t.Errorf("expected via-CLI status, got %+v", s)
	}
}

// TEST: a stale claude-code entry is replaced via remove-then-add, and uninstall
// removes via the CLI.
func TestClaudeCodeStaleAndUninstallViaCLI(t *testing.T) {
	var calls [][]string
	e := testEnv(t, true, &calls)
	app := codeApp(t)
	path, _ := app.configPath(e)

	// seed a stale cairn entry in the real store file the CLI reads
	writeJSON(t, path, map[string]any{
		"mcpServers": map[string]any{
			"cairn":    map[string]any{"command": "/stale/cairn", "args": []any{"mcp"}},
			"linkedin": map[string]any{"command": "node"},
		},
	})

	r, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Changed || r.BackupPath == "" {
		t.Fatalf("stale replace should change and back up: %+v", r)
	}
	if len(calls) != 2 {
		t.Fatalf("expected remove+add, got %v", calls)
	}
	if calls[0][2] != "remove" || calls[1][2] != "add" {
		t.Fatalf("expected remove then add, got %v", calls)
	}

	// uninstall drives a single CLI remove
	calls = nil
	ur, err := app.Uninstall(e)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !ur.Changed || len(calls) != 1 || calls[0][2] != "remove" {
		t.Fatalf("uninstall should CLI-remove once: %+v %v", ur, calls)
	}
}

// ============================================================================
// FIX-MCP2: Codex CLI (TOML) — regression tests mirroring the JSON suite above.
// The merge invariants (preserve other servers + unrelated tables, idempotent,
// malformed-refused, stale-fixed, create-from-absent) must hold identically for
// the TOML codec and Codex's [mcp_servers.<name>] schema.
// ============================================================================

// codexEnv is a hermetic Env whose only recognized CLI is `codex`.
func codexEnv(t *testing.T, cliAvailable bool, calls *[][]string) Env {
	t.Helper()
	home := t.TempDir()
	return Env{
		Home: home,
		Cwd:  home,
		Self: "/opt/cairn/bin/cairn",
		LookPath: func(name string) (string, error) {
			if name == "codex" && cliAvailable {
				return "/opt/homebrew/bin/codex", nil
			}
			return "", os.ErrNotExist
		},
		Run: func(name string, args ...string) error {
			if calls != nil {
				*calls = append(*calls, append([]string{name}, args...))
			}
			return nil
		},
		Now: func() int64 { return 1700000000 },
	}
}

func codexApp(t *testing.T) App {
	t.Helper()
	a, ok := Lookup("codex")
	if !ok {
		t.Fatal("codex not registered")
	}
	return a
}

func writeRaw(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTOML(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseTOML(b)
	if err != nil {
		t.Fatalf("re-read config is not valid TOML: %v", err)
	}
	return m
}

// codexCmd reads the configured cairn command from a Codex (mcp_servers) config.
func codexCmd(cfg map[string]any) (string, bool) { return cairnCommand(cfg, "mcp_servers") }

// TEST: TOML merge preserves other MCP servers AND unrelated tables/settings.
func TestCodexInstallPreservesOtherTOML(t *testing.T) {
	e := codexEnv(t, false, nil) // no codex CLI → file-merge path
	app := codexApp(t)
	path, _ := app.configPath(e)
	writeRaw(t, path, `model = "gpt-5.5"
model_reasoning_effort = "medium"

[mcp_servers.existing]
command = "node"
args = ["srv.js"]

[projects."/home/x/proj"]
trust_level = "trusted"
`)

	r, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Changed || r.Method != "file" {
		t.Fatalf("expected file change, got %+v", r)
	}
	if r.BackupPath == "" {
		t.Fatal("no backup before mutating write")
	}

	merged := readTOML(t, path)
	if merged["model"] != "gpt-5.5" || merged["model_reasoning_effort"] != "medium" {
		t.Error("unrelated top-level settings lost")
	}
	if _, ok := merged["projects"].(map[string]any)["/home/x/proj"]; !ok {
		t.Error("unrelated [projects] table lost")
	}
	servers := merged["mcp_servers"].(map[string]any)
	if _, ok := servers["existing"]; !ok {
		t.Error("existing MCP server lost")
	}
	if cmd, present := codexCmd(merged); !present || cmd != e.Self {
		t.Errorf("cairn command = %q (present=%v), want %q", cmd, present, e.Self)
	}
}

// TEST: idempotent second install (TOML round-trip) = no change, no backup.
func TestCodexInstallIdempotent(t *testing.T) {
	e := codexEnv(t, false, nil)
	app := codexApp(t)

	if r, err := app.Install(e, ""); err != nil || !r.Changed {
		t.Fatalf("first install should change: %+v %v", r, err)
	}
	r2, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if r2.Changed {
		t.Error("second identical install should be a no-op after TOML round-trip")
	}
	if !strings.Contains(r2.Message, "already up to date") {
		t.Errorf("message = %q, want 'already up to date'", r2.Message)
	}
	if r2.BackupPath != "" {
		t.Error("no-op run should not create a backup")
	}
}

// TEST: malformed TOML is refused, not clobbered, and backed up.
func TestCodexInstallRefusesMalformedTOML(t *testing.T) {
	e := codexEnv(t, false, nil)
	app := codexApp(t)
	path, _ := app.configPath(e)
	original := "[mcp_servers.cairn\ncommand = " // unterminated table header
	writeRaw(t, path, original)

	r, err := app.Install(e, "")
	if err == nil {
		t.Fatal("expected refusal on malformed TOML")
	}
	if got, _ := os.ReadFile(path); string(got) != original {
		t.Error("malformed TOML was clobbered")
	}
	if r.BackupPath == "" {
		t.Fatal("malformed TOML not backed up")
	}
	if b, _ := os.ReadFile(r.BackupPath); string(b) != original {
		t.Error("backup does not match original malformed content")
	}
}

// TEST: a stale Codex command path is detected and updated (file path).
func TestCodexInstallFixesStalePath(t *testing.T) {
	e := codexEnv(t, false, nil)
	app := codexApp(t)
	path, _ := app.configPath(e)
	writeRaw(t, path, `[mcp_servers.cairn]
command = "/stale/cairn"
args = ["mcp"]
`)
	r, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Changed || r.PrevCmd != "/stale/cairn" {
		t.Fatalf("stale path not detected: %+v", r)
	}
	if !strings.Contains(r.Message, "/stale/cairn") || !strings.Contains(r.Message, e.Self) {
		t.Errorf("message should report the stale fix: %q", r.Message)
	}
	if cmd, _ := codexCmd(readTOML(t, path)); cmd != e.Self {
		t.Errorf("command not updated on disk: %q", cmd)
	}
}

// TEST: create-from-absent produces a schema-valid Codex TOML config.
func TestCodexInstallCreatesFromAbsent(t *testing.T) {
	e := codexEnv(t, false, nil)
	app := codexApp(t)
	path, _ := app.configPath(e)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("precondition: config should be absent")
	}
	r, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Changed || r.Method != "file" {
		t.Fatalf("expected fresh file write, got %+v", r)
	}
	if r.BackupPath != "" {
		t.Error("create-from-absent should not back up")
	}
	cfg := readTOML(t, path) // fails the test if not valid TOML
	if cmd, present := codexCmd(cfg); !present || cmd != e.Self {
		t.Errorf("fresh config missing correct cairn entry: %q %v", cmd, present)
	}
}

// TEST: when the codex CLI is on PATH, writes go through it with the sanctioned
// args (global add, no --scope), never a hand-edited file.
func TestCodexDrivesCLI(t *testing.T) {
	var calls [][]string
	e := codexEnv(t, true, &calls)
	app := codexApp(t)

	r, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if r.Method != "cli" {
		t.Fatalf("expected CLI method, got %q", r.Method)
	}
	want := []string{"codex", "mcp", "add", "cairn", "--", e.Self, "mcp", "--view", "codex", "--actor", "codex"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("CLI args = %v, want single %v", calls, want)
	}
	if s := app.Inspect(e); !s.ViaCLI || !strings.Contains(s.ConfigPath, "via CLI") {
		t.Errorf("expected via-CLI status, got %+v", s)
	}
}

// TEST: a stale Codex entry is replaced via remove-then-add, and uninstall
// removes via the CLI (no --scope).
func TestCodexStaleAndUninstallViaCLI(t *testing.T) {
	var calls [][]string
	e := codexEnv(t, true, &calls)
	app := codexApp(t)
	path, _ := app.configPath(e)
	writeRaw(t, path, `[mcp_servers.cairn]
command = "/stale/cairn"
args = ["mcp"]

[mcp_servers.existing]
command = "node"
`)
	r, err := app.Install(e, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Changed || r.BackupPath == "" {
		t.Fatalf("stale replace should change and back up: %+v", r)
	}
	if len(calls) != 2 || calls[0][2] != "remove" || calls[1][2] != "add" {
		t.Fatalf("expected remove then add, got %v", calls)
	}
	// no --scope in the Codex CLI vector
	for _, c := range calls {
		for _, arg := range c {
			if arg == "--scope" {
				t.Errorf("codex CLI must not use --scope: %v", c)
			}
		}
	}

	calls = nil
	ur, err := app.Uninstall(e)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !ur.Changed || len(calls) != 1 || calls[0][2] != "remove" {
		t.Fatalf("uninstall should CLI-remove once: %+v %v", ur, calls)
	}
}

// TEST: uninstall (file path) removes only cairn and preserves the rest.
func TestUninstallFilePreservesOthers(t *testing.T) {
	e := testEnv(t, false, nil)
	app := desktopApp(t)
	path, _ := app.configPath(e)
	writeJSON(t, path, map[string]any{
		"foo": "bar",
		"mcpServers": map[string]any{
			"cairn":    map[string]any{"command": e.Self, "args": []any{"mcp"}},
			"linkedin": map[string]any{"command": "node"},
		},
	})
	r, err := app.Uninstall(e)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !r.Changed || r.BackupPath == "" {
		t.Fatalf("expected change + backup: %+v", r)
	}
	cfg := readJSON(t, path)
	if cfg["foo"] != "bar" {
		t.Error("unrelated setting lost on uninstall")
	}
	servers := cfg["mcpServers"].(map[string]any)
	if _, ok := servers["cairn"]; ok {
		t.Error("cairn not removed")
	}
	if _, ok := servers["linkedin"]; !ok {
		t.Error("linkedin lost on uninstall")
	}
}
