package embed

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

// Python is the sanctioned real-model fallback (CLAUDE.md): a subprocess
// running sentence-transformers with the PINNED model inside an operator-
// provisioned venv at <portable>/.cairn/embed-venv (or $CAIRN_EMBED_PYTHON).
// Protocol: one JSON array of strings per request line on stdin, one JSON
// array of float arrays per response line on stdout.
//
// The worker multiplexes ONE stdin encoder and ONE stdout scanner, so the
// whole request/response round-trip is serialized under mu: concurrent
// callers (search, digest, the background enricher, subscription
// calibration) would otherwise interleave request lines and read each
// other's vectors.
type Python struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdinRaw io.WriteCloser
	stdin    *json.Encoder
	stdout   *bufio.Scanner
	dead     bool
}

const pythonWorker = `
import json, sys
from sentence_transformers import SentenceTransformer
model = SentenceTransformer("` + config.EmbeddingModelID + `")
for line in sys.stdin:
    texts = json.loads(line)
    vecs = model.encode(texts, normalize_embeddings=True)
    sys.stdout.write(json.dumps([[float(x) for x in v] for v in vecs]) + "\n")
    sys.stdout.flush()
`

// PythonInterpreter locates the venv python for a cairn dir; "" if absent.
func PythonInterpreter(portableDir string) string {
	return PythonInterpreterConfigured(portableDir, "")
}

// PythonInterpreterConfigured resolves the interpreter with precedence
// env override > device-config embed_python (DEPLOY-E2: supervised
// daemons get no environment, so the TOML key is the durable knob) >
// the provisioned venv.
func PythonInterpreterConfigured(portableDir, configured string) string {
	if env := os.Getenv("CAIRN_EMBED_PYTHON"); env != "" {
		return env
	}
	if configured != "" {
		return configured
	}
	p := filepath.Join(portableDir, config.DerivedDirName, "embed-venv", "bin", "python3")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// NewPython starts the worker. Fails fast if the interpreter or the model
// is unavailable (caller degrades to lexical_only).
func NewPython(interpreter string) (*Python, error) {
	return newPythonWorker(interpreter, pythonWorker)
}

// newPythonWorker is NewPython with the worker source injectable (tests run
// a fake line-protocol worker; production always uses pythonWorker).
func newPythonWorker(interpreter, worker string) (*Python, error) {
	cmd := exec.Command(interpreter, "-c", worker)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting embed worker: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	p := &Python{cmd: cmd, stdinRaw: stdin, stdin: json.NewEncoder(stdin), stdout: sc}
	// handshake: embed one batch to force model-load errors now. First load
	// downloads/deserializes the model, so it gets the long timeout.
	if _, err := p.embed([]string{"handshake"}, config.EmbedHandshakeTimeout); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

func (p *Python) ModelID() string { return config.EmbeddingModelID }
func (p *Python) Dim() int        { return config.EmbeddingDim }

func (p *Python) Embed(texts []string) ([][]float32, error) {
	return p.embed(texts, config.EmbedRequestTimeout)
}

func (p *Python) embed(texts []string, timeout time.Duration) ([][]float32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return nil, errors.New("embed worker closed")
	}
	if err := p.stdin.Encode(texts); err != nil {
		return nil, err
	}
	// Scanner has no deadline: watchdog it. On timeout the worker is killed,
	// which unblocks the Scan, and the handle is poisoned — callers already
	// degrade to lexical_only on embed errors.
	done := make(chan bool, 1)
	go func() { done <- p.stdout.Scan() }()
	var scanned bool
	select {
	case scanned = <-done:
	case <-time.After(timeout):
		p.killLocked()
		<-done // Scan returns once the pipe closes; then the reap is safe
		_ = p.cmd.Wait()
		return nil, fmt.Errorf("embed worker timed out after %s (killed; retrieval degrades to lexical_only)", timeout)
	}
	if !scanned {
		err := p.stdout.Err()
		p.killLocked()
		_ = p.cmd.Wait()
		return nil, fmt.Errorf("embed worker died: %v", err)
	}
	var vecs [][]float32
	if err := json.Unmarshal(p.stdout.Bytes(), &vecs); err != nil {
		return nil, err
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("embed worker returned %d vectors for %d texts", len(vecs), len(texts))
	}
	return vecs, nil
}

// killLocked poisons the handle and signals the worker. Callers hold mu and
// are responsible for cmd.Wait() once any in-flight pipe read has returned.
func (p *Python) killLocked() {
	if p.dead {
		return
	}
	p.dead = true
	if p.stdinRaw != nil {
		p.stdinRaw.Close() // a well-behaved worker exits on stdin EOF
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *Python) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return nil
	}
	p.killLocked()
	_ = p.cmd.Wait() // reap: without this every closed worker is a zombie child
	return nil
}

// Detect picks the best available embedder for a cairn dir: the Python
// real-model worker when provisioned, else nil (lexical_only). Tests use
// BagOfWords explicitly.
func Detect(portableDir string) Embedder {
	e, _ := DetectVerbose(portableDir, "")
	return e
}

// DetectVerbose is Detect plus a human-readable reason when it returns nil
// (FIX-G5 / R45: a core subsystem that declines to start must say so, loudly,
// with the remedy — this feeds the daemon's startup log on every platform,
// macOS and Linux alike). A provisioned-but-broken venv surfaces its error
// instead of being swallowed. The reason is "" when a real embedder loaded.
func DetectVerbose(portableDir, configuredInterp string) (Embedder, string) {
	interp := PythonInterpreterConfigured(portableDir, configuredInterp)
	if interp == "" {
		return nil, "no embed venv found (semantic search disabled; retrieval is lexical-only). " +
			"Provision it with scripts/cairn-embed-bootstrap.sh, or point CAIRN_EMBED_PYTHON at a python with sentence-transformers, then `cairn reindex --semantic`."
	}
	p, err := NewPython(interp)
	if err != nil {
		return nil, fmt.Sprintf("embed venv at %s failed to start (%v); retrieval is lexical-only. "+
			"Re-provision with scripts/cairn-embed-bootstrap.sh, then `cairn reindex --semantic`.", interp, err)
	}
	return p, ""
}
