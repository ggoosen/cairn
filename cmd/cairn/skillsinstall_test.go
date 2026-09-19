package main

// D18 — the skills package. Three things are worth a test and nothing else is:
// that installation is idempotent and reversible against a REAL home
// directory, that a file cairn did not write is never touched, and — the one
// that matters — that a skill grants nothing (R21): a read-only session that
// follows the remember skill's own command still gets the daemon's refusal.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/identity"
	"github.com/ggoosen/cairn/internal/skillsinstall"
)

// skillsEnvFor builds an Env rooted at a temp HOME, with both harnesses'
// directories present so detection fires without a CLI on PATH.
func skillsEnvFor(t *testing.T) skillsinstall.Env {
	t.Helper()
	home := t.TempDir()
	for _, d := range []string{".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return skillsinstall.Env{
		Home:     home,
		Self:     "/usr/local/bin/cairn",
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		Now:      func() int64 { return 1 },
	}
}

func TestD18InstallIsIdempotentAndReversible(t *testing.T) {
	env := skillsEnvFor(t)
	for _, target := range skillsinstall.Registry() {
		st := target.Inspect(env, "")
		if !st.Installed {
			t.Fatalf("%s not detected with its dir present", target.Name)
		}
		first, err := target.Install(env, "")
		if err != nil {
			t.Fatalf("%s install: %v", target.Name, err)
		}
		for _, r := range first.Results {
			if !r.Changed {
				t.Errorf("%s/%s: first install changed nothing", target.Name, r.Unit)
			}
			if r.BackupPath != "" {
				t.Errorf("%s/%s: backed up a file that did not exist", target.Name, r.Unit)
			}
			if _, err := os.Stat(r.Path); err != nil {
				t.Errorf("%s/%s: not written: %v", target.Name, r.Unit, err)
			}
		}
		// R54 §4 applied to skills: a second identical run writes nothing.
		second, err := target.Install(env, "")
		if err != nil {
			t.Fatalf("%s reinstall: %v", target.Name, err)
		}
		for _, r := range second.Results {
			if r.Changed || r.BackupPath != "" {
				t.Errorf("%s/%s: second install was not a no-op (%s)", target.Name, r.Unit, r.Message)
			}
		}
		// every unit is reported current by the read-only status
		for _, sk := range target.Inspect(env, "").Skills {
			if !sk.Exists || !sk.Managed || !sk.UpToDate {
				t.Errorf("%s/%s: status after install = %+v", target.Name, sk.Unit, sk)
			}
			if sk.View != target.Name {
				t.Errorf("%s/%s: default view is %q, want the target's own name", target.Name, sk.Unit, sk.View)
			}
		}
		// reversible: removal leaves nothing of ours behind
		if _, err := target.Uninstall(env, ""); err != nil {
			t.Fatalf("%s uninstall: %v", target.Name, err)
		}
		for _, r := range first.Results {
			if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
				t.Errorf("%s/%s: survived uninstall", target.Name, r.Unit)
			}
		}
	}
}

func TestD18NeverClobbersAFileItDidNotWrite(t *testing.T) {
	env := skillsEnvFor(t)
	target, _ := skillsinstall.Lookup("codex")
	path := filepath.Join(env.Home, ".codex", "prompts", "cairn-recap.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const mine = "my own prompt, not cairn's\n"
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Install(env, ""); err == nil {
		t.Fatal("install did not report the unmanaged file")
	}
	if got, _ := os.ReadFile(path); string(got) != mine {
		t.Fatalf("unmanaged file was overwritten: %q", got)
	}
	if _, err := target.Uninstall(env, ""); err == nil {
		t.Fatal("uninstall did not report the unmanaged file")
	}
	if got, _ := os.ReadFile(path); string(got) != mine {
		t.Fatalf("unmanaged file was removed: %q", got)
	}
}

// A skill body must never teach a flag that widens what the session may do.
// This is the textual half of R21; TestD18SkillCannotEscalateCapability is the
// behavioural half.
func TestD18SkillsTeachNoEscalatingFlag(t *testing.T) {
	forbidden := []string{"--operator-override", "--force-class", "--durable", "--profile full", "CAIRN_SESSION="}
	for _, s := range skillsinstall.Skills("demo") {
		if s.Description == "" || s.Body == "" {
			t.Fatalf("skill %s is empty", s.Name)
		}
		for _, bad := range forbidden {
			if strings.Contains(s.Body, bad) {
				t.Errorf("skill %s teaches %q", s.Unit(), bad)
			}
		}
		// R18/R53: every skill states that mesh content is untrusted data.
		if !strings.Contains(s.Body, "untrusted") {
			t.Errorf("skill %s omits the untrusted-content rule", s.Unit())
		}
		if !strings.Contains(s.Body, "demo") {
			t.Errorf("skill %s does not name the view it was rendered for:\n%s", s.Unit(), s.Body)
		}
	}
}

// R21, behaviourally: a skill is a convenience over a verb, never a privilege.
// The remember skill tells the agent to run `cairn send`; run under a
// read-only session — which is what `cairn run --profile read-only` gives the
// agent's whole process tree — the daemon refuses it. Installing the skill
// changed nothing about what the session may do.
func TestD18SkillCannotEscalateCapability(t *testing.T) {
	dir := setupEnv(t)
	if out, err := runCLI(t, "init", "--dir", dir); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	startTestDaemon(t, dir)
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := daemon.Call(loaded.DeviceDir, daemon.Request{
		Op: "session-create", SessionProfile: "read-only", SessionName: "skilled"})
	if err != nil {
		t.Fatal(err)
	}
	token, _ := resp.Status["session"].(string)
	if token == "" {
		t.Fatal("no read-only handle minted")
	}

	// recall/recap: the read half a read-only session DOES have.
	if _, err := daemon.Call(loaded.DeviceDir, daemon.Request{
		Op: "digest", Session: token, AgentView: "skilled", BudgetChars: 500}); err != nil {
		t.Fatalf("read-only session cannot read: %v", err)
	}

	// remember/handoff: the write half it does NOT have. The skill names the
	// same verb the agent would have typed, so it gets the same refusal.
	_, err = daemon.Call(loaded.DeviceDir, daemon.Request{
		Op: "publish", Session: token,
		Publish: &daemon.PublishRequest{Actor: "skilled", Body: "written through a skill"}})
	if err == nil {
		t.Fatal("a read-only session published — a skill escalated capability")
	}
	if !strings.Contains(err.Error(), "capability") {
		t.Fatalf("refusal was not a capability refusal: %v", err)
	}
}
