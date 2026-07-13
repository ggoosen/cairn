package main

// FIX-G4: the launchd plist and systemd unit are rendered from the resolved
// binary path + cairn dir. These unit tests pin the generated content (the
// install/uninstall side effects touch the real login session, so they are
// operator-verified per the runbook, not here).

import (
	"strings"
	"testing"
)

func TestG4LaunchdPlist(t *testing.T) {
	p := launchdPlist(serviceLabel, "/usr/local/bin/cairn", "/Users/op/cairn", "/Users/op/Library/Logs/cairn.log")
	for _, want := range []string{
		"<string>" + serviceLabel + "</string>",
		"<string>/usr/local/bin/cairn</string>",
		"<string>daemon</string>",
		"<string>--dir</string>",
		"<string>/Users/op/cairn</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"/Users/op/Library/Logs/cairn.log",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q\n%s", want, p)
		}
	}
	if !strings.HasPrefix(p, "<?xml") {
		t.Error("plist is not valid XML preamble")
	}
}

func TestG4SystemdUnit(t *testing.T) {
	u := systemdUnit("/home/op/.local/bin/cairn", "/home/op/cairn")
	for _, want := range []string{
		"ExecStart=/home/op/.local/bin/cairn daemon --dir /home/op/cairn",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q\n%s", want, u)
		}
	}
}
