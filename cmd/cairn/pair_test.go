package main

// P3-2e: `cairn pair invite` mints a one-time token the new node can verify.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/identity"
)

func TestP32ePairInviteCLI(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir, "--display-name", "hub"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	rootKey := filepath.Join(loaded.DeviceDir, config.RootKeyName)
	tokenPath := filepath.Join(t.TempDir(), "invite.token")

	out, err := runCLI(t, "pair", "invite", "--dir", dir, "--name", "laptop", "--root-key", rootKey, "--out", tokenPath)
	if err != nil {
		t.Fatalf("pair invite: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pair join") || !strings.Contains(out, "SECRET") {
		t.Fatalf("invite output lacks the join instruction / secret warning:\n%s", out)
	}

	blob, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := identity.DecodeInvitation(string(blob))
	if err != nil {
		t.Fatalf("token does not decode: %v", err)
	}
	// the minted token verifies from genesis and carries a usable credential
	trust, priv, err := identity.VerifyPairingInvitation(inv, time.Now())
	if err != nil {
		t.Fatalf("minted token fails verification: %v", err)
	}
	if trust.CairnID != loaded.Portable.CairnID {
		t.Fatalf("token cairn_id %s != %s", trust.CairnID, loaded.Portable.CairnID)
	}
	if priv == nil || inv.Cert.DisplayName != "laptop" {
		t.Fatalf("token missing device key or name: %+v", inv.Cert)
	}
}

func TestP32eInvitationTokenRoundTripRejectsGarbage(t *testing.T) {
	if _, err := identity.DecodeInvitation("not-a-token"); err == nil {
		t.Fatal("decoded a non-token string")
	}
	if _, err := identity.DecodeInvitation("cairn-pair-v1.!!!notbase64!!!"); err == nil {
		t.Fatal("decoded an invalid-base64 token")
	}
}
