package daemon

// D3 — capability resource selectors (spec §7.2; BUILD-PLAN §4 D3).
//
// P0/P1 shipped the coarse action tiers only: a session could be denied
// WRITING but not confined to a SUBTREE, which is the grant an operator
// actually wants for a narrow agent ("this one works on project/x and sees
// nothing else"). Spec §7.2 always described selectors; this is them, for the
// two the plan names — topic globs and a per-session budget cap.
//
// FOUR PROPERTIES, all deliberate.
//
//  1. POSITIVE GRANTS ONLY. Spec §7.2 is explicit ("no negative labels —
//     deny/allow precedence ambiguity eliminated"), and mutes are D7's open
//     ruling. There is no way to express "everything except", and adding one
//     here would decide a question reserved for the author.
//
//  2. ENFORCEMENT IS SINGULAR. Every decision — refuse, clamp, scope — is made
//     here and called from ONE place: the IPC dispatch boundary, immediately
//     after the action-tier check. Handlers receive an already-scoped request;
//     they never consult the session. A second enforcement point is a second
//     thing to forget.
//
//  3. UNCLASSIFIED OPS ARE REFUSED. `opConfinement` classifies every op a
//     confined session may reach; anything absent is refused, exactly as
//     `capabilityFor` treats an unknown op as admin. A new op is therefore
//     unreachable from a confined session until someone decides what
//     confinement means for it, rather than silently exempt.
//
//  4. REFUSALS ARE TYPED, NEVER EMPTY RESULTS. An agent has to be able to tell
//     "nothing matched" from "you may not ask", so an out-of-scope request
//     returns a `Refusal` with a stable code — never zero rows. The one place
//     an empty result IS correct is a grant that matches no existing topic
//     yet: nothing is being withheld, there is genuinely nothing there.
//
// TOPIC GLOBS RESOLVE THROUGH THE EXISTING TOPIC TABLE. A selector is matched
// against `TopicList()` and the resulting NAMES are handed to the existing
// scope path (`ScopeMessageIDs`) that `cairn search --topic` already uses.
// There is no second resolver and no second notion of what a topic is. The
// resolution is redone per request, so a topic created after the handle was
// minted falls inside the grant without re-minting.
//
// GLOB SEMANTICS: `*` matches any run of characters INCLUDING `/`, so
// `project/x/*` is the subtree, which is what §7.2's example means and what
// path.Match would not give (its `*` stops at a separator, making
// `project/x/*` miss `project/x/api/v1` — a grant that silently excludes most
// of the subtree it names is worse than no grant). The bare parent is NOT
// matched by `a/*`: an operator who wants the parent topic too writes both
// `a` and `a/*`. Widening a written grant is not this code's decision to make.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ggoosen/cairn/internal/event"
)

// Selectors is the optional resource scoping attached to a capability handle
// (spec §7.2 `resource_selectors` + the `max_digest_chars` constraint).
type Selectors struct {
	// Topics are positive topic globs. Empty = no topic confinement.
	Topics []string `json:"topics,omitempty"`
	// MaxBudgetChars caps budget_chars on every budgeted retrieval. 0 = uncapped.
	MaxBudgetChars int `json:"max_budget_chars,omitempty"`
}

// Empty reports whether the selectors confine nothing.
func (s Selectors) Empty() bool { return len(s.Topics) == 0 && s.MaxBudgetChars <= 0 }

// Validate checks selectors at MINT time, so a malformed grant is refused when
// the operator writes it rather than misinterpreted on every later request.
func (s Selectors) Validate() error {
	for _, g := range s.Topics {
		if g == "" {
			return fmt.Errorf("empty topic selector")
		}
		if strings.Contains(g, "..") || !event.ValidTopicSelector(g) {
			return fmt.Errorf("invalid topic selector %q (topic-name charset plus `*`: %s)", g, event.TopicSelectorPattern)
		}
	}
	if s.MaxBudgetChars < 0 {
		return fmt.Errorf("max_budget_chars must not be negative")
	}
	return nil
}

// globMatch matches one selector against one topic name. `*` spans everything,
// including `/` (see the file header for why).
func globMatch(pattern, name string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == name
	}
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	rest := name[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		if mid == "" {
			continue
		}
		i := strings.Index(rest, mid)
		if i < 0 {
			return false
		}
		rest = rest[i+len(mid):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// Confinement is a session's selectors resolved against the CURRENT topic
// table. Built per request; never cached.
type Confinement struct {
	Globs     []string // the grant as the operator wrote it
	Topics    []string // existing topic names the grant matches right now
	MaxBudget int
}

// ConfinesTopics reports whether a topic grant is in force.
func (c *Confinement) ConfinesTopics() bool { return c != nil && len(c.Globs) > 0 }

// MatchesTopic reports whether one topic name lies inside the grant.
func (c *Confinement) MatchesTopic(name string) bool {
	if !c.ConfinesTopics() {
		return true
	}
	for _, g := range c.Globs {
		if globMatch(g, name) {
			return true
		}
	}
	return false
}

// Scope is the topic-name list handed to the existing scope path. It is
// non-nil whenever a topic grant is in force — INCLUDING when it is empty,
// which means "your grant matches no existing topic", not "no filter".
func (c *Confinement) Scope() []string {
	if !c.ConfinesTopics() {
		return nil
	}
	if c.Topics == nil {
		return []string{}
	}
	return c.Topics
}

// resolveConfinement expands a session's selectors against the topic table.
func (d *Daemon) resolveConfinement(sess *Session) (*Confinement, error) {
	if sess == nil || sess.Selectors.Empty() {
		return nil, nil
	}
	c := &Confinement{Globs: sess.Selectors.Topics, MaxBudget: sess.Selectors.MaxBudgetChars}
	if len(c.Globs) == 0 {
		return c, nil
	}
	topics, err := d.proj.TopicList() // the existing topic resolver's listing
	if err != nil {
		return nil, err
	}
	for _, t := range topics {
		if c.MatchesTopic(t.Name) {
			c.Topics = append(c.Topics, t.Name)
		}
	}
	sort.Strings(c.Topics)
	return c, nil
}

// confineScope turns a resolved topic scope into a message-id set. nil means
// unconfined; an EMPTY map means the grant admits nothing.
func (d *Daemon) confineScope(confine []string) (map[string]bool, error) {
	if confine == nil {
		return nil, nil
	}
	if len(confine) == 0 {
		return map[string]bool{}, nil
	}
	return d.proj.ScopeMessageIDs(confine, "", "")
}

// intersectScope narrows an existing scope with a confinement scope. A nil
// scope means "unfiltered", so it is replaced rather than intersected.
func intersectScope(scope, confine map[string]bool) map[string]bool {
	if confine == nil {
		return scope
	}
	if scope == nil {
		return confine
	}
	out := map[string]bool{}
	for id := range scope {
		if confine[id] {
			out[id] = true
		}
	}
	return out
}

// messageInScope reports whether one message carries a topic inside the grant.
// A message with NO topics is out of scope: grants are positive, and an
// untopiced message is inside no subtree. A message that does not exist is
// likewise out of scope — for a confined session "absent" and "not yours" are
// deliberately indistinguishable, so the refusal is not an existence oracle.
func (d *Daemon) messageInScope(c *Confinement, messageID string) (bool, error) {
	if !c.ConfinesTopics() {
		return true, nil
	}
	if messageID == "" {
		return true, nil // the op names no resource
	}
	meta, err := d.proj.ResultMeta([]string{messageID})
	if err != nil {
		return false, err
	}
	for _, name := range meta[messageID].Topics {
		if c.MatchesTopic(name) {
			return true, nil
		}
	}
	return false, nil
}

// --- op classification -------------------------------------------------------

type confineMode int

const (
	// confineRefuse is the ZERO VALUE and therefore the default for any op not
	// listed below — a new op is unreachable from a confined session until
	// somebody decides what confinement means for it.
	confineRefuse confineMode = iota
	// confineScoped: the op searches or lists content; the grant becomes a hard
	// pre-filter and the response says so.
	confineScoped
	// confineResource: the op names ONE message; its topics decide.
	confineResource
	// confineOpen: the op exposes no mesh content (daemon/device state, this
	// view's own local config), so a topic grant has nothing to say about it.
	confineOpen
	// confinePublish: a write whose declared topics must lie inside the grant.
	confinePublish
)

var opConfinement = map[string]confineMode{
	// retrieval over a collection
	"search": confineScoped, "digest": confineScoped, "thread": confineScoped,
	"saved-run": confineScoped, "topic-list": confineScoped,

	// one named message
	"peek": confineResource, "fetch": confineResource, "why-ranked": confineResource,
	"signal": confineResource, "outcome": confineResource, "summary-show": confineResource,
	"derivative-list": confineResource, "retract": confineResource, "revise": confineResource,

	// no mesh content
	"status": confineOpen, "sync-status": confineOpen, "peer-list": confineOpen,
	"saved-list": confineOpen, "onboarding-get": confineOpen,
	"subscribe-local": confineOpen, "subscription-local-get": confineOpen,
	// G6: staging bytes names no message; the publish that references them is
	// the checked step.
	"stage-attachment": confineOpen,

	"publish": confinePublish,

	// Explicitly refused, listed rather than omitted so the choice is on the
	// record: `map` and `compact` render the WHOLE mesh's topology (every
	// topic, every thread rollup) and confining them means rewriting both
	// renderers, not filtering a list; `source-ref` maps an ingest path to a
	// message id and would answer about messages outside the grant. A typed
	// refusal is the honest answer until confining them is worth doing.
	"map": confineRefuse, "compact": confineRefuse, "source-ref": confineRefuse,

	// Everything else is refused BY DEFAULT (confineRefuse is the zero value):
	// structural ops (link, pin,
	// topic-create, export, resolve, sync-now, peer-add/remove, subscriptions,
	// housekeep, reindex, gates, interaction-list, rank-stats, …) restructure or
	// expose the whole mesh and are operator work; they need an unconfined
	// handle, and the action tier already denies most of them to agents.
}

// Refusal is a TYPED refusal (D3). Zero rows means "nothing matched"; this
// means "you may not ask", and the two must never look alike.
type Refusal struct {
	Code       string   `json:"code"` // stable: "out_of_scope"
	Op         string   `json:"op"`
	Detail     string   `json:"detail"`
	TopicGrant []string `json:"topic_grant,omitempty"`
}

// RefusalOutOfScope is the one code D3 defines.
const RefusalOutOfScope = "out_of_scope"

// CapabilityNotice reports what the selectors DID to a request — a clamp or a
// scope that was applied silently is a lie by omission, and an agent that
// cannot see its own confinement cannot report it to a human.
type CapabilityNotice struct {
	TopicGrant      []string `json:"topic_grant,omitempty"`
	ScopedTopics    []string `json:"scoped_topics,omitempty"`
	BudgetRequested int      `json:"budget_chars_requested,omitempty"`
	BudgetGranted   int      `json:"budget_chars_granted,omitempty"`
	BudgetClamped   bool     `json:"budget_clamped,omitempty"`
	Withheld        int      `json:"withheld_out_of_scope,omitempty"`
	Note            string   `json:"note,omitempty"`
}

func (c *Confinement) refuse(op, detail string) Response {
	r := &Refusal{Code: RefusalOutOfScope, Op: op, Detail: detail, TopicGrant: c.Globs}
	return Response{
		Refused: r,
		Error: fmt.Sprintf("capability: %s: %s (grant: topic=%s) — this is a REFUSAL, not an empty result",
			RefusalOutOfScope, detail, strings.Join(c.Globs, ",")),
	}
}

// applyConfinement is the single enforcement point. It returns a non-nil
// Response when the request is refused, and a notice describing whatever it
// changed. It mutates only budget fields on the request; the topic scope is
// returned via the notice's caller (dispatch passes c.Scope() into the
// options struct of the three ops that take one).
func (d *Daemon) applyConfinement(req *Request, c *Confinement) (*Response, *CapabilityNotice, error) {
	if c == nil {
		return nil, nil, nil
	}
	notice := &CapabilityNotice{TopicGrant: c.Globs, ScopedTopics: c.Topics}

	// 1. budget cap. A request that asks for no budget (0 = unbudgeted for
	// search) is capped too — otherwise the cap is trivially bypassed by
	// omitting the field.
	if c.MaxBudget > 0 {
		clamp := func(v *int) {
			if *v <= 0 || *v > c.MaxBudget {
				notice.BudgetRequested, notice.BudgetGranted, notice.BudgetClamped = *v, c.MaxBudget, true
				*v = c.MaxBudget
			}
		}
		clamp(&req.BudgetChars)
		if req.Search2 != nil {
			clamp(&req.Search2.BudgetChars)
		}
		if notice.BudgetClamped {
			notice.Note = fmt.Sprintf("budget_chars clamped to the session cap (%d)", c.MaxBudget)
		}
	}

	if !c.ConfinesTopics() {
		return nil, notice, nil
	}

	switch opConfinement[req.Op] {
	case confineOpen:
		return nil, notice, nil

	case confineScoped:
		// search may ALSO carry caller topics: each must lie inside the grant,
		// or the request is refused rather than quietly narrowed.
		var asked []string
		if req.Search2 != nil {
			asked = req.Search2.Topics
		}
		for _, t := range asked {
			if !c.MatchesTopic(t) {
				resp := c.refuse(req.Op, fmt.Sprintf("topic %q is outside this session's grant", t))
				return &resp, notice, nil
			}
		}
		return nil, notice, nil

	case confineResource:
		in, err := d.messageInScope(c, req.MessageID)
		if err != nil {
			return nil, notice, err
		}
		if !in {
			resp := c.refuse(req.Op, fmt.Sprintf("message %s is outside this session's grant (or does not exist — a confined session is told the same thing either way)", req.MessageID))
			return &resp, notice, nil
		}
		return nil, notice, nil

	case confinePublish:
		if req.Publish == nil {
			return nil, notice, nil // the handler reports the missing payload
		}
		if len(req.Publish.Topics) == 0 {
			resp := c.refuse(req.Op, "a confined session must name at least one topic inside its grant (an untopiced message would land outside its own scope)")
			return &resp, notice, nil
		}
		for _, t := range req.Publish.Topics {
			if !c.MatchesTopic(t) {
				resp := c.refuse(req.Op, fmt.Sprintf("topic %q is outside this session's grant", t))
				return &resp, notice, nil
			}
		}
		// a reply inherits a thread that may sit outside the grant
		if req.Publish.ReplyToMessageID != "" {
			in, err := d.messageInScope(c, req.Publish.ReplyToMessageID)
			if err != nil {
				return nil, notice, err
			}
			if !in {
				resp := c.refuse(req.Op, fmt.Sprintf("cannot reply to %s: it is outside this session's grant", req.Publish.ReplyToMessageID))
				return &resp, notice, nil
			}
		}
		return nil, notice, nil

	default: // confineRefuse — including every op nobody has classified
		resp := c.refuse(req.Op, fmt.Sprintf("op %q is not available to a topic-confined session; it exposes or restructures the whole mesh", req.Op))
		return &resp, notice, nil
	}
}
