package daemon

// FIX-G2 (R44/R45): the sync listener defaults to AUTO (detect a tailnet
// interface, bind <tailnet-ip>:9700), NEVER binds 0.0.0.0, can be disabled
// with "off", and NEVER declines silently — every non-listening outcome
// yields a loud, remedy-bearing reason string surfaced in `cairn sync status`.

import (
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
)

func TestG2ResolveSyncListen(t *testing.T) {
	found := func() (string, bool) { return "100.101.102.103", true }
	none := func() (string, bool) { return "", false }

	cases := []struct {
		name          string
		cfg           string
		detect        func() (string, bool)
		wantAddr      string
		wantReasonSub string // substring the reason must contain when addr==""
	}{
		{"off is a deliberate, explained disable", config.SyncListenOff, found, "", `"off"`},
		{"auto binds the detected tailnet ip:9700", config.SyncListenAuto, found, "100.101.102.103:9700", ""},
		{"empty defaults to auto", "", found, "100.101.102.103:9700", ""},
		{"auto with no tailnet is disabled but loud", config.SyncListenAuto, none, "", "no tailnet interface"},
		{"explicit tailnet address passes through", "100.64.1.2:9700", none, "100.64.1.2:9700", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, reason := resolveSyncListen(c.cfg, c.detect)
			if addr != c.wantAddr {
				t.Fatalf("addr = %q, want %q (reason=%q)", addr, c.wantAddr, reason)
			}
			// R44: the listener NEVER binds 0.0.0.0 / :: via the resolver.
			if strings.HasPrefix(addr, "0.0.0.0") || strings.HasPrefix(addr, "[::]") {
				t.Fatalf("resolver produced an unspecified bind address: %q", addr)
			}
			if addr == "" {
				// R45: a non-listening outcome must always explain itself.
				if reason == "" {
					t.Fatal("R45 VIOLATION: listener declined to start with an empty reason (silent)")
				}
				if c.wantReasonSub != "" && !strings.Contains(reason, c.wantReasonSub) {
					t.Fatalf("reason %q missing %q", reason, c.wantReasonSub)
				}
			} else if reason != "" {
				t.Fatalf("addr %q returned with a non-empty reason %q", addr, reason)
			}
		})
	}
}
