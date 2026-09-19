package daemon

// D3 unit drills on the selector primitives. The acceptance test
// (confine_test.go) proves enforcement end-to-end over real IPC; these pin the
// glob semantics and the mint-time validation, which are the two places a
// mistake would silently WIDEN a grant.

import "testing"

func TestD3GlobSemantics(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
		why           string
	}{
		{"a/*", "a/b", true, "direct child"},
		{"a/*", "a/b/c", true, "`*` spans `/` — the whole subtree, which is what §7.2's example means"},
		{"a/*", "a", false, "the bare parent is NOT the subtree; write both if you mean both"},
		{"a/*", "ab/c", false, "prefix must respect the separator"},
		{"a/*", "z/a/b", false, "no floating match"},
		{"project/x/*", "project/x/api/v1", true, "the case path.Match would have missed"},
		{"project/x/*", "project/y/api", false, "sibling subtree"},
		{"a", "a", true, "a selector with no wildcard is an exact topic"},
		{"a", "a/b", false, "an exact selector is not a prefix"},
		{"*", "anything/at/all", true, "a bare star is everything"},
		{"*/notes", "team/notes", true, "leading wildcard"},
		{"*/notes", "team/sub/notes", true, "leading wildcard spans separators too"},
		{"*/notes", "team/notes/old", false, "suffix must be the end"},
		{"a/*/z", "a/b/z", true, "middle wildcard"},
		{"a/*/z", "a/b/c/z", true, "middle wildcard spans separators"},
		{"a/*/z", "a/b/zz", false, "suffix is anchored"},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.name); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v — %s", c.pattern, c.name, got, c.want, c.why)
		}
	}
}

func TestD3SelectorValidation(t *testing.T) {
	ok := []string{"a", "a/*", "*", "project/x/*", "a-b_c/*", "a/*/z", "0/1"}
	for _, s := range ok {
		if err := (Selectors{Topics: []string{s}}).Validate(); err != nil {
			t.Errorf("selector %q rejected: %v", s, err)
		}
	}
	// Negation, traversal, uppercase and whitespace are all outside the topic
	// charset. There is deliberately NO syntax for "everything except" — mutes
	// are D7's open ruling and this must not pre-empt it.
	bad := []string{"", "!a", "-a/*", "^a", "a/../b", "..", "A/*", "a b", "a|b", "a\n"}
	for _, s := range bad {
		if err := (Selectors{Topics: []string{s}}).Validate(); err == nil {
			t.Errorf("selector %q accepted — a malformed or negative grant must be refused at mint", s)
		}
	}
	if err := (Selectors{MaxBudgetChars: -1}).Validate(); err == nil {
		t.Error("a negative budget cap was accepted")
	}
	if !(Selectors{}).Empty() {
		t.Error("zero selectors must confine nothing (they are opt-in)")
	}
}

// TestD3EveryReachableOpIsClassified: the confinement table's zero value is
// REFUSE, so an op nobody classified is unreachable from a confined session.
// This test states the intent out loud and pins the classification of the ops
// an agent profile can actually reach, so a future op silently landing in
// "refuse" is a deliberate default rather than an accident nobody sees.
func TestD3ConfinementClassification(t *testing.T) {
	for op, want := range map[string]confineMode{
		"search": confineScoped, "digest": confineScoped, "thread": confineScoped,
		"fetch": confineResource, "peek": confineResource, "why-ranked": confineResource,
		"publish": confinePublish, "status": confineOpen,
		// whole-mesh renderings and structural ops: refused (the zero value)
		"map": confineRefuse, "compact": confineRefuse, "link": confineRefuse,
		"export": confineRefuse, "source-ref": confineRefuse, "sync-now": confineRefuse,
		"an-op-nobody-has-written-yet": confineRefuse,
	} {
		if got := opConfinement[op]; got != want {
			t.Errorf("op %q classified %v, want %v", op, got, want)
		}
	}
	// every op the action tiers let an agent-standard session reach must be
	// classified deliberately — this is the list to revisit when adding ops
	for op, capa := range opCapability {
		if capa == capAdmin {
			continue
		}
		if _, ok := opConfinement[op]; !ok {
			t.Errorf("op %q is reachable by a non-admin profile but has no confinement classification (it defaults to REFUSED — decide that on purpose)", op)
		}
	}
}
