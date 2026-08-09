// Package merge implements the P0 body merge (rulings §8): UTF-8,
// LF-normalized, line-based diff3 with PINNED semantics — `git merge-file`
// (CLAUDE.md library table). Exit 0 = clean; positive = conflict markers in
// the output; the merged output is git merge-file's union of the operator
// edit and the current head relative to the export base.
package merge

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unicode/utf8"
)

// ErrInvalidUTF8: bodies must be valid UTF-8 (spec: text objects are UTF-8).
var ErrInvalidUTF8 = errors.New("body is not valid UTF-8")

// NormalizeLF converts CRLF/CR line endings to LF and validates UTF-8
// (rulings §8/§9: UTF-8 no BOM, LF).
func NormalizeLF(body []byte) ([]byte, error) {
	if !utf8.Valid(body) {
		return nil, ErrInvalidUTF8
	}
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF}) // strip BOM
	body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	body = bytes.ReplaceAll(body, []byte("\r"), []byte("\n"))
	return body, nil
}

// Result of a three-way merge.
type Result struct {
	Merged []byte // clean merge output, or conflict-marked output when !Clean
	Clean  bool
}

// Diff3 merges the operator edit and the current head relative to their
// common base: `git merge-file -p <operator> <base> <current>`. The merged
// output is the operator's version with current's changes folded in.
// Inputs must already be normalized (NormalizeLF).
func Diff3(base, current, operator []byte) (*Result, error) {
	dir, err := os.MkdirTemp("", "cairn-merge-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	paths := map[string][]byte{
		"OPERATOR": operator,
		"BASE":     base,
		"CURRENT":  current,
	}
	for name, content := range paths {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return nil, err
		}
	}

	cmd := exec.Command("git", "merge-file",
		"-p",
		"-L", "operator-edit", "-L", "export-base", "-L", "current-head",
		filepath.Join(dir, "OPERATOR"), filepath.Join(dir, "BASE"), filepath.Join(dir, "CURRENT"))
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err == nil {
		return &Result{Merged: out.Bytes(), Clean: true}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		// positive exit code = number of conflicts; output carries markers
		return &Result{Merged: out.Bytes(), Clean: false}, nil
	}
	return nil, fmt.Errorf("git merge-file failed (%v): %s — cairn requires git for the pinned diff3 semantics", err, stderr.String())
}
