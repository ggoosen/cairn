package backend

import (
	"strings"
	"unicode"
)

// Shared literal-matching used by the file-backed baselines (B1, B2).
//
// This is deliberately DUMB. The baselines model what a person or a shipping
// agent harness actually does with a pile of text — grep it — and a baseline
// that quietly acquired ranking would stop being the thing it is supposed to
// represent. Nothing here is tuned, and nothing here may be tuned against a
// corpus: BUILD-PLAN §3.7 forbids tuning on the evaluation set, and that
// prohibition binds the baselines as much as Cairn.
//
// The one concession to realism is the two-pass fallback in matchOrder: an
// agent that greps a phrase and gets nothing greps the individual terms next.
// Modelling only the first attempt would make B1 weaker than the real
// zero-effort baseline, and beating a strawman proves nothing.

// tokenize lowercases and splits on anything that is not a letter or digit.
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	seen := map[string]bool{}
	for _, f := range fields {
		if len(f) < 2 || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// containsAll / containsAny over a lowercased haystack.
func containsAll(hay string, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

func containsAny(hay string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(hay, t) {
			return true
		}
	}
	return false
}

// snippet returns up to max characters of body around the first term hit,
// so a baseline's payload holds readable context rather than a file offset.
// Character units are runes: a byte slice of UTF-8 can cut a rune in half,
// and the budget Cairn enforces is in Unicode scalars.
func snippet(body string, terms []string, max int) string {
	runes := []rune(body)
	if len(runes) <= max {
		return body
	}
	start := 0
	lower := strings.ToLower(body)
	for _, t := range terms {
		if idx := strings.Index(lower, t); idx >= 0 {
			// byte index → rune index
			start = len([]rune(body[:idx]))
			break
		}
	}
	start -= max / 4 // a little context before the match
	if start < 0 {
		start = 0
	}
	end := start + max
	if end > len(runes) {
		end = len(runes)
		start = end - max
	}
	return string(runes[start:end])
}

// truncateRunes caps a payload at a budget expressed in Unicode scalars,
// matching how Cairn counts budget_chars.
func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return s, false
	}
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	return string(r[:max]), true
}
