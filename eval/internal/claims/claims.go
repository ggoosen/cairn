// Package claims reads eval/claims.yaml — the E1 register of every public
// claim, its threshold, and its kill criterion — and answers the one question
// the whole S4 sprint turns on: HAS THIS CLAIM'S KILL CRITERION BEEN SIGNED?
//
// Why the harness parses the register instead of hard-coding a flag. The rule
// (BUILD-PLAN §5-E1) is that apparatus may be built ahead of sign-off but no
// measurement may be reported as evidence before its kill criterion is signed.
// A boolean constant in Go would encode that rule at the moment someone typed
// it; reading the register makes the gate follow the operator's actual
// decisions, and makes flipping it a reviewable edit to a file whose whole
// purpose is to be reviewed. The register is the authority, not this code.
//
// THE PARSER IS DELIBERATELY STRICT AND DELIBERATELY TINY. eval/ has no
// external dependencies on purpose (see eval/go.mod), so there is no YAML
// library here, and there must not be one: pulling in a general parser to read
// one project-owned file would trade the module's offline-and-free property
// for convenience. Instead this reads the exact restricted subset claims.yaml
// is written in, and REFUSES anything it does not recognise — an unknown key,
// a stray indent, a claim missing a field. A lenient parser would silently
// drop a hand-edited signoff and leave the gate reading "pending" (or, far
// worse, "signed") for reasons nobody could see.
package claims

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PendingSignoff is the literal value a claim carries before an operator has
// accepted its kill criterion.
const PendingSignoff = "pending"

// Claim is one row of the register.
type Claim struct {
	ID            string `json:"id"`
	Claim         string `json:"claim"`
	Source        string `json:"source"`
	Class         string `json:"class"` // engineering | retrieval | product | safety
	Evidence      string `json:"evidence"`
	Measurement   string `json:"measurement"`
	Threshold     string `json:"threshold"`
	KillCriterion string `json:"kill_criterion"`
	Signoff       string `json:"signoff"` // "pending" | an ISO date
}

// Signed reports whether an operator has accepted this claim's kill criterion.
// A signoff is accepted only as an ISO date: "yes", "ok", "true" and the empty
// string are all refused, because a signoff that cannot be dated cannot be
// audited against the results it authorised.
func (c Claim) Signed() bool {
	s := strings.TrimSpace(c.Signoff)
	if s == "" || s == PendingSignoff {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// Register is the parsed claims.yaml.
type Register struct {
	Version      int
	RegisteredAt string
	Claims       []Claim

	byID map[string]Claim
}

// Get returns a claim by id.
func (r *Register) Get(id string) (Claim, bool) {
	c, ok := r.byID[id]
	return c, ok
}

// Signed reports whether every one of the given claim ids is signed off, and
// returns the ids that are not. An unknown id is reported as unsigned WITH a
// distinguishing reason rather than being skipped: a measurement that names a
// claim the register has never heard of is a bug in the measurement, and
// treating it as "nothing to check" would open the gate by typo.
func (r *Register) Signed(ids ...string) (bool, []string) {
	var blocked []string
	for _, id := range ids {
		c, ok := r.byID[id]
		switch {
		case !ok:
			blocked = append(blocked, id+" (NOT IN THE REGISTER)")
		case !c.Signed():
			blocked = append(blocked, id+" (signoff: "+c.Signoff+")")
		}
	}
	sort.Strings(blocked)
	return len(blocked) == 0, blocked
}

// Unsigned lists every claim still awaiting sign-off, in register order.
func (r *Register) Unsigned() []Claim {
	var out []Claim
	for _, c := range r.Claims {
		if !c.Signed() {
			out = append(out, c)
		}
	}
	return out
}

// Load reads and parses a claims register.
func Load(path string) (*Register, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// DefaultPath is the register's location relative to the eval module root.
const DefaultPath = "claims.yaml"

// known claim keys, in the order they appear in the register.
var claimFields = map[string]func(*Claim, string){
	"id":             func(c *Claim, v string) { c.ID = v },
	"claim":          func(c *Claim, v string) { c.Claim = v },
	"source":         func(c *Claim, v string) { c.Source = v },
	"class":          func(c *Claim, v string) { c.Class = v },
	"evidence":       func(c *Claim, v string) { c.Evidence = v },
	"measurement":    func(c *Claim, v string) { c.Measurement = v },
	"threshold":      func(c *Claim, v string) { c.Threshold = v },
	"kill_criterion": func(c *Claim, v string) { c.KillCriterion = v },
	"signoff":        func(c *Claim, v string) { c.Signoff = v },
}

// Parse reads the restricted subset claims.yaml is written in:
//
//	key: value                 at indent 0
//	claims:                    at indent 0, then a list of
//	  - key: value             at indent 2 (starts a claim)
//	    key: value             at indent 4 (continues it)
//
// plus blank lines and whole-line `#` comments. Anything else is an error.
func Parse(r io.Reader) (*Register, error) {
	reg := &Register{byID: map[string]Claim{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	inClaims := false
	var cur *Claim
	line := 0
	flush := func() error {
		if cur == nil {
			return nil
		}
		if cur.ID == "" || cur.KillCriterion == "" || cur.Signoff == "" {
			return fmt.Errorf("claim ending at line %d is missing id, kill_criterion or signoff — an unsignable row would silently never block anything", line)
		}
		if _, dup := reg.byID[cur.ID]; dup {
			return fmt.Errorf("duplicate claim id %q", cur.ID)
		}
		reg.Claims = append(reg.Claims, *cur)
		reg.byID[cur.ID] = *cur
		cur = nil
		return nil
	}

	for sc.Scan() {
		line++
		raw := strings.TrimRight(sc.Text(), " \t")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))

		switch {
		case indent == 0:
			if err := flush(); err != nil {
				return nil, err
			}
			key, val, err := splitKV(raw, line)
			if err != nil {
				return nil, err
			}
			switch key {
			case "version":
				n, err := strconv.Atoi(val)
				if err != nil {
					return nil, fmt.Errorf("line %d: version %q is not an integer", line, val)
				}
				reg.Version = n
			case "registered_at":
				reg.RegisteredAt = val
			case "claims":
				if val != "" {
					return nil, fmt.Errorf("line %d: `claims:` must introduce a list, not a value", line)
				}
				inClaims = true
			default:
				return nil, fmt.Errorf("line %d: unknown top-level key %q — this parser refuses what it does not understand rather than dropping it", line, key)
			}

		case indent == 2 && strings.HasPrefix(trimmed, "- "):
			if !inClaims {
				return nil, fmt.Errorf("line %d: list item outside `claims:`", line)
			}
			if err := flush(); err != nil {
				return nil, err
			}
			cur = &Claim{}
			if err := setField(cur, strings.TrimPrefix(trimmed, "- "), line); err != nil {
				return nil, err
			}

		case indent == 4 && cur != nil:
			if err := setField(cur, trimmed, line); err != nil {
				return nil, err
			}

		default:
			return nil, fmt.Errorf("line %d: unexpected indent %d (%q) — claims.yaml uses 0 / 2 / 4 only", line, indent, trimmed)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(reg.Claims) == 0 {
		return nil, fmt.Errorf("no claims parsed — an empty register would report every criterion as satisfied by vacuity")
	}
	return reg, nil
}

func setField(c *Claim, kv string, line int) error {
	key, val, err := splitKV(kv, line)
	if err != nil {
		return err
	}
	set, ok := claimFields[key]
	if !ok {
		return fmt.Errorf("line %d: unknown claim field %q", line, key)
	}
	set(c, val)
	return nil
}

func splitKV(s string, line int) (string, string, error) {
	s = strings.TrimSpace(s)
	i := strings.Index(s, ":")
	if i < 0 {
		return "", "", fmt.Errorf("line %d: %q is not `key: value`", line, s)
	}
	key := strings.TrimSpace(s[:i])
	val := strings.TrimSpace(s[i+1:])
	if strings.HasPrefix(val, `"`) {
		unq, err := strconv.Unquote(val)
		if err != nil {
			// The register's prose contains em-dashes, arrows and quotes; a
			// value strconv cannot unquote is a real syntax problem worth
			// reporting rather than guessing at.
			return "", "", fmt.Errorf("line %d: %s has a malformed quoted value: %w", line, key, err)
		}
		val = unq
	}
	return key, val, nil
}
