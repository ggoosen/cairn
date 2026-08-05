package main

// FIX-G4 (F10): `cairn daemon --install` / `--uninstall` register the resident
// daemon as a user service — launchd (macOS, primary) or a systemd --user unit
// (Linux, best-effort; WSL needs systemd enabled, otherwise we write the unit
// and tell the operator to start the daemon manually). Never a system-wide/root
// service: cairn is single-writer per user, device-local.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const serviceLabel = "com.cairn.daemon"

// daemonExecPath resolves the absolute, symlink-free path of this binary so the
// service points at a stable location (not a $PATH lookup at boot).
func daemonExecPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		return resolved, nil
	}
	return p, nil
}

func installService(dir string, out io.Writer) error {
	exe, err := daemonExecPath()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(exe, dir, out)
	case "linux":
		return installSystemd(exe, dir, out)
	default:
		return fmt.Errorf("service install is not supported on %s (macOS launchd / Linux systemd only) — run `cairn daemon` directly", runtime.GOOS)
	}
}

func uninstallService(out io.Writer) error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd(out)
	case "linux":
		return uninstallSystemd(out)
	default:
		return fmt.Errorf("service uninstall is not supported on %s", runtime.GOOS)
	}
}

// ---- macOS: launchd user agent ----

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"), nil
}

func launchdLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "cairn.log"), nil
}

// launchdPlist renders the user LaunchAgent (pure — unit-tested). RunAtLoad +
// KeepAlive make it resident; the daemon self-guards single-writer via its lock.
func launchdPlist(label, exe, dir, logPath string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + exe + `</string>
		<string>daemon</string>
		<string>--dir</string>
		<string>` + dir + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>` + logPath + `</string>
	<key>StandardErrorPath</key>
	<string>` + logPath + `</string>
	<key>ProcessType</key>
	<string>Background</string>
</dict>
</plist>
`
}

func installLaunchd(exe, dir string, out io.Writer) error {
	plistPath, err := launchAgentPath()
	if err != nil {
		return err
	}
	logPath, err := launchdLogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, []byte(launchdPlist(serviceLabel, exe, dir, logPath)), 0o644); err != nil {
		return err
	}
	// reload cleanly: unload an existing instance (ignore errors), then load.
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if o, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load %s: %v: %s", plistPath, err, strings.TrimSpace(string(o)))
	}
	fmt.Fprintf(out, "installed launchd agent %s\n  plist: %s\n  logs:  %s\n  stop/remove: cairn daemon --uninstall\n", serviceLabel, plistPath, logPath)
	return nil
}

func uninstallLaunchd(out io.Writer) error {
	plistPath, err := launchAgentPath()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Fprintf(out, "uninstalled launchd agent %s (removed %s)\n", serviceLabel, plistPath)
	return nil
}

// ---- Linux: systemd --user unit ----

func systemdUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", "cairn.service"), nil
}

// systemdUnit renders the user unit (pure — unit-tested).
func systemdUnit(exe, dir string) string {
	return `[Unit]
Description=cairn local-first message & knowledge daemon
After=network.target

[Service]
Type=simple
ExecStart=` + exe + ` daemon --dir ` + dir + `
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`
}

// systemdAvailable reports whether `systemctl --user` is usable. Under WSL
// without systemd enabled it is not — we still write the unit but tell the
// operator to start the daemon manually.
func systemdAvailable() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "--user", "show-environment").Run() == nil
}

func isWSL() bool {
	b, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	v := strings.ToLower(string(b))
	return strings.Contains(v, "microsoft") || strings.Contains(v, "wsl")
}

func installSystemd(exe, dir string, out io.Writer) error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnit(exe, dir)), 0o644); err != nil {
		return err
	}
	if !systemdAvailable() {
		caveat := ""
		if isWSL() {
			caveat = " (WSL: enable systemd in /etc/wsl.conf → [boot] systemd=true, or start the daemon manually)"
		}
		fmt.Fprintf(out, "wrote systemd unit %s, but `systemctl --user` is unavailable%s.\nStart the daemon manually with `cairn daemon`.\n", unitPath, caveat)
		return nil
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if o, err := exec.Command("systemctl", "--user", "enable", "--now", "cairn.service").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user enable --now cairn.service: %v: %s", err, strings.TrimSpace(string(o)))
	}
	fmt.Fprintf(out, "installed + started systemd --user unit\n  unit: %s\n  logs: journalctl --user -u cairn\n  stop/remove: cairn daemon --uninstall\n", unitPath)
	return nil
}

func uninstallSystemd(out io.Writer) error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if systemdAvailable() {
		_ = exec.Command("systemctl", "--user", "disable", "--now", "cairn.service").Run()
		defer func() { _ = exec.Command("systemctl", "--user", "daemon-reload").Run() }()
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Fprintf(out, "uninstalled systemd --user unit (removed %s)\n", unitPath)
	return nil
}
