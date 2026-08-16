package rank

// D4 — budget accounting. Rulings §7 ruled `budget_chars` only for P0 ("no
// bundled tokenizers"); this file adds `budget_tokens` beside it under the
// same hard-budget property: a budget covers the COMPLETE returned payload
// (header, metadata, quote prefixes, truncation marker included), and an
// item that does not fit is dropped WHOLE. Nothing is ever truncated
// mid-item, in either mode.
//
// Two invariants make the surface honest:
//
//  1. EXACTLY ONE budget per request. A request carrying both budget_chars
//     and budget_tokens is REFUSED (NewSpec), not silently resolved by a
//     precedence rule the caller cannot see.
//  2. Every budgeted response NAMES the tokenizer and the mode it used. A
//     token budget against an unnamed tokenizer means nothing, and the one
//     shipped token counter is an approximation whose NAME says so
//     (config.TokenizerApprox = "cairn-approx-v1").
//
// A capability grant (D3 max_budget_chars) is a second, independent ceiling
// rather than a conversion: Limits is a LIST, and every limit in it must
// hold simultaneously. That is why a token-budgeted request under a
// char-capped session stays honest — no char↔token exchange rate is ever
// invented.

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ggoosen/cairn/internal/config"
)

// Budget modes as reported to callers.
const (
	BudgetModeChars      = "chars"
	BudgetModeTokens     = "tokens"
	BudgetModeUnbudgeted = "unbudgeted"
)

// Counter counts one rendered payload in one named unit. The name is
// reported to the caller verbatim, so it must identify the counting rules
// exactly — swapping the rules means a new name.
type Counter interface {
	Name() string
	Count(s string) int
}

// BudgetChars counts Unicode scalar values (rulings §7: budget_chars is
// scalars, not bytes).
func BudgetChars(s string) int { return len([]rune(s)) }

type charCounter struct{}

func (charCounter) Name() string       { return config.TokenizerChars }
func (charCounter) Count(s string) int { return BudgetChars(s) }

// CharCounter counts Unicode scalar values. Exact, vocabulary-free.
func CharCounter() Counter { return charCounter{} }

type approxTokenCounter struct{}

func (approxTokenCounter) Name() string { return config.TokenizerApprox }

// Count applies the documented approximation (config.Tokenizer* constants).
// Deterministic, allocation-light, stdlib-only, and tuned to over-estimate
// typical prose so that a hard budget stays hard.
//
// Rules, applied to maximal runs:
//   - ASCII letters:      ceil(len / ApproxTokenLettersPerToken)
//   - ASCII digits:       ceil(len / ApproxTokenDigitsPerToken)
//   - newline:            1 each (a line break is its own token)
//   - other ASCII space:  len-1 (one leading space rides with the next word)
//   - other ASCII:        1 per rune (punctuation and symbols)
//   - non-ASCII:          ceil(utf8 bytes / ApproxTokenBytesPerNonASCII)
func (approxTokenCounter) Count(s string) int {
	total := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case isASCIILetter(c):
			n := 1
			for i+n < len(s) && isASCIILetter(s[i+n]) {
				n++
			}
			total += ceilDiv(n, config.ApproxTokenLettersPerToken)
			i += n

		case c >= '0' && c <= '9':
			n := 1
			for i+n < len(s) && s[i+n] >= '0' && s[i+n] <= '9' {
				n++
			}
			total += ceilDiv(n, config.ApproxTokenDigitsPerToken)
			i += n

		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f':
			n, newlines := 1, 0
			if c == '\n' {
				newlines++
			}
			for i+n < len(s) && isASCIISpace(s[i+n]) {
				if s[i+n] == '\n' {
					newlines++
				}
				n++
			}
			total += newlines
			if blank := n - newlines; blank > 1 {
				total += blank - 1
			}
			i += n

		case c < utf8.RuneSelf:
			total++ // punctuation, symbols
			i++

		default:
			n := 0
			for i+n < len(s) && s[i+n] >= utf8.RuneSelf {
				n++
			}
			total += ceilDiv(n, config.ApproxTokenBytesPerNonASCII)
			i += n
		}
	}
	return total
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// ApproxTokenCounter counts tokens by the documented approximation. It is
// NOT a BPE tokenizer and its Name() says so; see config.TokenizerApprox.
func ApproxTokenCounter() Counter { return approxTokenCounter{} }

// CounterFor returns the counter for a mode name (used when a stored or
// wire-carried mode has to be reconstituted).
func CounterFor(mode string) (Counter, error) {
	switch mode {
	case BudgetModeChars:
		return CharCounter(), nil
	case BudgetModeTokens:
		return ApproxTokenCounter(), nil
	}
	return nil, fmt.Errorf("unknown budget mode %q", mode)
}

// Spec is the caller's budget REQUEST: at most one of Chars/Tokens.
// Ceiling is a second, independently-enforced maximum in CHARACTERS, set
// only by the capability gate (D3 max_budget_chars) — never by a client.
type Spec struct {
	Chars   int
	Tokens  int
	Ceiling int
}

// NewSpec validates a caller's budget request. Both budgets at once is a
// REFUSAL: resolving it by precedence would mean one of the two numbers the
// caller wrote was silently ignored.
func NewSpec(chars, tokens int) (Spec, error) {
	if chars > 0 && tokens > 0 {
		return Spec{}, fmt.Errorf("budget_chars and budget_tokens are mutually exclusive (got %d and %d) — pass exactly one", chars, tokens)
	}
	if chars < 0 || tokens < 0 {
		return Spec{}, fmt.Errorf("budgets must not be negative (budget_chars=%d budget_tokens=%d)", chars, tokens)
	}
	return Spec{Chars: chars, Tokens: tokens}, nil
}

// Bounded reports whether the caller asked for any budget at all.
func (s Spec) Bounded() bool { return s.Chars > 0 || s.Tokens > 0 }

// Mode is the caller-facing mode name.
func (s Spec) Mode() string {
	switch {
	case s.Tokens > 0:
		return BudgetModeTokens
	case s.Chars > 0:
		return BudgetModeChars
	}
	return BudgetModeUnbudgeted
}

// Limit is one hard maximum in one named unit.
type Limit struct {
	Mode string
	Max  int
	c    Counter
}

// Limits is a conjunction: a payload complies only if EVERY limit holds.
// A nil/empty Limits is unbudgeted.
type Limits []Limit

// Limits resolves the spec (plus any capability ceiling) into the hard
// limits the renderers enforce. The caller's budget comes first; the
// capability ceiling, when present and not already implied, is appended.
func (s Spec) Limits() Limits {
	var out Limits
	switch {
	case s.Tokens > 0:
		out = append(out, Limit{Mode: BudgetModeTokens, Max: s.Tokens, c: ApproxTokenCounter()})
	case s.Chars > 0:
		out = append(out, Limit{Mode: BudgetModeChars, Max: s.Chars, c: CharCounter()})
	}
	if s.Ceiling > 0 && (s.Chars <= 0 || s.Chars > s.Ceiling) {
		out = append(out, Limit{Mode: BudgetModeChars, Max: s.Ceiling, c: CharCounter()})
	}
	return out
}

// Report is what a budgeted response tells the caller about its own budget:
// the mode, the limit, the tokenizer that counted it, and what the returned
// payload actually cost in that same unit. An agent can verify compliance
// from the report alone.
type Report struct {
	Mode      string `json:"budget_mode"`            // chars | tokens | unbudgeted
	Limit     int    `json:"budget_limit,omitempty"` // in Mode's unit
	Tokenizer string `json:"tokenizer"`              // the NAMED counter
	Used      int    `json:"budget_used"`            // payload cost in Mode's unit
	// CeilingChars is a capability ceiling (D3) that also bound this payload,
	// reported whenever it is not itself the caller's budget.
	CeilingChars int `json:"budget_ceiling_chars,omitempty"`
	// Approximate is true when Tokenizer is not exact, so a caller never has
	// to recognise the name to know it is dealing with an estimate.
	Approximate bool `json:"approximate,omitempty"`
}

// Report describes a rendered payload against this spec.
func (s Spec) Report(payload string) Report {
	r := Report{Mode: s.Mode()}
	switch r.Mode {
	case BudgetModeTokens:
		c := ApproxTokenCounter()
		r.Limit, r.Tokenizer, r.Used, r.Approximate = s.Tokens, c.Name(), c.Count(payload), true
	case BudgetModeChars:
		c := CharCounter()
		r.Limit, r.Tokenizer, r.Used = s.Chars, c.Name(), c.Count(payload)
	default:
		c := CharCounter()
		r.Tokenizer, r.Used = c.Name(), c.Count(payload)
	}
	if s.Ceiling > 0 && r.Mode != BudgetModeChars {
		r.CeilingChars = s.Ceiling
	}
	return r
}

// BudgetRender carries the payload parts that are counted alongside items.
type BudgetRender struct {
	Header string // counted always
	Marker string // appended when truncated; counted when present
}

// TakeWithinBudget renders items one at a time via render(i) and returns the
// largest PREFIX whose complete payload (header + items + the truncation
// marker when items were dropped) satisfies EVERY limit. Items are dropped
// whole; nothing is ever cut mid-item. render must be deterministic.
//
// Empty limits means unbudgeted: everything is rendered and no marker is
// appended. A limit whose Max is not positive admits nothing — that is the
// "budget too small for even the header" case, and it returns the empty
// payload rather than one character over.
func TakeWithinBudget(n int, limits Limits, br BudgetRender, render func(i int) string) (included int, payload string) {
	if len(limits) == 0 {
		var b strings.Builder
		b.WriteString(br.Header)
		for i := 0; i < n; i++ {
			b.WriteString(render(i))
		}
		return n, b.String()
	}
	for _, l := range limits {
		if l.Max <= 0 || l.c.Count(br.Header) > l.Max {
			return 0, ""
		}
	}
	// running cost per limit, so each item is counted once per limit
	totals := make([]int, len(limits))
	markers := make([]int, len(limits))
	for i, l := range limits {
		totals[i] = l.c.Count(br.Header)
		markers[i] = l.c.Count(br.Marker)
	}
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		piece := render(i)
		fits := true
		costs := make([]int, len(limits))
		for j, l := range limits {
			costs[j] = l.c.Count(piece)
			marker := 0
			if i < n-1 {
				marker = markers[j] // more items remain: the marker will be needed
			}
			if totals[j]+costs[j]+marker > l.Max {
				fits = false
				break
			}
		}
		if !fits {
			break
		}
		for j := range limits {
			totals[j] += costs[j]
		}
		parts = append(parts, piece)
	}
	included = len(parts)
	var b strings.Builder
	b.WriteString(br.Header)
	for _, p := range parts {
		b.WriteString(p)
	}
	out := b.String()
	if included < n {
		// the marker is part of the budgeted payload too: append it only if it
		// fits EVERY limit (a budget too small for even the marker returns the
		// bare header — never a single unit over)
		withMarker := out + br.Marker
		ok := true
		for _, l := range limits {
			if l.c.Count(withMarker) > l.Max {
				ok = false
				break
			}
		}
		if ok {
			out = withMarker
		}
	}
	return included, out
}

// Complies reports whether a payload satisfies every limit — the property
// the budget tests assert, expressed once.
func (ls Limits) Complies(payload string) error {
	for _, l := range ls {
		if got := l.c.Count(payload); got > l.Max {
			return fmt.Errorf("payload is %d %s, budget %d", got, l.Mode, l.Max)
		}
	}
	return nil
}
