package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/review"
)

func treeBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			m[p] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestReviewWorkflowDoesNotChangeMemory(t *testing.T) {
	dir := setupEnv(t)
	if out, e := runCLI(t, "init", "--dir", dir); e != nil {
		t.Fatalf("%s %v", out, e)
	}
	startTestDaemon(t, dir)
	out, e := runCLI(t, "send", "Build failed without sqlite_fts5.", "--dir", dir)
	if e != nil {
		t.Fatal(e)
	}
	var pub daemon.PublishResult
	if e = json.Unmarshal([]byte(out), &pub); e != nil {
		t.Fatal(e)
	}
	before := treeBytes(t, filepath.Join(dir, "events"))
	objects := treeBytes(t, filepath.Join(dir, "objects"))
	dest := t.TempDir()
	bundle := filepath.Join(dest, "bundle.json")
	out, e = runCLI(t, "review", "prepare", "--dir", dir, "--handoff", pub.MessageID, "--out", bundle)
	if e != nil {
		t.Fatalf("%s %v", out, e)
	}
	raw, e := os.ReadFile(bundle)
	if e != nil {
		t.Fatal(e)
	}
	var b review.Bundle
	if e = review.Decode(raw, &b); e != nil {
		t.Fatal(e)
	}
	r := review.Response{BundleHash: review.Hash(raw), Summary: "One build issue.", Proposals: []review.Proposal{{Title: "Build tag", Action: "add", VerificationStatus: "hypothesis", VerificationBasis: "Fix remains untested.", OutcomeEvidence: []review.Evidence{}, FutureTest: review.FutureTest{Trigger: "Next build", Procedure: "Use the tag", SuccessCriterion: "Build succeeds"}, Body: "Build with sqlite_fts5.", Reason: "Observed failed build.", Benefit: "Avoid repeats.", Audience: "Developers", Counterexamples: "Single note only.", Evidence: []review.Evidence{{ID: pub.MessageID, Quote: "Build failed without sqlite_fts5."}}}}}
	proposals := filepath.Join(dest, "proposals.json")
	data, _ := json.Marshal(r)
	if e = os.WriteFile(proposals, data, 0600); e != nil {
		t.Fatal(e)
	}
	packet := filepath.Join(dest, "packet.md")
	if out, e = runCLI(t, "review", "packet", "--dir", dir, "--bundle", bundle, "--proposals", proposals, "--out", packet); e != nil {
		t.Fatalf("%s %v", out, e)
	}
	md, _ := os.ReadFile(packet)
	if !strings.Contains(string(md), "PENDING HUMAN REVIEW") {
		t.Fatal(string(md))
	}
	for root, want := range map[string]map[string]string{"events": before, "objects": objects} {
		got := treeBytes(t, filepath.Join(dir, root))
		if len(got) != len(want) {
			t.Fatalf("%s changed", root)
		}
		for p, v := range want {
			if got[p] != v {
				t.Fatalf("changed %s", p)
			}
		}
	}
	if _, e = runCLI(t, "review", "packet", "--dir", dir, "--bundle", bundle, "--proposals", proposals, "--out", packet); e == nil {
		t.Fatal("overwrote packet")
	}
	if _, e = runCLI(t, "review", "apply"); e == nil {
		t.Fatal("apply exists")
	}
}
func TestReviewOutputBoundaries(t *testing.T) {
	mesh := t.TempDir()
	for _, path := range []string{filepath.Join(mesh, "new.json"), ""} {
		if reviewOutput(&mesh, path, []byte("x")) == nil {
			t.Fatal("accepted unsafe destination")
		}
	}
	outside := t.TempDir()
	alias := filepath.Join(outside, "alias")
	if e := os.Symlink(mesh, alias); e != nil {
		t.Fatal(e)
	}
	if reviewOutput(&mesh, filepath.Join(alias, "new.json"), []byte("x")) == nil {
		t.Fatal("accepted mesh symlink")
	}
	existing := filepath.Join(outside, "existing")
	if e := os.WriteFile(existing, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	if reviewOutput(&mesh, existing, []byte("overwrite")) == nil {
		t.Fatal("overwrote file")
	}
}
