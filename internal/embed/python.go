package embed

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ggoosen/cairn/internal/config"
)

// Python is the sanctioned real-model fallback (CLAUDE.md): a subprocess
// running sentence-transformers with the PINNED model inside an operator-
// provisioned venv at <portable>/.cairn/embed-venv (or $CAIRN_EMBED_PYTHON).
// Protocol: one JSON array of strings per request line on stdin, one JSON
// array of float arrays per response line on stdout.
type Python struct {
	cmd    *exec.Cmd
	stdin  *json.Encoder
	stdout *bufio.Scanner
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
	if env := os.Getenv("CAIRN_EMBED_PYTHON"); env != "" {
		return env
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
	cmd := exec.Command(interpreter, "-c", pythonWorker)
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
	p := &Python{cmd: cmd, stdin: json.NewEncoder(stdin), stdout: sc}
	// handshake: embed one empty batch to force model load errors now
	if _, err := p.Embed([]string{"handshake"}); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *Python) ModelID() string { return config.EmbeddingModelID }
func (p *Python) Dim() int        { return config.EmbeddingDim }

func (p *Python) Embed(texts []string) ([][]float32, error) {
	if err := p.stdin.Encode(texts); err != nil {
		return nil, err
	}
	if !p.stdout.Scan() {
		return nil, fmt.Errorf("embed worker died: %v", p.stdout.Err())
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

func (p *Python) Close() error {
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	return nil
}

// Detect picks the best available embedder for a cairn dir: the Python
// real-model worker when provisioned, else nil (lexical_only). Tests use
// BagOfWords explicitly.
func Detect(portableDir string) Embedder {
	e, _ := DetectVerbose(portableDir)
	return e
}

// DetectVerbose is Detect plus a human-readable reason when it returns nil
// (FIX-G5 / R45: a core subsystem that declines to start must say so, loudly,
// with the remedy — this feeds the daemon's startup log on every platform,
// macOS and Linux alike). A provisioned-but-broken venv surfaces its error
// instead of being swallowed. The reason is "" when a real embedder loaded.
func DetectVerbose(portableDir string) (Embedder, string) {
	interp := PythonInterpreter(portableDir)
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
