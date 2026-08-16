package main

// D5 acceptance (RULINGS R34, BUILD-PLAN §4 D5): merging an ad-hoc standalone
// mesh into a primary one ends with `cairn doctor` clean on BOTH origins and
// no event loss from either side.
//
// This drives the REAL script over two REAL daemons and the REAL binary,
// following TestBackupRestoreDrillScripts: the procedure is the deliverable,
// so testing a Go re-implementation of it would test the wrong thing. S1 and
// S2 both found defects that unit tests could not see, for the same reason.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/object"
)

type adoptMesh struct {
	dir  string
	proc *exec.Cmd
	log  *lockedBuffer
}

// startMeshDaemon inits a cairn dir and runs a real `cairn daemon` against it.
func startMeshDaemon(t *testing.T, bin, dir string, env []string) *adoptMesh {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "--dir", dir, "--sync-listen", "off")

	m := &adoptMesh{dir: dir, log: &lockedBuffer{}}
	m.proc = exec.Command(bin, "daemon", "--dir", dir)
	m.proc.Env = env
	m.proc.Stdout, m.proc.Stderr = m.log, m.log
	if err := m.proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = m.proc.Process.Kill()
		_ = m.proc.Wait()
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		cmd := exec.Command(bin, "status", "--dir", dir, "--json")
		cmd.Env = env
		if err := cmd.Run(); err == nil {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon for %s never became ready:\n%s", dir, m.log.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestD5AdoptStandaloneScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX shell")
	}
	bin := buildBinary(t)
	repoRoot, _ := filepath.Abs("../..")
	script := filepath.Join(repoRoot, "scripts", "cairn-adopt-standalone.sh")

	// A SHORT runtime dir: the unix socket path is derived from it, and a
	// deep t.TempDir() blows the 108-byte sun_path limit (the daemon then
	// fails to bind with a bare "invalid argument").
	runtimeDir, err := os.MkdirTemp("", "cd5")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })

	base := t.TempDir()
	env := append(os.Environ(),
		"CAIRN_DEVICE_STATE_DIR="+filepath.Join(base, "state"),
		"CAIRN_FAKE_VOLUME_STATUS=encrypted",
		"XDG_RUNTIME_DIR="+runtimeDir,
	)
	cli := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	mustCLI := func(args ...string) string {
		t.Helper()
		out, err := cli(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}

	// Two meshes, each with its own genesis and root — the situation R34
	// describes, not two devices of one mesh.
	primary := startMeshDaemon(t, bin, filepath.Join(base, "primary"), env)
	standalone := startMeshDaemon(t, bin, filepath.Join(base, "standalone"), env)

	mustCLI("send", "primary note: the roaster PID tuning was finalised in march", "--dir", primary.dir)
	mustCLI("send", "standalone A: espresso grinder burr replacement interval is 400kg",
		"--dir", standalone.dir, "--topic", "roastery/hardware")
	mustCLI("send", "standalone B: lease expiry is 2029-03-01, renewal notice due six months prior",
		"--dir", standalone.dir, "--topic", "legal")
	mustCLI("send", "standalone C: an untopiced note that must not be lost", "--dir", standalone.dir)
	var retract struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal([]byte(mustCLI("send", "standalone D: this one is retracted", "--dir", standalone.dir)), &retract); err != nil {
		t.Fatal(err)
	}
	mustCLI("retract", retract.MessageID, "--dir", standalone.dir)

	standaloneEventsBefore := doctorEventCount(t, mustCLI("doctor", "--dir", standalone.dir))

	// ---- the procedure itself -------------------------------------------
	cmd := exec.Command(script,
		"--standalone", standalone.dir, "--primary", primary.dir,
		"--cairn", bin, "--yes")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("adopt-standalone failed: %v\n%s", err, out)
	}
	transcript := string(out)
	if !strings.Contains(transcript, "ADOPTION COMPLETE") {
		t.Fatalf("procedure did not complete:\n%s", transcript)
	}
	// It must SAY what it does not preserve. A procedure that only advertises
	// its successes is how an operator loses something they cared about.
	for _, want := range []string{"PRESERVED:", "NOT PRESERVED:", "device clone", "R34"} {
		if !strings.Contains(transcript, want) && !strings.Contains(readFile(t, filepath.Join(standalone.dir, "RETIRED.md")), want) {
			t.Errorf("neither the transcript nor RETIRED.md mentions %q", want)
		}
	}

	// ---- no event loss from the STANDALONE side --------------------------
	// Its log is read, never appended to: same origin, same event count,
	// clean. (Only exports/ and RETIRED.md are written into that directory.)
	standaloneDoctor := mustCLI("doctor", "--dir", standalone.dir)
	if got := doctorEventCount(t, standaloneDoctor); got != standaloneEventsBefore {
		t.Fatalf("the standalone log changed during adoption: %d events before, %d after", standaloneEventsBefore, got)
	}
	if !strings.Contains(standaloneDoctor, "doctor: clean") ||
		!strings.Contains(standaloneDoctor, "deep doctor: clean") {
		t.Fatalf("standalone doctor is not clean:\n%s", standaloneDoctor)
	}

	// ---- no content loss into the PRIMARY --------------------------------
	// Every live standalone message, including the untopiced one, is findable
	// in the primary; the retracted one is NOT resurrected.
	for _, probe := range []struct {
		query string // what an agent would ask
		body  string // a contiguous fragment of the body it must return
		want  bool
	}{
		{"espresso grinder burr", "burr replacement interval is 400kg", true},
		{"lease expiry renewal", "lease expiry is 2029-03-01", true},
		{"untopiced note", "an untopiced note that must not be lost", true},
		{"retracted", "this one is retracted", false},
		{"roaster PID tuning", "roaster PID tuning was finalised", true}, // the primary's OWN message survives
	} {
		found := strings.Contains(mustCLI("search", probe.query, "--budget", "4000", "--dir", primary.dir), probe.body)
		if found != probe.want {
			t.Errorf("primary search %q for %q: found=%v want=%v", probe.query, probe.body, found, probe.want)
		}
	}

	// provenance: the adopted messages carry a source_ref naming the ORIGINAL
	// message id, which is what makes an adopted note traceable rather than
	// merely present.
	manifest := readCorpusManifest(t, transcript)
	if manifest.Messages != 3 {
		t.Fatalf("expected 3 live messages exported (the retracted one excluded), got %d", manifest.Messages)
	}
	for _, e := range manifest.Entries {
		if !strings.Contains(e.Path, e.MessageID) {
			t.Errorf("export path %q does not carry the original message id %s", e.Path, e.MessageID)
		}
		// VERBATIM: the exported file must hash to the message's own body
		// hash. Not decoration — the content hash is what makes a re-run a
		// no-op, and a single added byte of "helpful" metadata would both
		// break that and change what the adopting mesh stores.
		exported := readFile(t, filepath.Join(manifest.Root, filepath.FromSlash(e.Path)))
		if got := object.Hash([]byte(exported)); got != e.BodyHash {
			t.Errorf("exported %s is not the body verbatim: hash %s, want %s", e.Path, got, e.BodyHash)
		}
		out, err := cli("fetch", e.MessageID, "--dir", primary.dir)
		if err == nil {
			t.Errorf("the ORIGINAL message id %s resolves in the primary — adoption must create new ids, not reuse foreign ones:\n%s", e.MessageID, out)
		}
	}

	// ---- doctor clean on BOTH origins ------------------------------------
	primaryDoctor := mustCLI("doctor", "--dir", primary.dir)
	if !strings.Contains(primaryDoctor, "doctor: clean") ||
		!strings.Contains(primaryDoctor, "deep doctor: clean") {
		t.Fatalf("primary doctor is not clean:\n%s", primaryDoctor)
	}

	// ---- re-running is a no-op ------------------------------------------
	// A high-consequence procedure that cannot be repeated safely will be run
	// twice by somebody who is not sure it worked the first time.
	cmd = exec.Command(script,
		"--standalone", standalone.dir, "--primary", primary.dir,
		"--cairn", bin, "--yes")
	cmd.Env = env
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("re-run failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"published": 0`) || !strings.Contains(string(out), `"skipped": 3`) {
		t.Fatalf("re-running the adoption was not a no-op:\n%s", out)
	}

	// ---- the two refusals ------------------------------------------------
	// Adopting a mesh into ITSELF is not adoption; it must not be attempted.
	if out, err := runScript(script, env, "--standalone", primary.dir, "--primary", primary.dir, "--cairn", bin, "--yes"); err == nil {
		t.Fatalf("adopting a mesh into itself was accepted:\n%s", out)
	} else if !strings.Contains(out, "same mesh") {
		t.Fatalf("same-mesh refusal is unclear: %v\n%s", err, out)
	}
	// A directory that is not a cairn at all.
	if out, err := runScript(script, env, "--standalone", base, "--primary", primary.dir, "--cairn", bin, "--yes"); err == nil {
		t.Fatalf("a non-cairn directory was accepted as a standalone mesh:\n%s", out)
	} else if !strings.Contains(out, "not a cairn directory") {
		t.Fatalf("non-cairn refusal is unclear: %v\n%s", err, out)
	}
}

func runScript(script string, env []string, args ...string) (string, error) {
	cmd := exec.Command(script, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(blob)
}

// doctorEventCount pulls the event total out of doctor's origin line
// ("... N events, next seq M"), which is the cheapest whole-log fingerprint
// the CLI exposes.
func doctorEventCount(t *testing.T, doctorOut string) int {
	t.Helper()
	for _, line := range strings.Split(doctorOut, "\n") {
		i := strings.Index(line, " events,")
		if i < 0 {
			continue
		}
		fields := strings.Fields(line[:i])
		n := 0
		if _, err := fmt.Sscanf(fields[len(fields)-1], "%d", &n); err != nil {
			t.Fatalf("unparseable doctor line %q", line)
		}
		return n
	}
	t.Fatalf("no origin line in doctor output:\n%s", doctorOut)
	return 0
}

// readCorpusManifest loads the export manifest the transcript points at.
func readCorpusManifest(t *testing.T, transcript string) daemon.CorpusExport {
	t.Helper()
	root := ""
	for _, line := range strings.Split(transcript, "\n") {
		if strings.Contains(line, `"root":`) {
			root = strings.Trim(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]), `", `)
			break
		}
	}
	if root == "" {
		t.Fatalf("no export root in the transcript:\n%s", transcript)
	}
	var man daemon.CorpusExport
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, daemon.CorpusManifestName))), &man); err != nil {
		t.Fatalf("corpus manifest: %v", err)
	}
	return man
}
