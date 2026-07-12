package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/embed"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/rank"
	"github.com/ggoosen/cairn/internal/telemetry"
)

// SearchOptions is one search invocation (budget over the COMPLETE payload).
type SearchOptions struct {
	Query            string `json:"query"`
	K                int    `json:"k,omitempty"`
	BudgetChars      int    `json:"budget_chars,omitempty"`
	IncludeRetracted bool   `json:"include_retracted,omitempty"`

	// attribution (rulings §10); missing values are inferred and flagged
	TaskID          string `json:"task_id,omitempty"`
	AgentSurface    string `json:"agent_surface,omitempty"`
	AgentInstanceID string `json:"agent_instance_id,omitempty"`

	// Principal hierarchy (N2). Set by dispatch from the capability gate —
	// any client-supplied value is overwritten there.
	Principal string `json:"principal,omitempty"`
}

// SearchOutput carries the ranked results plus the budget-compliant payload.
type SearchOutput struct {
	InteractionID string         `json:"interaction_id"`
	RetrievalMode string         `json:"retrieval_mode"` // full | lexical_only
	Results       []RankedResult `json:"results"`
	Payload       string         `json:"payload"` // ≤ budget_chars Unicode scalars, metadata included
	Omitted       int            `json:"omitted,omitempty"`
}

// RankedResult is one scored hit.
type RankedResult struct {
	Rank       int     `json:"rank"`
	MessageID  string  `json:"message_id"`
	RevisionID string  `json:"revision_id"`
	BodyHash   string  `json:"body_hash"`
	TextClass  string  `json:"text_class"`
	Score      float64 `json:"score"`
	Mandatory  string  `json:"mandatory,omitempty"`
}

// componentsRecord is the stored why_ranked arithmetic (decimal strings).
type componentsRecord struct {
	R       string `json:"R"`
	F       string `json:"F"`
	Peff    string `json:"P_eff"`
	RRF     string `json:"RRF"`
	LexRank int    `json:"lex_rank"`
	VecRank int    `json:"vec_rank"`
	Score   string `json:"score"`
	Weights struct {
		R string `json:"R"`
		F string `json:"F"`
		P string `json:"P"`
	} `json:"weights"`
	CreatedAt string `json:"created_at"`
	Mandatory string `json:"mandatory,omitempty"`
}

// Search: FTS top-100 + vector top-100 → RRF k=60 → percentile → P0 search
// profile → budget-truncate (rulings §7). Never fails for lack of
// embeddings — degrades to lexical_only.
func (d *Daemon) Search(opts SearchOptions) (*SearchOutput, error) {
	if opts.K <= 0 {
		opts.K = 10
	}
	lexIDs, err := d.proj.LexicalTopK(opts.Query, config.FusionCandidatesFTS, opts.IncludeRetracted)
	if err != nil {
		return nil, err
	}

	mode := "lexical_only"
	vecIDs := []string(nil)
	if d.embedder != nil {
		if qvecs, err := d.embedder.Embed([]string{opts.Query}); err == nil {
			heads, herr := d.proj.HeadVectors(d.embedder.ModelID(), opts.IncludeRetracted)
			if herr == nil && len(heads) > 0 {
				vecIDs = topKCosine(heads, qvecs[0], config.FusionCandidatesVector)
				mode = "full"
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
		cands = append(cands, *c)
	}

	scored := rank.Rank(cands, rank.ProfileSearch, d.now())
	if len(scored) > opts.K {
		scored = scored[:opts.K]
	}
	out, err := d.finishRetrieval(scored, rows, rank.ProfileSearch, mode, opts.BudgetChars)
	if err != nil {
		return nil, err
	}
	d.recordInteraction("search", out.InteractionID, opts.Query, opts.BudgetChars, out, opts.TaskID, opts.AgentSurface, opts.AgentInstanceID, opts.Principal)
	return out, nil
}

// recordInteraction logs telemetry (local-only; never an event). Missing
// attribution is daemon-inferred and flagged (rulings §10).
func (d *Daemon) recordInteraction(kind, interactionID, query string, budget int, out *SearchOutput, taskID, surface, instance, principal string) {
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
		Inferred: inferred, Query: query, BudgetRequested: budget,
		PayloadChars: rank.BudgetChars(out.Payload), ResultCount: len(out.Results),
		RetrievalMode: out.RetrievalMode, CreatedAt: d.now(), ResultIDs: ids,
	}
	if err := d.tel.Record(it); err != nil {
		fmt.Fprintf(d.warn, "WARNING: telemetry: %v\n", err)
	}
}

// finishRetrieval renders the budget-compliant payload, stores why_ranked
// inputs, and assembles the output. Budget covers the ENTIRE payload —
// header, entries, truncation marker (rulings §7).
func (d *Daemon) finishRetrieval(scored []rank.Scored, rows map[string]projection.RankRow, profile rank.Profile, mode string, budget int) (*SearchOutput, error) {
	interactionID := d.newUUID()

	render := func(i int) string {
		s := scored[i]
		row := rows[s.MessageID]
		m := s.Mandatory
		if m == "" {
			m = "-"
		}
		return fmt.Sprintf("%d\t%s\t%s\t%s\t%s\t%s\n",
			i+1, s.MessageID, row.HeadRevisionID, row.BodyHash, rank.Dec(s.Score), m)
	}
	header := fmt.Sprintf("interaction\t%s\tmode\t%s\n", interactionID, mode)
	included := len(scored)
	payload := header
	for i := range scored {
		payload += render(i)
	}
	if budget > 0 {
		included, payload = rank.TakeWithinBudget(len(scored), budget,
			rank.BudgetRender{Header: header, Marker: "TRUNCATED\n"}, render)
	}

	out := &SearchOutput{
		InteractionID: interactionID,
		RetrievalMode: mode,
		Payload:       payload,
		Omitted:       len(scored) - included,
	}
	var expl []projection.ExplanationRow
	for i := 0; i < included; i++ {
		s := scored[i]
		row := rows[s.MessageID]
		out.Results = append(out.Results, RankedResult{
			Rank: i + 1, MessageID: s.MessageID, RevisionID: row.HeadRevisionID,
			BodyHash: row.BodyHash, TextClass: row.TextClass, Score: s.Score, Mandatory: s.Mandatory,
		})
		var rec componentsRecord
		rec.R, rec.F, rec.Peff, rec.RRF = rank.Dec(s.R), rank.Dec(s.F), rank.Dec(s.Peff), rank.Dec(s.RRF)
		rec.LexRank, rec.VecRank = s.Components.LexRank, s.Components.VecRank
		rec.Score = rank.Dec(s.Score)
		wR, wF, wP := profileWeights(profile)
		rec.Weights.R, rec.Weights.F, rec.Weights.P = rank.Dec(wR), rank.Dec(wF), rank.Dec(wP)
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

func profileWeights(p rank.Profile) (float64, float64, float64) {
	if p == rank.ProfileDigest {
		return config.DigestWeightR, config.DigestWeightF, config.DigestWeightP
	}
	return config.SearchWeightR, config.SearchWeightF, config.SearchWeightP
}

func topKCosine(heads map[string][]float32, q []float32, k int) []string {
	type hit struct {
		id  string
		sim float64
	}
	hits := make([]hit, 0, len(heads))
	for id, v := range heads {
		hits = append(hits, hit{id, embed.Cosine(q, v)})
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].sim != hits[b].sim {
			return hits[a].sim > hits[b].sim
		}
		return hits[a].id < hits[b].id
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.id
	}
	return out
}

// --- digest ------------------------------------------------------------------

// ViewConfig is the LOCAL, non-event digest configuration (rulings §7):
// hard topic filters + optional natural-language interest query.
type ViewConfig struct {
	Topics        []string `json:"topics,omitempty"`
	InterestQuery string   `json:"interest_query,omitempty"`
}

// DigestOptions parameterizes one digest generation.
type DigestOptions struct {
	AgentView   string `json:"agent_view"`
	BudgetChars int    `json:"budget_chars"`
	TaskID      string `json:"task_id,omitempty"`
	Principal   string `json:"principal,omitempty"` // dispatch-resolved (N2)
}

// DigestOutput is the generated digest.
type DigestOutput struct {
	InteractionID    string `json:"interaction_id"`
	Path             string `json:"path"`
	Payload          string `json:"payload"`
	Included         int    `json:"included"`
	OmittedMandatory int    `json:"omitted_mandatory_count"`
	RetrievalMode    string `json:"retrieval_mode"`
}

// Digest generates views/<agent>/digest.md: candidates pass the view's hard
// filters; R is hybrid relevance vs the interest query (no query ⇒ R=1.0);
// mandatory items (explicit recipients, then pins) come first and consume
// budget; overflow drops oldest-first and is reported (rulings §7).
func (d *Daemon) Digest(opts DigestOptions) (*DigestOutput, error) {
	if opts.AgentView == "" || strings.ContainsAny(opts.AgentView, "/\\") || strings.Contains(opts.AgentView, "..") {
		return nil, fmt.Errorf("invalid agent view %q", opts.AgentView)
	}
	if opts.BudgetChars <= 0 {
		return nil, fmt.Errorf("digest requires budget_chars > 0 (budget_tokens is unsupported_in_P0)")
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
		if d.embedder != nil {
			if qvecs, err := d.embedder.Embed([]string{cfg.InterestQuery}); err == nil {
				heads, herr := d.proj.HeadVectors(d.embedder.ModelID(), false)
				if herr == nil && len(heads) > 0 {
					for i, id := range topKCosine(heads, qvecs[0], config.FusionCandidatesVector) {
						vecRank[id] = i + 1
					}
					mode = "full"
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
	cands := make([]rank.Candidate, 0, len(candIDs))
	for _, id := range candIDs {
		row, ok := rows[id]
		if !ok {
			continue
		}
		cands = append(cands, rank.Candidate{
			MessageID: id,
			EventID:   row.CreatedEventID,
			CreatedAt: parseWall(row.CreatedAt),
			Priority:  row.Priority,
			Suspended: row.PinActive || row.PriorityConf,
			LexRank:   lexRank[id],
			VecRank:   vecRank[id],
			Mandatory: mandatory[id],
		})
	}

	var scored []rank.Scored
	if uniform {
		scored = rank.RankUniformR(cands, rank.ProfileDigest, d.now())
	} else {
		scored = rank.Rank(cands, rank.ProfileDigest, d.now())
	}

	interactionID := d.newUUID()
	header := fmt.Sprintf("# digest — %s\ninteraction: %s\nmode: %s\n\n", opts.AgentView, interactionID, mode)
	render := func(i int) string { return d.renderDigestEntry(i+1, scored[i], rows[scored[i].MessageID]) }
	included, payload := rank.TakeWithinBudget(len(scored), opts.BudgetChars,
		rank.BudgetRender{Header: header, Marker: "…truncated…\n"}, render)

	// mandatory overflow accounting (drop-oldest-first is the sort order:
	// within a mandatory class newest sorts last by wall time? No — rank
	// sorts newer first, so TakeWithinBudget keeps newest and drops oldest)
	omitted := 0
	for i := included; i < len(scored); i++ {
		if scored[i].Mandatory != "" {
			omitted++
		}
	}

	path := filepath.Join(d.dir, config.ViewsDirName, opts.AgentView, "digest.md")
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
		wR, wF, wP := profileWeights(rank.ProfileDigest)
		rec.Weights.R, rec.Weights.F, rec.Weights.P = rank.Dec(wR), rank.Dec(wF), rank.Dec(wP)
		rec.CreatedAt = rows[s.MessageID].CreatedAt
		rec.Mandatory = s.Mandatory
		blob, _ := json.Marshal(rec)
		expl = append(expl, projection.ExplanationRow{MessageID: s.MessageID, ComponentsJSON: string(blob), FinalRank: i + 1})
	}
	if err := d.proj.SaveExplanations(interactionID, string(rank.ProfileDigest), expl); err != nil {
		return nil, err
	}
	dout := &DigestOutput{
		InteractionID:    interactionID,
		Path:             path,
		Payload:          payload,
		Included:         included,
		OmittedMandatory: omitted,
		RetrievalMode:    mode,
	}
	so := &SearchOutput{Results: nil, Payload: payload, RetrievalMode: mode, InteractionID: interactionID}
	for i := 0; i < included; i++ {
		so.Results = append(so.Results, RankedResult{MessageID: scored[i].MessageID})
	}
	d.recordInteraction("digest", interactionID, cfg.InterestQuery, opts.BudgetChars, so, opts.TaskID, opts.AgentView, "", opts.Principal)
	return dout, nil
}

// renderDigestEntry: one digest item; EVERY line quoting cairn content is
// prefixed with config.QuotePrefix (per-line prefixing cannot be escaped).
func (d *Daemon) renderDigestEntry(pos int, s rank.Scored, row projection.RankRow) string {
	var b strings.Builder
	tag := ""
	if s.Mandatory != "" {
		tag = " [" + s.Mandatory + "]"
	}
	fmt.Fprintf(&b, "%d. %s%s score=%s\n", pos, s.MessageID, tag, rank.Dec(s.Score))
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
	blob, err := d.fs.ReadFile(filepath.Join(d.dir, config.ViewsDirName, agent, "view.json"))
	if err == nil {
		json.Unmarshal(blob, &cfg)
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
	fmt.Fprintf(&b, "  R     %s × %s = %s   (lex rank %d, vec rank %d, RRF %s)\n",
		rec.R, rec.Weights.R, rank.Dec(rank.ParseDec(rec.R)*rank.ParseDec(rec.Weights.R)), rec.LexRank, rec.VecRank, rec.RRF)
	fmt.Fprintf(&b, "  F     %s × %s = %s   (created %s)\n",
		rec.F, rec.Weights.F, rank.Dec(rank.ParseDec(rec.F)*rank.ParseDec(rec.Weights.F)), rec.CreatedAt)
	fmt.Fprintf(&b, "  P_eff %s × %s = %s\n",
		rec.Peff, rec.Weights.P, rank.Dec(rank.ParseDec(rec.Peff)*rank.ParseDec(rec.Weights.P)))
	if rec.Mandatory != "" {
		fmt.Fprintf(&b, "  mandatory: %s\n", rec.Mandatory)
	}
	fmt.Fprintf(&b, "  total %s\n", rec.Score)
	return b.String(), nil
}

// --- enrichment ---------------------------------------------------------------

// EnrichOnce embeds up to batch pending revisions (background enricher body;
// rulings §6: agents never wait on this). Returns how many were embedded.
func (d *Daemon) EnrichOnce(batch int) (int, error) {
	if d.embedder == nil {
		return 0, nil // lexical_only until an embedder is provisioned
	}
	stored, err := d.proj.EmbeddingModelID()
	if err != nil {
		return 0, err
	}
	if stored != "" && stored != d.embedder.ModelID() {
		return 0, fmt.Errorf("stored vectors belong to model %q, embedder is %q: run `cairn reindex --semantic` to migrate", stored, d.embedder.ModelID())
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
		vecs, err := d.embedder.Embed([]string{string(body)})
		if err != nil {
			return done, err
		}
		if err := d.proj.InsertVector(pe.RevisionID, d.embedder.ModelID(), vecs[0]); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// ReindexSemantic: model migration = invalidate + full re-embed (rulings §7).
func (d *Daemon) ReindexSemantic() (int, error) {
	if d.embedder == nil {
		return 0, fmt.Errorf("no embedder available: provision the embed venv (.cairn/embed-venv) or set CAIRN_EMBED_PYTHON")
	}
	stored, err := d.proj.EmbeddingModelID()
	if err != nil {
		return 0, err
	}
	if stored != "" && stored != d.embedder.ModelID() {
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

// Outcome binds a retrieval outcome to its interaction_id (rulings §10).
func (d *Daemon) Outcome(interactionID, outcome, messageID string) error {
	if d.tel == nil {
		return fmt.Errorf("telemetry unavailable")
	}
	return d.tel.RecordOutcome(interactionID, outcome, messageID, d.now())
}

// Telemetry exposes the store (gates report).
func (d *Daemon) Telemetry() *telemetry.Store { return d.tel }

// SetEmbedderForTest swaps the embedder (enricher-death simulation).
func (d *Daemon) SetEmbedderForTest(e embed.Embedder) { d.embedder = e }
