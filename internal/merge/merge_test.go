package merge

import (
	"strings"
	"testing"
)

func TestNormalizeLF(t *testing.T) {
	got, err := NormalizeLF([]byte("\xEF\xBB\xBFa\r\nb\rc\n"))
	if err != nil || string(got) != "a\nb\nc\n" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := NormalizeLF([]byte{0xff, 0xfe, 0x00}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestDiff3CleanMerge(t *testing.T) {
	base := []byte("line one\nline two\nline three\n")
	current := []byte("line one CHANGED\nline two\nline three\n")  // head edited line 1
	operator := []byte("line one\nline two\nline three EDITED\n") // operator edited line 3

	res, err := Diff3(base, current, operator)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Clean {
		t.Fatalf("non-overlapping edits conflicted: %s", res.Merged)
	}
	want := "line one CHANGED\nline two\nline three EDITED\n"
	if string(res.Merged) != want {
		t.Fatalf("merged %q, want %q", res.Merged, want)
	}
}

func TestDiff3Conflict(t *testing.T) {
	base := []byte("greeting\n")
	current := []byte("hello from head\n")
	operator := []byte("hello from operator\n")

	res, err := Diff3(base, current, operator)
	if err != nil {
		t.Fatal(err)
	}
	if res.Clean {
		t.Fatalf("overlapping edits merged cleanly: %s", res.Merged)
	}
	out := string(res.Merged)
	for _, marker := range []string{"<<<<<<<", "=======", ">>>>>>>", "operator-edit", "current-head"} {
		if !strings.Contains(out, marker) {
			t.Fatalf("missing %q in conflict output:\n%s", marker, out)
		}
	}
}

func TestDiff3IdenticalEdits(t *testing.T) {
	base := []byte("a\n")
	same := []byte("b\n")
	res, err := Diff3(base, same, same)
	if err != nil || !res.Clean || string(res.Merged) != "b\n" {
		t.Fatalf("identical edits: %q clean=%v err=%v", res.Merged, res.Clean, err)
	}
}
