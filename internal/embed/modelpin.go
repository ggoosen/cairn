package embed

// P4-G2: model artifact pinning for the venv embedder. The bootstrap
// script downloads the model into <venv>/hf-cache and records
// <venv>/model.hash = "sha256:<hex>" over a deterministic walk. Before
// starting the worker, the daemon recomputes the walk: a mismatch means
// the model was swapped or tampered with since provisioning, and the
// embedder REFUSES (callers degrade to lexical-only with the loud R45
// warning) rather than silently embedding with something else.
//
// sha256 (not BLAKE3) deliberately: the pin is WRITTEN by the venv's own
// python at provision time (hashlib has no blake3), and this is a local
// integrity fingerprint — not mesh content addressing, where the BLAKE3
// rule applies. The worker also runs with HF_HOME pinned to the same
// cache so runtime and provision time read the same artifact.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	modelPinName  = "model.hash"
	modelCacheDir = "hf-cache"
)

// hashModelCache mirrors the bootstrap walk exactly: sorted relpaths,
// relpath + NUL + content per regular file, symlinks skipped.
func hashModelCache(cacheDir string) (string, error) {
	var paths []string
	err := filepath.Walk(cacheDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		rel, err := filepath.Rel(cacheDir, p)
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyModelPin checks the venv's recorded model pin against the cache
// on disk. A venv WITHOUT a pin passes (pre-G2 provisioning; the pin is
// recorded by the current bootstrap script) — a venv WITH a pin must
// match it exactly.
func VerifyModelPin(venvDir string) error {
	pinPath := filepath.Join(venvDir, modelPinName)
	blob, err := os.ReadFile(pinPath)
	if os.IsNotExist(err) {
		return nil // unpinned venv (older bootstrap): nothing to verify
	}
	if err != nil {
		return err
	}
	want := strings.TrimSpace(string(blob))
	got, err := hashModelCache(filepath.Join(venvDir, modelCacheDir))
	if err != nil {
		return fmt.Errorf("hashing model cache: %w", err)
	}
	if got != want {
		return fmt.Errorf("model artifact does not match its recorded pin (%s ≠ %s): the model was changed since provisioning — re-run scripts/cairn-embed-bootstrap.sh to re-provision and re-pin, then `cairn reindex --semantic`", got, want)
	}
	return nil
}
