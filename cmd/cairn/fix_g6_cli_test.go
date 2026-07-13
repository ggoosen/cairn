package main

// FIX-G6: attachments are STREAMED over IPC (not inlined in the publish JSON),
// with a client-side size pre-check. Decision (work order): the real ceiling is
// 16 MiB (MaxRecordBytes / DeriveMaxBytes); an attachment over it fails cleanly
// BEFORE transmission instead of `broken pipe` from breaching IPCMaxRequestBytes.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
)

func TestG6AttachmentStreamedAndCapEnforced(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)

	// (1) a normal attachment streams fine and the message is searchable.
	small := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(small, []byte("zzattachcanary the streamed attachment body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// send success proves the full path: client stat/stream → daemon
	// stage-attachment (Put) → publish referencing the object by hash. A broken
	// link would surface as "references unstaged object".
	out, err := runCLI(t, "send", "message with a streamed attachment", "--dir", dir, "--attach", small)
	if err != nil {
		t.Fatalf("send --attach: %v\n%s", err, out)
	}
	if !strings.Contains(out, "message_id") {
		t.Fatalf("send --attach produced no publish result:\n%s", out)
	}

	// (2) an attachment over the 16 MiB ceiling fails CLEANLY before any
	// transmission — the clean cap message, never `broken pipe`.
	big := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(big, make([]byte, config.DeriveMaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, "send", "oversized attachment", "--dir", dir, "--attach", big)
	if err == nil {
		t.Fatalf("oversized attachment accepted:\n%s", out)
	}
	msg := out + " " + err.Error()
	if strings.Contains(msg, "broken pipe") {
		t.Fatalf("G6 VIOLATION: oversized attachment failed with `broken pipe` (transmitted before the size check): %s", msg)
	}
	if !strings.Contains(msg, "16 MiB") && !strings.Contains(msg, "cap") {
		t.Fatalf("oversized attachment did not fail with the clean cap message: %s", msg)
	}
}
