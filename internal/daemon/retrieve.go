package daemon

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/rank"
	"github.com/ggoosen/cairn/internal/telemetry"
)

// SearchOptions is one search invocation (budget over the COMPLETE payload).
type SearchOptions struct {
	Query string `json:"query"`
	K     int    `json:"k,omitempty"`
	// D4: exactly one budget, never both. A request carrying both is
	// REFUSED — resolving it by precedence would silently ignore one of the
	// two numbers the caller wrote.
	BudgetChars      int  `json:"budget_chars,omitempty"`
	BudgetTokens     int  `json:"budget_tokens,omitempty"`
	IncludeRetracted bool `json:"include_retracted,omitempty"`

	// BudgetCeilingChars (D4 × D3) is the session's max_budget_chars cap,
	// enforced as a SECOND hard limit beside the caller's budget rather than
	// converted into it. `json:"-"`: dispatch sets it from the capability
	// gate, never a client.
	BudgetCeilingChars int `json:"-"`

	// RETR-D3 scope (spec §7.1 search(query, scope, k)): hard pre-filters.
	// Topics match by NAME (existing topics only — typos refuse, they do
	// not silently return nothing); Sender is a principal id; ThreadID
	// narrows to one conversation.
	Topics   []string `json:"topics,omitempty"`
	Sender   string   `json:"sender,omitempty"`
	ThreadID string   `json:"thread_id,omitempty"`

	// attribution (rulings §10); missing values are inferred and flagged
	TaskID          string `json:"task_id,omitempty"`
	AgentSurface    string `json:"agent_surface,omitempty"`
	AgentInstanceID string `json:"agent_instance_id,omitempty"`

	// Principal hierarchy (N2). Set by dispatch from the capability gate —
	// any client-supplied value is overwritten there.
	Principal string `json:"principal,omitempty"`

	// Confine (D3) is the session's resolved topic grant, set ONLY by the
	// dispatch capability gate — `json:"-"` so a client cannot supply it. nil
	// means unconfined; an EMPTY non-nil slice means the grant matches no
	// existing topic, which admits nothing (an absent filter would admit
	// everything, which is the bug this distinction exists to prevent).
	Confine []string `json:"-"`
}

// SearchOutput carries the ranked results plus the budget-compliant payload.
type SearchOutput struct {
	InteractionID string         `json:"interaction_id"`
	RetrievalMode string         `json:"retrieval_mode"` // full | lexical_only
	Results       []RankedResult `json:"results"`
	Payload       string         `json:"payload"` // ≤ budget_chars Unicode scalars, metadata included
	Omitted       int            `json:"omitted,omitempty"`
	// Partial (P3-3b, spec §7): on a THIN node the local corpus is only a recent
	// window, so a universal search has no offline completeness guarantee. The
	// flag is truthful signal to the agent that older material may exist on full
	// nodes and was not searched here.
	Partial       bool   `json:"partial,omitempty"`
	PartialReason string `json:"partial_reason,omitempty"`
	// RemoteSource (P3-3d): set when a thin node satisfied this search by asking a
	// full peer — the peer's address. The results are that peer's complete view,
	// not this node's local window.
	RemoteSource string `json:"remote_source,omitempty"`
	// Budget (D4) names the mode, the limit, the TOKENIZER that counted it and
	// what the payload actually cost — a budget is only meaningful against a
	// named tokenizer, so every budgeted response carries one.
	Budget rank.Report `json:"budget"`
	// LexicalQuery (D11) reports which query terms the lexical arm actually
	// searched. Matching is disjunctive, so the answer to "why did this come
	// back?" is no longer "it contained every word you typed"; this says what
	// was searched and what the index dropped as carrying no ordering
	// information, without an agent having to reason about bm25.
	LexicalQuery *projection.LexicalPlan `json:"lexical_query,omitempty"`
}

// RankedResult is one scored hit. RETR-D1: sender/created/topics/snippet
// ride along so a result is triageable without a fetch — pre-fix the
// payload was UUIDs, hashes and scores only, and the budget was spent on
// opaque identifiers. Snippet/sender/topics are MESH CONTENT: untrusted
// data, never instructions (R18/R53).
type RankedResult struct {
	Rank       int      `json:"rank"`
	MessageID  string   `json:"message_id"`
	RevisionID string   `json:"revision_id"`
	BodyHash   string   `json:"body_hash"`
	TextClass  string   `json:"text_class"`
	Score      float64  `json:"score"`
	Mandatory  string   `json:"mandatory,omitempty"`
	Sender     string   `json:"sender,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
	Topics     []string `json:"topics,omitempty"`
	Snippet    string   `json:"snippet,omitempty"` // first SearchSnippetChars scalars of the body
}

// componentsRecord is the stored why_ranked arithmetic (decimal strings).
type componentsRecord struct {
	R       string `json:"R"`
	S       string `json:"S,omitempty"` // P2 salience
	F       string `json:"F"`
	Peff    string `json:"P_eff"`
	I       string `json:"I,omitempty"` // P2 operator intent
	N       string `json:"N,omitempty"` // P2 novelty
	RRF     string `json:"RRF"`
	LexRank int    `json:"lex_rank"`
	VecRank int    `json:"vec_rank"`
	Score   string `json:"score"`
	Weights struct {
		R string `json:"R"`
		S string `json:"S,omitempty"`
		F string `json:"F"`
		P string `json:"P"`
		I string `json:"I,omitempty"`
		N string `json:"N,omitempty"`
	} `json:"weights"`
	CreatedAt string `json:"created_at"`
	Mandatory string `json:"mandatory,omitempty"`
}

// fillP2Components adds the S/I/N component + weight strings when the profile is
// P2 (kept out of the P0 record so existing explanations are byte-identical).
func fillP2Components(rec *componentsRecord, s rank.Scored, profile rank.Profile) {
	if !profile.IsP2() {
		return
	}
	w := profile.Weights()
	rec.S, rec.I, rec.N = rank.Dec(s.S), rank.Dec(s.I), rank.Dec(s.N)
	rec.Weights.S, rec.Weights.I, rec.Weights.N = rank.Dec(w.S), rank.Dec(w.I), rank.Dec(w.N)
}

// Search: FTS top-100 + vector top-100 → RRF k=60 → percentile → P0 search
// profile → budget-truncate (rulings §7). Never fails for lack of
// embeddings — degrades to lexical_only.
func (d *Daemon) Search(opts SearchOptions) (*SearchOutput, error) {
	if opts.K <= 0 {
		opts.K = 10
	}
	// D4: exactly one budget. Refused BEFORE any work, so a malformed
	// request costs nothing and reads the same on every surface.
	spec, err := rank.NewSpec(opts.BudgetChars, opts.BudgetTokens)
	if err != nil {
		return nil, fmt.Errorf("rejected before ack: %w", err)
	}
	spec.Ceiling = opts.BudgetCeilingChars
	// RETR-D3: resolve the scope FIRST (hard filters before ranking, like
	// digest topic filters); nil scope = unfiltered.
	scope, serr := d.proj.ScopeMessageIDs(opts.Topics, opts.Sender, opts.ThreadID)
	if serr != nil {
		return nil, fmt.Errorf("rejected before ack: %w", serr)
	}
	// D3: the capability grant is an additional HARD filter, intersected with
	// whatever the caller asked for. It runs through the same resolver, so
	// there is one notion of "in this topic" in the system.
	confined, err := d.confineScope(opts.Confine)
	if err != nil {
		return nil, fmt.Errorf("rejected before ack: %w", err)
	}
	scope = intersectScope(scope, confined)
	lexIDs, lexPlan, err := d.proj.LexicalTopKPlan(opts.Query, config.FusionCandidatesFTS, opts.IncludeRetracted)
	if err != nil {
		return nil, err
	}
	if scope != nil {
		// The FTS pool is cut at FusionCandidatesFTS BEFORE this filter, so
		// a very narrow scope inside a very large corpus can under-fill; the
		// vector path below filters BEFORE its top-K and compensates.
		kept := lexIDs[:0]
		for _, id := range lexIDs {
			if scope[id] {
				kept = append(kept, id)
			}
		}
		lexIDs = kept
	}

	mode := "lexical_only"
	vecIDs := []string(nil)
	// H6/rung 4 (spec §8.2): under a severe embedding backlog the ladder forces
	// lexical-only, shedding the vector query's cost even when an embedder is
	// present. Inert at a healthy level.
	if e := d.emb(); e != nil && !d.DegradationLevel().LexicalOnlyForced() {
		qvecs, err := e.Embed([]string{opts.Query})
		d.noteEmbed(err) // D10: so `cairn status` can name a failing embedder
		if err == nil {
			// D1: the SCOPE goes into the query, not around it. Whether the
			// candidates come from the vec0 index or the brute-force oracle,
			// the top-K is computed over the permitted set — a confined
			// session cannot see a neighbour it has no grant for, and a
			// --topic search cannot be diluted by out-of-topic vectors.
			ids, verr := d.proj.VectorTopK(e.ModelID(), qvecs[0], config.FusionCandidatesVector,
				opts.IncludeRetracted, scope)
			if verr == nil {
				vecIDs = ids
				// "full" means the semantic path RAN over an embedded corpus,
				// not that the scope left anything in it — a narrow scope that
				// matches nothing is still a hybrid retrieval.
				if len(ids) > 0 {
					mode = "full"
				} else if has, herr := d.proj.HasVectors(e.ModelID()); herr == nil && has {
					mode = "full"
				}
			}
		}
	}

	// union of candidates with per-list 1-based ranks
	union := map[string]*rank.Candidate{}
	for i, id := range lexIDs {
		union[id] = &rank.Candidate{MessageID: id, LexRank: i + 1}
	}
	for i, id := range vecIDs {
		if c, ok := union[id]; ok {
			c.VecRank = i + 1
		} else {
			union[id] = &rank.Candidate{MessageID: id, VecRank: i + 1}
		}
	}
	ids := make([]string, 0, len(union))
	for id := range union {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows, err := d.proj.RankRows(ids, "")
	if err != nil {
		return nil, err
	}
	profile := d.rankProfileSearch()
	// P2-3: the full additive profile also needs S (salience) + N (novelty) per
	// candidate; I (intent) comes from per-message pin/priority-confirm. P0
	// leaves them zero (ignored by its weights).
	var p2 map[string]P2Input
	if profile.IsP2() {
		p2, _ = d.P2Inputs()
	}
	cands := make([]rank.Candidate, 0, len(ids))
	for _, id := range ids {
		c := union[id]
		row, ok := rows[id]
		if !ok {
			continue
		}
		c.EventID = row.CreatedEventID
		c.CreatedAt = parseWall(row.CreatedAt)
		c.Priority = row.Priority
		c.Suspended = row.PinActive || row.PriorityConf
		if profile.IsP2() {
			c.Salience = p2[id].Salience
			c.Novelty = p2[id].Novelty
			c.Intent = intentFromRow(row.PinActive, row.PriorityConf)
		}
		cands = append(cands, *c)
	}

	scored := rank.Rank(cands, profile, d.now())
	if profile.IsP2() {
		// §9.2 exploration quota: reserve a fraction of the K slots for new items
		// so cold-start content gets exposure (P2H4). P0 profiles have no
		// exploration term, so they keep the plain top-K cut.
		scored = applyExplorationQuota(scored, opts.K, func(id string) bool {
			return p2[id].Impressions < config.SalienceMinImpressions
		})
	} else if len(scored) > opts.K {
		scored = scored[:opts.K]
	}
	out, err := d.finishRetrieval(scored, rows, profile, mode, spec)
	if err != nil {
		return nil, err
	}
	out.LexicalQuery = &lexPlan
	d.recordInteraction("search", out.InteractionID, opts.Query, out.Budget, out, opts.TaskID, opts.AgentSurface, opts.AgentInstanceID, opts.Principal)
	// P3-3d: on a thin node with remote-query enabled, prefer a full peer's
	// complete result over our partial local one (best-effort; keeps local on
	// failure). Last, so no lock is held across the network call.
	if remote := d.maybeRemoteConsult(out, opts); remote != nil {
		return remote, nil
	}
	return out, nil
}

// recordInteraction logs telemetry (local-only; never an event). Missing
// attribution is daemon-inferred and flagged (rulings §10).
func (d *Daemon) recordInteraction(kind, interactionID, query string, budget rank.Report, out *SearchOutput, taskID, surface, instance, principal string) {
	if d.tel == nil {
		return
	}
	inferred := false
	if taskID == "" {
		taskID = "task-" + interactionID[:8]
		inferred = true
	}
	if surface == "" {
		surface = "operator"
		inferred = true
	}
	if principal == "" {
		principal = "operator" // direct method call = tier-1 (not inferred)
	}
	ids := make([]string, 0, len(out.Results))
	for _, r := range out.Results {
		ids = append(ids, r.MessageID)
	}
	it := telemetry.Interaction{
		InteractionID: interactionID, Kind: kind,
		TaskID: taskID, AgentSurface: surface, AgentInstanceID: instance, Principal: principal,
		Inferred: inferred, Query: query,
		// D4: the limit and the payload cost are both recorded in the
		// budget's OWN unit, so the §11 compliance gate compares like with
		// like whichever mode the caller used.
		BudgetRequested: budget.Limit, BudgetMode: budget.Mode,
		BudgetTokenizer: budget.Tokenizer, PayloadUnits: budget.Used,
		PayloadChars: rank.BudgetChars(out.Payload), ResultCount: len(out.Results),
		RetrievalMode: out.RetrievalMode, CreatedAt: d.now(), ResultIDs: ids,
	}
	if err := d.tel.Record(it); err != nil {
		fmt.Fprintf(d.warn, "WARNING: telemetry: %v\n", err)
	}
}

// snippetFor returns the first config.SearchSnippetChars Unicode scalars of
// a body (raw; the payload renderer quotes it line-by-line).
func snippetFor(body []byte) string {
	txt := string(body)
	if runes := []rune(txt); len(runes) > config.SearchSnippetChars {
		txt = string(runes[:config.SearchSnippetChars])
	}
	return strings.TrimRight(txt, "\n")
}

// quoteLines prefixes every line with QuotePrefix (untrusted-content
// quoting, same discipline as digest excerpts), newline-terminated.
func quoteLines(txt string) string {
	if txt == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(txt, "\n") {
		b.WriteString(config.QuotePrefix)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// inlineMeta renders one mesh-supplied metadata field for a single-line
// payload cell (R53 render leg: same charset collapse the map view uses).
func inlineMeta(s string) string {
	if s == "" {
		return "-"
	}
	return sanitizeMapField(s)
}

// finishRetrieval renders the budget-compliant payload, stores why_ranked
// inputs, and assembles the output. Budget covers the ENTIRE payload —
// header, entries, truncation marker (rulings §7).
func (d *Daemon) finishRetrieval(scored []rank.Scored, rows map[string]projection.RankRow, profile rank.Profile, mode string, spec rank.Spec) (*SearchOutput, error) {
	interactionID := d.newUUID()

	ids := make([]string, 0, len(scored))
	for _, s := range scored {
		ids = append(ids, s.MessageID)
	}
	meta, err := d.proj.ResultMeta(ids)
	if err != nil {
		return nil, err
	}
	// bodies for snippets: best-effort (expired/missing = no snippet)
	snippets := make(map[string]string, len(scored))
	for _, s := range scored {
		if body, err := d.store.Get(rows[s.MessageID].BodyHash); err == nil {
			snippets[s.MessageID] = snippetFor(body)
		}
	}

	render := func(i int) string {
		s := scored[i]
		row := rows[s.MessageID]
		m := s.Mandatory
		if m == "" {
			m = "-"
		}
		mm := meta[s.MessageID]
		topics := "-"
		if len(mm.Topics) > 0 {
			names := make([]string, len(mm.Topics))
			for j, t := range mm.Topics {
				names[j] = inlineMeta(t)
			}
			topics = strings.Join(names, ",")
		}
		return fmt.Sprintf("%d\t%s\t%s\t%s\t%s\t%s\tfrom=%s\tat=%s\ttopics=%s\n",
			i+1, s.MessageID, row.HeadRevisionID, row.BodyHash, rank.Dec(s.Score), m,
			inlineMeta(mm.Sender), row.CreatedAt, topics) + quoteLines(snippets[s.MessageID])
	}
	header := fmt.Sprintf("interaction\t%s\tmode\t%s\n", interactionID, mode)
	included, payload := rank.TakeWithinBudget(len(scored), spec.Limits(),
		rank.BudgetRender{Header: header, Marker: "TRUNCATED\n"}, render)

	out := &SearchOutput{
		InteractionID: interactionID,
		RetrievalMode: mode,
		Payload:       payload,
		Omitted:       len(scored) - included,
		Budget:        spec.Report(payload),
	}
	if r := d.thinSearchPartialReason(); r != "" {
		out.Partial, out.PartialReason = true, r
	}
	var expl []projection.ExplanationRow
	for i := 0; i < included; i++ {
		s := scored[i]
		row := rows[s.MessageID]
		mm := meta[s.MessageID]
		// the struct field carries the raw truncated text (the payload keeps
		// the QuotePrefix-quoted form); still untrusted data (R18)
		out.Results = append(out.Results, RankedResult{
			Rank: i + 1, MessageID: s.MessageID, RevisionID: row.HeadRevisionID,
			BodyHash: row.BodyHash, TextClass: row.TextClass, Score: s.Score, Mandatory: s.Mandatory,
			Sender: mm.Sender, CreatedAt: row.CreatedAt, Topics: mm.Topics, Snippet: snippets[s.MessageID],
		})
		var rec componentsRecord
		rec.R, rec.F, rec.Peff, rec.RRF = rank.Dec(s.R), rank.Dec(s.F), rank.Dec(s.Peff), rank.Dec(s.RRF)
		rec.LexRank, rec.VecRank = s.Components.LexRank, s.Components.VecRank
		rec.Score = rank.Dec(s.Score)
		wR, wF, wP := profileWeights(profile)
		rec.Weights.R, rec.Weights.F, rec.Weights.P = rank.Dec(wR), rank.Dec(wF), rank.Dec(wP)
		fillP2Components(&rec, s, profile)
		rec.CreatedAt = row.CreatedAt
		rec.Mandatory = s.Mandatory
		blob, err := json.Marshal(rec)
		if err != nil {
			return nil, err
		}
		expl = append(expl, projection.ExplanationRow{MessageID: s.MessageID, ComponentsJSON: string(blob), FinalRank: i + 1})
	}
	if err := d.proj.SaveExplanations(interactionID, string(profile), expl); err != nil {
		return nil, err
	}
	return out, nil
}

// profileWeights returns the (R, F, P) weights for the P0 why_ranked lines. For
// P2 profiles P is 0 and the S/I/N terms carry the weight (recorded separately
// by fillP2Components); R and F come from the profile's public weights.
func profileWeights(p rank.Profile) (float64, float64, float64) {
	w := p.Weights()
	return w.R, w.F, w.P
}

// applyExplorationQuota implements the §9.2 "10% exploration quota for new
// items" (P2H4). `scored` is in final rank order (best first). It fills the K
// result slots as: (1) the top (K−quota) by score = the merit slots; (2) up to
// `quota` NEW items — impressions < SalienceMinImpressions — promoted from the
// cut region in score order, so cold-start content that scoring would have
// buried still surfaces and accrues impressions; (3) any exploration slots left
// unfilled (not enough new items) backfilled with the next-best by merit, so
// the quota never wastes budget. The returned slice preserves global rank order
// (a promoted new item appears where its own score places it, not pinned to the
// top). quota is floored, so a search too small to reserve a whole slot
// (K < 1/fraction) keeps the plain top-K cut — no tiny-K starvation.
func applyExplorationQuota(scored []rank.Scored, k int, isNew func(id string) bool) []rank.Scored {
	if k <= 0 || len(scored) <= k {
		return scored // every candidate already fits; nothing to reserve against
	}
	quota := int(math.Floor(float64(k)*config.ExplorationQuotaFraction + 1e-9))
	if quota < 1 {
		return scored[:k]
	}
	merit := k - quota

	chosen := make([]bool, len(scored))
	count := 0
	// (1) merit slots — top (K−quota) by score
	for i := 0; i < len(scored) && count < merit; i++ {
		chosen[i] = true
		count++
	}
	// (2) exploration slots — new items from the remainder, best score first
	explored := 0
	for i := 0; i < len(scored) && explored < quota && count < k; i++ {
		if !chosen[i] && isNew(scored[i].MessageID) {
			chosen[i] = true
			count++
			explored++
		}
	}
	// (3) backfill any unused exploration slots with the next-best by merit
	for i := 0; i < len(scored) && count < k; i++ {
		if !chosen[i] {
			chosen[i] = true
			count++
		}
	}
	out := make([]rank.Scored, 0, count)
	for i := range scored {
		if chosen[i] {
			out = append(out, scored[i])
		}
	}
	return out
}

// D1: the in-process cosine scan that used to live here moved into
// internal/projection (VectorTopKBruteForce), beside the vec0 path it is the
// oracle for. Both are reached through Projection.VectorTopK, so a caller
// cannot accidentally pick one.

// --- digest ------------------------------------------------------------------

// ViewConfig is the LOCAL, non-event digest configuration (rulings §7):
// hard topic filters + optional natural-language interest query.
type ViewConfig struct {
	Topics        []string `json:"topics,omitempty"`
	InterestQuery string   `json:"interest_query,omitempty"`
}

// DigestOptions parameterizes one digest generation.
type DigestOptions struct {
	AgentView string `json:"agent_view"`
	// D4: exactly one of BudgetChars / BudgetTokens, and a digest requires
	// one of them (unlike search, which may be unbudgeted).
	BudgetChars  int    `json:"budget_chars"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	Principal    string `json:"principal,omitempty"` // dispatch-resolved (N2)
	// BudgetCeilingChars (D4 × D3): the session's max_budget_chars cap, a
	// second hard limit rather than a conversion. Dispatch-set only.
	BudgetCeilingChars int `json:"-"`
	// Confine (D3) is the session's resolved topic grant — dispatch-set only
	// (`json:"-"`). Unlike the view's own hard topic filters it also binds the
	// MANDATORY items: an explicit recipient or a pin outside the grant would
	// otherwise walk straight past the confinement, since mandatory items are
	// deliberately exempt from the view's filters.
	Confine []string `json:"-"`
}

// DigestOutput is the generated digest.
type DigestOutput struct {
	InteractionID    string `json:"interaction_id"`
	Path             string `json:"path"`
	Payload          string `json:"payload"`
	Included         int    `json:"included"`
	OmittedMandatory int    `json:"omitted_mandatory_count"`
	RetrievalMode    string `json:"retrieval_mode"`
	// Partial (P3-3b, spec §7): true on a thin node — the digest is drawn from a
	// recent window only, not the whole corpus.
	Partial       bool   `json:"partial,omitempty"`
	PartialReason string `json:"partial_reason,omitempty"`
	// Budget (D4): mode, limit, tokenizer and the payload's cost in that unit.
	Budget rank.Report `json:"budget"`
}

// Digest generates views/<agent>/digest.md: candidates pass the view's hard
// filters; R is hybrid relevance vs the interest query (no query ⇒ R=1.0);
// mandatory items (explicit recipients, then pins) come first and consume
// budget; overflow drops oldest-first and is reported (rulings §7).
func (d *Daemon) Digest(opts DigestOptions) (*DigestOutput, error) {
	if !validViewName(opts.AgentView) {
		return nil, fmt.Errorf("invalid agent view %q (plain names only: no separators, no ..)", opts.AgentView)
	}
	// D4: exactly one budget, and a digest must have one — an unbudgeted
	// digest is the whole corpus, which is precisely what a digest is not.
	spec, err := rank.NewSpec(opts.BudgetChars, opts.BudgetTokens)
	if err != nil {
		return nil, err
	}
	spec.Ceiling = opts.BudgetCeilingChars
	if !spec.Bounded() {
		return nil, fmt.Errorf("digest requires a budget: pass budget_chars > 0 or budget_tokens > 0 (exactly one)")
	}
	cfg := d.readViewConfig(opts.AgentView)

	candIDs, err := d.proj.DigestCandidates(cfg.Topics)
	if err != nil {
		return nil, err
	}
	recips, pinned, err := d.proj.MandatoryDigestIDs(opts.AgentView)
	if err != nil {
		return nil, err
	}
	mandatory := map[string]string{}
	for _, id := range recips {
		mandatory[id] = "recipient"
	}
	for _, id := range pinned {
		if _, ok := mandatory[id]; !ok {
			mandatory[id] = "pin"
		}
	}
	// mandatory items are included regardless of hard filters
	inSet := map[string]bool{}
	for _, id := range candIDs {
		inSet[id] = true
	}
	for id := range mandatory {
		if !inSet[id] {
			candIDs = append(candIDs, id)
			inSet[id] = true
		}
	}

	// N3 (R26): durable-subscription matches come after mandatory, marked,
	// inside the SAME budget. Hard filters, delivery history, windows and
	// caps are all applied inside subscriptionMatches (R24).
	subAttribution, err := d.subscriptionMatches(opts.AgentView, mandatory)
	if err != nil {
		return nil, err
	}
	for id := range subAttribution {
		if _, ok := mandatory[id]; !ok {
			mandatory[id] = "subscription"
		}
		if !inSet[id] {
			candIDs = append(candIDs, id)
			inSet[id] = true
		}
	}

	// D3: the capability grant binds LAST, after mandatory items and
	// subscription matches have been added — precisely because those three
	// paths are exempt from the view's own hard filters, and a grant that
	// anything can walk around is not a grant.
	if confined, cerr := d.confineScope(opts.Confine); cerr != nil {
		return nil, cerr
	} else if confined != nil {
		kept := candIDs[:0]
		for _, id := range candIDs {
			if confined[id] {
				kept = append(kept, id)
				continue
			}
			delete(mandatory, id)
		}
		candIDs = kept
	}

	// relevance: hybrid vs the interest query; none ⇒ R=1.0 uniformly
	mode := "lexical_only"
	lexRank := map[string]int{}
	vecRank := map[string]int{}
	uniform := strings.TrimSpace(cfg.InterestQuery) == ""
	if !uniform {
		lexIDs, err := d.proj.LexicalTopK(cfg.InterestQuery, config.FusionCandidatesFTS, false)
		if err != nil {
			return nil, err
		}
		for i, id := range lexIDs {
			lexRank[id] = i + 1
		}
		if e := d.emb(); e != nil {
			if qvecs, err := e.Embed([]string{cfg.InterestQuery}); err == nil {
				// D1: same routed top-K as search, but deliberately UNSCOPED
				// (nil). The digest fuses a global lexical top-K with a global
				// vector top-K, and reads a rank only for candidates that
				// already survived the view's hard filters and the D3 grant —
				// so nothing out of scope reaches the agent. Scoping only the
				// vector half would tilt RRF toward it; scoping both is a
				// ranking change, not an indexing one, and does not belong in
				// a performance task.
				ids, verr := d.proj.VectorTopK(e.ModelID(), qvecs[0], config.FusionCandidatesVector, false, nil)
				if verr == nil {
					for i, id := range ids {
						vecRank[id] = i + 1
					}
					if len(ids) > 0 {
						mode = "full"
					} else if has, herr := d.proj.HasVectors(e.ModelID()); herr == nil && has {
						mode = "full"
					}
				}
			}
		}
	} else {
		mode = "full" // no relevance component in play
	}

	rows, err := d.proj.RankRows(candIDs, opts.AgentView)
	if err != nil {
		return nil, err
	}
	digestProfile := d.rankProfileDigest()
	var p2 map[string]P2Input
	if digestProfile.IsP2() {
		p2, _ = d.P2Inputs()
	}
	cands := make([]rank.Candidate, 0, len(candIDs))
	for _, id := range candIDs {
		row, ok := rows[id]
		if !ok {
			continue
		}
		c := rank.Candidate{
			MessageID: id,
			EventID:   row.CreatedEventID,
			CreatedAt: parseWall(row.CreatedAt),
			Priority:  row.Priority,
			Suspended: row.PinActive || row.PriorityConf,
			LexRank:   lexRank[id],
			VecRank:   vecRank[id],
			Mandatory: mandatory[id],
		}
		if digestProfile.IsP2() {
			c.Salience = p2[id].Salience
			c.Novelty = p2[id].Novelty
			c.Intent = intentFromRow(row.PinActive, row.PriorityConf)
		}
		cands = append(cands, c)
	}

	var scored []rank.Scored
	if uniform {
		scored = rank.RankUniformR(cands, digestProfile, d.now())
	} else {
		scored = rank.Rank(cands, digestProfile, d.now())
	}

	interactionID := d.newUUID()
	// N4 (spec §8.4): disputed sender summaries carry a visible marker
	disputed, err := d.proj.DisputedSummaries()
	if err != nil {
		return nil, err
	}
	// N7 (spec §6.3): messages whose attachment blobs are below their
	// durability target carry a [replication-pending] marker.
	pendingRepl := d.pendingBlobMessages()
	header := fmt.Sprintf("# digest — %s\ninteraction: %s\nmode: %s\n\n", opts.AgentView, interactionID, mode)
	// RETR-D2: one meta batch per digest (sender/topics per entry)
	scoredIDs := make([]string, 0, len(scored))
	for _, s := range scored {
		scoredIDs = append(scoredIDs, s.MessageID)
	}
	entryMeta, err := d.proj.ResultMeta(scoredIDs)
	if err != nil {
		return nil, err
	}
	render := func(i int) string {
		return d.renderDigestEntry(i+1, scored[i], rows[scored[i].MessageID], entryMeta[scored[i].MessageID], disputed[scored[i].MessageID], pendingRepl[scored[i].MessageID])
	}
	included, payload := rank.TakeWithinBudget(len(scored), spec.Limits(),
		rank.BudgetRender{Header: header, Marker: "…truncated…\n"}, render)

	// mandatory overflow accounting (drop-oldest-first is the sort order:
	// within a mandatory class newest sorts last by wall time? No — rank
	// sorts newer first, so TakeWithinBudget keeps newest and drops oldest).
	// Subscription matches are NOT mandatory (R26): budget overflow drops
	// them without alarm, and only INCLUDED matches consume window/cap
	// allowance (recorded below).
	omitted := 0
	for i := included; i < len(scored); i++ {
		if scored[i].Mandatory != "" && scored[i].Mandatory != "subscription" {
			omitted++
		}
	}
	if d.tel != nil {
		for i := 0; i < included; i++ {
			for _, subID := range subAttribution[scored[i].MessageID] {
				if err := d.tel.RecordSubDelivery(subID, scored[i].MessageID, d.now()); err != nil {
					fmt.Fprintf(d.warn, "WARNING: subscription delivery record: %v\n", err)
				}
			}
		}
	}

	base, err := d.viewDir(opts.AgentView)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(base, "digest.md")
	if err := d.fs.MkdirAll(filepath.Dir(path), config.DirPerm); err != nil {
		return nil, err
	}
	d.fs.Remove(path)
	if err := fsx.WriteFileAtomic(d.fs, path, []byte(payload), config.FilePerm); err != nil {
		return nil, err
	}

	var expl []projection.ExplanationRow
	for i := 0; i < included; i++ {
		s := scored[i]
		var rec componentsRecord
		rec.R, rec.F, rec.Peff, rec.RRF = rank.Dec(s.R), rank.Dec(s.F), rank.Dec(s.Peff), rank.Dec(s.RRF)
		rec.LexRank, rec.VecRank = s.Components.LexRank, s.Components.VecRank
		rec.Score = rank.Dec(s.Score)
		wR, wF, wP := profileWeights(digestProfile)
		rec.Weights.R, rec.Weights.F, rec.Weights.P = rank.Dec(wR), rank.Dec(wF), rank.Dec(wP)
		fillP2Components(&rec, s, digestProfile)
		rec.CreatedAt = rows[s.MessageID].CreatedAt
		rec.Mandatory = s.Mandatory
		blob, _ := json.Marshal(rec)
		expl = append(expl, projection.ExplanationRow{MessageID: s.MessageID, ComponentsJSON: string(blob), FinalRank: i + 1})
	}
	if err := d.proj.SaveExplanations(interactionID, string(digestProfile), expl); err != nil {
		return nil, err
	}
	dout := &DigestOutput{
		InteractionID:    interactionID,
		Path:             path,
		Payload:          payload,
		Included:         included,
		OmittedMandatory: omitted,
		RetrievalMode:    mode,
		Budget:           spec.Report(payload),
	}
	if r := d.thinSearchPartialReason(); r != "" {
		dout.Partial, dout.PartialReason = true, r
	}
	so := &SearchOutput{Results: nil, Payload: payload, RetrievalMode: mode, InteractionID: interactionID}
	for i := 0; i < included; i++ {
		so.Results = append(so.Results, RankedResult{MessageID: scored[i].MessageID})
	}
	d.recordInteraction("digest", interactionID, cfg.InterestQuery, dout.Budget, so, opts.TaskID, opts.AgentView, "", opts.Principal)
	return dout, nil
}

// renderDigestEntry: one digest item; EVERY line quoting cairn content is
// prefixed with config.QuotePrefix (per-line prefixing cannot be escaped).
// RETR-D2: the meta line (sender · created · topics) makes an entry
// attributable without a peek per item; fields are R53-collapsed inline.
func (d *Daemon) renderDigestEntry(pos int, s rank.Scored, row projection.RankRow, meta projection.ResultMetaRow, summaryDisputed, replicationPending bool) string {
	var b strings.Builder
	tag := ""
	if s.Mandatory != "" {
		tag = " [" + s.Mandatory + "]"
	}
	if summaryDisputed {
		// the sender's summary claim diverges from the body; the excerpt
		// below is the receiver's own (bodies are always excerpted, never
		// sender summaries — spec §8.4)
		tag += " [summary-disputed]"
	}
	if replicationPending {
		// N7: this message's attachment blobs are not yet at their durability
		// target — satisfied asynchronously as peers replicate.
		tag += " [replication-pending]"
	}
	fmt.Fprintf(&b, "%d. %s%s score=%s\n", pos, s.MessageID, tag, rank.Dec(s.Score))
	topics := ""
	if len(meta.Topics) > 0 {
		names := make([]string, len(meta.Topics))
		for i, t := range meta.Topics {
			names[i] = inlineMeta(t)
		}
		topics = " · " + strings.Join(names, ", ")
	}
	fmt.Fprintf(&b, "   from %s · %s%s\n", inlineMeta(meta.Sender), row.CreatedAt, topics)
	body, err := d.store.Get(row.BodyHash)
	if err == nil {
		lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
		if len(lines) > 3 {
			lines = lines[:3]
		}
		for _, line := range lines {
			b.WriteString(config.QuotePrefix)
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

func (d *Daemon) readViewConfig(agent string) ViewConfig {
	var cfg ViewConfig
	base, err := d.viewDir(agent) // traversal guard: never read outside views/
	if err != nil {
		return cfg
	}
	blob, err := d.fs.ReadFile(filepath.Join(base, "view.json"))
	if err == nil {
		_ = json.Unmarshal(blob, &cfg)
	}
	return cfg
}

// WhyRanked prints the EXACT stored arithmetic for one ranked item.
func (d *Daemon) WhyRanked(interactionID, messageID string) (string, error) {
	profile, compJSON, finalRank, err := d.proj.Explanation(interactionID, messageID)
	if err != nil {
		return "", err
	}
	var rec componentsRecord
	if err := json.Unmarshal([]byte(compJSON), &rec); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  rank %d (profile=%s)\n", messageID, finalRank, profile)
	// R47/H3: print EVERY additive term (R, S, F, P_eff, I, N) with its value,
	// weight, and product under BOTH profiles — the printed products must sum to
	// the total, or the explanation is the black box §9 forbids. Empty component/
	// weight strings (a P0 record omits S/I/N; a P2 record zeroes P) parse to 0,
	// so their line reads "0 × 0 = 0" and reconciliation holds on either profile.
	term := func(name, val, weight, extra string) {
		v, w := rank.ParseDec(val), rank.ParseDec(weight)
		fmt.Fprintf(&b, "  %-5s %s × %s = %s%s\n", name, rank.Dec(v), rank.Dec(w), rank.Dec(v*w), extra)
	}
	term("R", rec.R, rec.Weights.R, fmt.Sprintf("   (lex rank %d, vec rank %d, RRF %s)", rec.LexRank, rec.VecRank, rec.RRF))
	term("S", rec.S, rec.Weights.S, "   (salience §9.2)")
	term("F", rec.F, rec.Weights.F, fmt.Sprintf("   (created %s)", rec.CreatedAt))
	term("P_eff", rec.Peff, rec.Weights.P, "   (executable priority, decayed)")
	term("I", rec.I, rec.Weights.I, "   (operator intent)")
	term("N", rec.N, rec.Weights.N, "   (novelty/exposure)")
	if rec.Mandatory != "" {
		fmt.Fprintf(&b, "  mandatory: %s (inclusion class — not an additive score term)\n", rec.Mandatory)
	}
	fmt.Fprintf(&b, "  total %s\n", rec.Score)
	return b.String(), nil
}

// --- enrichment ---------------------------------------------------------------

// EnrichOnce embeds up to batch pending revisions (background enricher body;
// rulings §6: agents never wait on this). Returns how many were embedded.
func (d *Daemon) EnrichOnce(batch int) (int, error) {
	e := d.emb()
	if e == nil {
		return 0, nil // lexical_only until an embedder is provisioned
	}
	stored, err := d.proj.EmbeddingModelID()
	if err != nil {
		return 0, err
	}
	if stored != "" && stored != e.ModelID() {
		return 0, fmt.Errorf("stored vectors belong to model %q, embedder is %q: run `cairn reindex --semantic` to migrate", stored, e.ModelID())
	}
	pending, err := d.proj.PendingEmbeddings(batch)
	if err != nil || len(pending) == 0 {
		return 0, err
	}
	done := 0
	for _, pe := range pending {
		body, err := d.store.Get(pe.BodyHash)
		if err != nil {
			continue // expired/missing: stays unembedded; lexical_only for it
		}
		vecs, err := e.Embed([]string{string(body)})
		d.noteEmbed(err) // D10: a failing embedder is a reportable state, not just a log line
		if err != nil {
			return done, err
		}
		if err := d.proj.InsertVector(pe.RevisionID, e.ModelID(), vecs[0]); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// ReindexSemantic: model migration = invalidate + full re-embed (rulings §7).
func (d *Daemon) ReindexSemantic() (int, error) {
	e := d.emb()
	if e == nil {
		return 0, fmt.Errorf("no embedder available: provision the embed venv (.cairn/embed-venv) or set CAIRN_EMBED_PYTHON")
	}
	stored, err := d.proj.EmbeddingModelID()
	if err != nil {
		return 0, err
	}
	if stored != "" && stored != e.ModelID() {
		if err := d.proj.InvalidateVectors(); err != nil {
			return 0, err
		}
	}
	total := 0
	for {
		n, err := d.EnrichOnce(256)
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
		total += n
	}
}

// ThreadOutput is one thread expansion (RETR-D4): every non-retracted
// message of the thread in wall order, budget-capped like every retrieval.
type ThreadOutput struct {
	InteractionID string `json:"interaction_id"`
	ThreadID      string `json:"thread_id"`
	Included      int    `json:"included"`
	Omitted       int    `json:"omitted,omitempty"`
	Payload       string `json:"payload"` // ≤ budget_chars, metadata included
	// Withheld (D3) counts messages dropped because they lie outside the
	// session's topic grant — distinct from Omitted, which is budget.
	Withheld int `json:"withheld_out_of_scope,omitempty"`
	// Budget (D4): mode, limit, tokenizer and the payload's cost in that unit.
	Budget rank.Report `json:"budget"`
}

// ThreadOptions is one thread expansion request. It became a struct with D4:
// a budget is now a mode plus a limit plus a capability ceiling, and three
// more positional ints would have been unreadable.
type ThreadOptions struct {
	ThreadID string `json:"thread_id"`
	// D4: exactly one of these; both is a refusal. Neither = unbudgeted.
	BudgetChars  int `json:"budget_chars,omitempty"`
	BudgetTokens int `json:"budget_tokens,omitempty"`
	// BudgetCeilingChars (D4 × D3), Principal and Confine are dispatch-set.
	BudgetCeilingChars int      `json:"-"`
	Principal          string   `json:"-"`
	Confine            []string `json:"-"`
}

// Thread renders a whole conversation. Until this existed an agent handed
// a reply could see that a thread existed (peek exposes thread_id) but had
// no way to read it. Bodies are quoted per line (untrusted content).
func (d *Daemon) Thread(opts ThreadOptions) (*ThreadOutput, error) {
	threadID, confine, principal := opts.ThreadID, opts.Confine, opts.Principal
	if threadID == "" {
		return nil, fmt.Errorf("thread_id is required")
	}
	spec, err := rank.NewSpec(opts.BudgetChars, opts.BudgetTokens)
	if err != nil {
		return nil, err
	}
	spec.Ceiling = opts.BudgetCeilingChars
	msgs, terr := d.proj.ThreadMessages(threadID)
	if terr != nil {
		return nil, terr
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("thread %s not found (or has no live messages)", threadID)
	}
	// D3: a thread crosses topics by construction — a reply is linked to its
	// own topics, not its parent's — so the grant is applied per MESSAGE here,
	// not per thread. The caller (dispatch) turns "everything withheld" into a
	// typed refusal; a partially-in-scope thread renders its in-scope part and
	// reports the count it withheld.
	withheld := 0
	if confined, cerr := d.confineScope(confine); cerr != nil {
		return nil, cerr
	} else if confined != nil {
		kept := msgs[:0]
		for _, m := range msgs {
			if confined[m.MessageID] {
				kept = append(kept, m)
			} else {
				withheld++
			}
		}
		msgs = kept
	}
	if len(msgs) == 0 {
		return &ThreadOutput{ThreadID: threadID, Withheld: withheld, Budget: spec.Report("")}, nil
	}
	interactionID := d.newUUID()
	header := fmt.Sprintf("# thread %s — %d message(s)\ninteraction: %s\n\n", threadID, len(msgs), interactionID)
	render := func(i int) string {
		m := msgs[i]
		reply := ""
		if m.ReplyTo != "" {
			reply = " reply-to=" + m.ReplyTo
		}
		entry := fmt.Sprintf("%d. %s from %s · %s%s\n", i+1, m.MessageID, inlineMeta(m.Sender), m.CreatedAt, reply)
		if body, err := d.store.Get(m.BodyHash); err == nil {
			entry += quoteLines(strings.TrimRight(string(body), "\n"))
		}
		return entry + "\n"
	}
	included, payload := rank.TakeWithinBudget(len(msgs), spec.Limits(),
		rank.BudgetRender{Header: header, Marker: "…truncated…\n"}, render)
	out := &ThreadOutput{
		InteractionID: interactionID, ThreadID: threadID,
		Included: included, Omitted: len(msgs) - included, Payload: payload,
		Withheld: withheld, Budget: spec.Report(payload),
	}
	so := &SearchOutput{Payload: payload, RetrievalMode: "thread"}
	for i := 0; i < included; i++ {
		so.Results = append(so.Results, RankedResult{MessageID: msgs[i].MessageID})
	}
	d.recordInteraction("thread", interactionID, threadID, out.Budget, so, "", "", "", principal)
	return out, nil
}

// Outcome binds a retrieval outcome to its interaction_id (rulings §10).
func (d *Daemon) Outcome(interactionID, outcome, messageID string) error {
	if d.tel == nil {
		return fmt.Errorf("telemetry unavailable")
	}
	return d.tel.RecordOutcome(interactionID, outcome, messageID, d.now())
}

// Telemetry exposes the store (gates report).
func (d *Daemon) Telemetry() *telemetry.Store { return d.tel }

// emb snapshots the embedder pointer (see embMu: the pointer can be swapped
// by SetEmbedderForTest while the enricher goroutine and retrieval read it).
func (d *Daemon) emb() embed.Embedder {
	d.embMu.RLock()
	defer d.embMu.RUnlock()
	return d.embedder
}

// SetEmbedderForTest swaps the embedder (enricher-death simulation).
func (d *Daemon) SetEmbedderForTest(e embed.Embedder) {
	d.embMu.Lock()
	d.embedder = e
	// a different embedder's health is not this one's (D10)
	d.embLastOK, d.embLastFail, d.embLastErr = time.Time{}, time.Time{}, ""
	d.embMu.Unlock()
}

// noteEmbed records the outcome of a real Embed call (D10). Health is "did the
// LAST call work", not a rate: an embedder subprocess that has started failing
// fails every call, and one that recovers reports healthy on its next success.
func (d *Daemon) noteEmbed(err error) {
	d.embMu.Lock()
	defer d.embMu.Unlock()
	if err == nil {
		d.embLastOK = d.now()
		return
	}
	d.embLastFail = d.now()
	d.embLastErr = err.Error()
}

// RetrievalStatus explains what search will actually do right now and, when
// that is lexical-only, WHY.
//
// D10: `lexical_only` in a search response has always been `d.emb() == nil`,
// which is NOT the ladder's rung 4 — same visible state, unrelated causes,
// opposite remedies (provision the venv vs wait out the load vs fix a broken
// embedder), and `cairn status` could not tell them apart.
type RetrievalStatus struct {
	Mode   string `json:"mode"`             // "hybrid" | "lexical_only"
	Cause  string `json:"cause,omitempty"`  // "" | "no_embedder" | "ladder_rung_4" | "embedder_failing"
	Detail string `json:"detail,omitempty"` // one line, naming the remedy
}

func (d *Daemon) RetrievalStatus() RetrievalStatus {
	e := d.emb()
	if e == nil {
		return RetrievalStatus{Mode: "lexical_only", Cause: "no_embedder",
			Detail: "no embedder configured; provision the embed venv (scripts/cairn-embed-bootstrap.sh) or set CAIRN_EMBED_PYTHON"}
	}
	if d.DegradationLevel().LexicalOnlyForced() {
		return RetrievalStatus{Mode: "lexical_only", Cause: "ladder_rung_4",
			Detail: "degradation ladder rung 4; the embedding backlog is shedding the vector query, and it clears as the enricher catches up"}
	}
	d.embMu.RLock()
	failedAt, okAt, lastErr := d.embLastFail, d.embLastOK, d.embLastErr
	d.embMu.RUnlock()
	if !failedAt.IsZero() && failedAt.After(okAt) {
		return RetrievalStatus{Mode: "lexical_only", Cause: "embedder_failing",
			Detail: fmt.Sprintf("embedder %s is configured but its last call failed; %s", e.ModelID(), lastErr)}
	}
	return RetrievalStatus{Mode: "hybrid"}
}
