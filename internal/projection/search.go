package projection

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/object"
)

// SearchResult is one lexical hit (head revisions only). Score is BM25
// (lower = better in SQLite's bm25()); ties break deterministically by
// message_id, then revision_id (rulings §7 determinism requirement).
type SearchResult struct {
	MessageID  string  `json:"message_id"`
	RevisionID string  `json:"revision_id"`
	BodyHash   string  `json:"body_hash"`
	TextClass  string  `json:"text_class"`
	Priority   int     `json:"declared_priority"`
	CreatedAt  string  `json:"created_at"`
	Retracted  bool    `json:"retracted"`
	Score      float64 `json:"score"`
}

// FTSQuery converts a raw user/agent query into a safe FTS5 MATCH
// expression: each whitespace-separated term is double-quoted (embedded
// quotes doubled), so FTS5 operator characters (- # @ : etc.) can never
// produce syntax errors or column filters.
//
// Semantics (D11): the terms are joined with OR, not with the implicit AND
// FTS5 applies to adjacent phrases. Conjunction made a document qualify only
// by containing EVERY term, so "what did the council decide about approval"
// returned nothing while "council approved" returned the document — the
// retriever refused to answer the question an agent actually asks. Under
// disjunction the document qualifies on any term and bm25 does the
// discriminating: it sums an idf-weighted contribution per MATCHED term, so a
// document matching more of the query (and rarer parts of it) sorts ahead of
// one matching less. Measured on a 4-document fixture: matching "council"+"the"
// scores −0.88764680 against −0.88764538 for "council" alone, and a document
// matching only "the" scores −0.00000142.
//
// A term that tokenizes to nothing (`"---"`) yields an empty phrase, which
// FTS5 matches against NO document — verified, and load-bearing: inside a
// disjunction an empty phrase must stay inert rather than admit everything.
// A query whose terms appear nowhere still returns nothing; OR is not
// match-anything.
//
// This is the raw builder. The production candidate paths call it through
// lexicalMatch, which first drops terms the index says carry no ordering
// information (see LexicalPlan).
func FTSQuery(raw string) string {
	return ftsDisjunction(strings.Fields(raw))
}

// ftsDisjunction quotes and ORs an already-selected term list.
func ftsDisjunction(fields []string) string {
	if len(fields) == 0 {
		return `""` // matches nothing (an empty phrase is inert, not universal)
	}
	quoted := make([]string, len(fields))
	for i, f := range fields {
		quoted[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " OR ")
}

// LexicalPlan records which query terms actually formed the disjunction and
// why the others did not. D11 widened matching from AND to OR; this is what
// keeps the widening inspectable rather than a black box (spec §9), and it is
// reported on every search response.
type LexicalPlan struct {
	// Terms is what was searched, in query order. Empty means no term of the
	// query is in the index at all — the honest empty answer, not a refusal.
	Terms []string `json:"terms,omitempty"`
	// Common lists terms DROPPED because they occur in MORE than
	// FTSNonDiscriminatingDocFraction of the indexed documents — past where
	// bm25's idf crosses zero, so the index itself says they cannot order
	// results. An OR over "the" is not recall, it is the whole corpus.
	Common []string `json:"common_terms,omitempty"`
	// Unmatched lists terms DROPPED because no indexed document contains them.
	// They are inert, never unwanted: a query none of whose terms match still
	// returns nothing.
	Unmatched []string `json:"unmatched_terms,omitempty"`
	// AllCommon reports the degenerate case: every matching term of the query
	// was non-discriminating, so all of them were searched ANYWAY (they appear
	// in Terms, not Common) rather than answering nothing. Also the normal state
	// of a very small mesh, where every term is in half the documents.
	AllCommon bool `json:"all_terms_common,omitempty"`
}

// ftsIndex names one FTS5 index, the rowid map that holds its document
// population, and its fts5vocab companion (D14: the document-frequency
// source). The two values below are the only ones; their names are
// compile-time constants (they are interpolated into SQL) and never caller
// input.
type ftsIndex struct{ table, mapTable, vocabTable string }

var (
	ftsRevisionsIndex   = ftsIndex{"fts_revisions", "fts_map", "fts_revisions_vocab"}
	ftsDerivativesIndex = ftsIndex{"fts_derivatives", "fts_derivatives_map", "fts_derivatives_vocab"}
)

// indexedDocs is the FTS index's document count — the N in bm25's idf.
// max(rowid) is an O(log N) b-tree lookup on the map table, whose rowids are
// assigned by INSERT and never deleted (retraction and supersession are
// filtered at query time, never by removing FTS rows), so it equals the row
// count. Should a row ever be deleted it would OVER-count, which widens the
// cutoff and therefore drops FEWER terms — the safe direction: a term is never
// discarded because the corpus was mis-measured downward.
func (p *Projection) indexedDocs(idx ftsIndex) (int, error) {
	var n sql.NullInt64
	if err := p.db.QueryRow(`SELECT max(rowid) FROM ` + idx.mapTable).Scan(&n); err != nil {
		return 0, err
	}
	return int(n.Int64), nil
}

// termDocs counts documents containing one term by enumerating matching
// rowids, stopping at limit. This was D11's probe and is now D14's FALLBACK
// and its test oracle (the way brute-force cosine remains the vector index's
// oracle — rulings §7): it answers for the terms termDF cannot fold, and the
// differential test drives lexicalMatch through it to prove the vocab path
// decides identically.
//
// Its cost is why D14 exists. `limit` is the D11 cutoff — HALF the indexed
// corpus — so a term present in most documents is scanned ~n/2 rows deep on
// every query: a bound that looks constant and is a function of corpus size.
// Measured on the 100k scorecard corpus: 2.5 ms for a term in every document,
// 0.09 ms for a term in 1%.
func (p *Projection) termDocs(idx ftsIndex, quotedTerm string, limit int) (int, error) {
	var n int
	err := p.db.QueryRow(`SELECT count(*) FROM (SELECT rowid FROM `+idx.table+
		` WHERE `+idx.table+` MATCH ? LIMIT ?)`, quotedTerm, limit).Scan(&n)
	return n, err
}

// dfSource answers "how many indexed documents contain this raw query term?"
// for one index, given the D11 cutoff (which a bounded implementation may use
// to stop early). The seam exists so the production source and the D11 probe
// that preceded it can be driven through the SAME rule — see lexicalMatchWith.
type dfSource func(idx ftsIndex, term string, limit int) (int, error)

// termDF reports how many indexed documents contain one raw query term (D14).
//
// It asks the index's fts5vocab companion, which publishes (term, doc, cnt)
// per term and seeks to a term rather than enumerating the documents that
// match it. The count it returns is the FULL df, where termDocs returns
// min(df, limit); the two are interchangeable here because lexicalMatch only
// ever asks two questions of the answer — is it zero, and is it at or above
// the cutoff — and neither can distinguish a capped count from an uncapped one.
//
// Honest about what this buys: fts5vocab is a view over the index's own
// b-tree, and `doc` is computed by walking the term's doclist, so this is a
// cheaper O(df), not O(1). Measured on the same 100k corpus: 1.9 ms against
// 2.5 ms for a term in every document, 0.04 ms against 0.09 ms for a term in
// 1%. FTS5 stores no document count per term anywhere, so an O(1) answer
// would need a df table of our own, maintained on the append path by
// tokenizing bodies in Go — which buys a millisecond by putting a second,
// divergence-prone tokenizer in front of the durability path. Not taken.
//
// Terms it cannot fold fall back to termDocs rather than guess: see
// indexToken. A term whose folded form is absent from the vocabulary also
// falls back, because "absent" is ambiguous — either the corpus has never
// seen the term (the probe agrees, and costs nothing to say so) or our
// folding disagrees with the tokenizer's (the probe is the authority).
func (p *Projection) termDF(idx ftsIndex, term string, limit int) (int, error) {
	if tok, ok := indexToken(term); ok {
		var doc sql.NullInt64
		err := p.db.QueryRow(`SELECT doc FROM `+idx.vocabTable+` WHERE term = ?`, tok).Scan(&doc)
		switch {
		case err == nil:
			return int(doc.Int64), nil
		case errors.Is(err, sql.ErrNoRows):
			// ambiguous — fall through to the probe
		default:
			return 0, err
		}
	}
	return p.termDocs(idx, ftsDisjunction([]string{term}), limit)
}

// indexToken folds one raw query term the way `unicode61 tokenchars '_-#@'`
// (config.FTSTokenize) folded the documents, so it can be looked up in the
// fts5vocab companion — which stores terms exactly as the tokenizer emitted
// them. ok=false means "do not use the vocabulary for this term"; the caller
// falls back to the bounded probe, which asks SQLite's own tokenizer by
// construction.
//
// Deliberately ASCII-only, and that is the whole safety argument. For ASCII
// the fold is exactly reproducible — A–Z lowercase, letters/digits and the
// four tokenchars are token bytes, everything else separates — with no
// dependence on which Unicode version SQLite's alphanumeric and case-folding
// tables were built from, and no dependence on `remove_diacritics`, which is
// a no-op on ASCII. A non-ASCII term is handed to the probe instead of folded
// on a guess: mis-folding would look up a DIFFERENT term's df and silently
// move a ranking decision, which is exactly what D14 must not do. The cost of
// that conservatism is that a non-ASCII query term keeps D11's old probe cost;
// the common terms that make the probe expensive in a Latin-script corpus
// ("the", "and", a project's name) are ASCII.
//
// A term that produces no token, or more than one, is also refused: an empty
// phrase matches nothing, and a multi-token phrase's df is a property of the
// tokens' ADJACENCY that no per-term vocabulary can answer.
func indexToken(term string) (string, bool) {
	var tok, cur []byte
	found := 0
	flush := func() {
		if len(cur) > 0 {
			found++
			if found == 1 {
				tok = cur
			}
			cur = nil
		}
	}
	for i := 0; i < len(term); i++ {
		c := term[i]
		if c >= utf8.RuneSelf {
			return "", false // non-ASCII: the tokenizer is the authority
		}
		switch {
		case c >= 'A' && c <= 'Z':
			cur = append(cur, c+('a'-'A'))
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.IndexByte(config.FTSTokenChars, c) >= 0:
			cur = append(cur, c)
		default:
			flush()
		}
	}
	flush()
	if found != 1 {
		return "", false
	}
	return string(tok), true
}

// lexicalMatch builds the MATCH expression for a raw query against one index,
// dropping terms the index says cannot order results (D11).
//
// The rule, in one sentence: search the terms that discriminate; if none of
// them do, search all of them anyway. Precision comes from ranking, never from
// refusing to answer — but a disjunction that includes "the" makes every
// document a candidate, and then ranking is deciding between the whole corpus
// on the strength of the one or two terms that meant something.
//
// Cost: one O(log N) count plus one df lookup per distinct term (D14 —
// termDF). Single-term queries skip the lookups entirely, which is a pure
// saving: with one term the filter can only reach the all-common fallback
// (searching that term) or drop it as unmatched (matching nothing, which
// searching it also does), so the expression is the same either way.
func (p *Projection) lexicalMatch(idx ftsIndex, raw string) (string, LexicalPlan, error) {
	return p.lexicalMatchWith(idx, raw, p.termDF)
}

// lexicalMatchWith is lexicalMatch parameterised by its document-frequency
// source. Production passes termDF (D14); the differential test passes the
// D11 probe as an oracle and asserts the two plans are identical, because
// this decision selects the candidate pool and therefore every ranking
// explanation reconciled under R47/R51. The RULE lives here exactly once, so
// the oracle can only differ in where df came from.
func (p *Projection) lexicalMatchWith(idx ftsIndex, raw string, df dfSource) (string, LexicalPlan, error) {
	fields := dedupeFields(strings.Fields(raw))
	if len(fields) <= 1 {
		return ftsDisjunction(fields), LexicalPlan{Terms: fields}, nil
	}
	n, err := p.indexedDocs(idx)
	if err != nil {
		return "", LexicalPlan{}, err
	}
	if n == 0 {
		return ftsDisjunction(fields), LexicalPlan{Terms: fields}, nil
	}
	// df ≥ cutoff ⇒ idf ≤ 0 ⇒ non-discriminating. The +1 makes it STRICTLY more
	// than the fraction, and the floor of 2 says the thing bm25's ratio cannot:
	// a term occurring in a single document names that document, so it is the
	// most specific signal available whatever the corpus size. Without the
	// floor a one-document mesh would call every term of every query common —
	// true of the arithmetic, useless as a rule.
	cutoff := int(float64(n)*config.FTSNonDiscriminatingDocFraction) + 1
	if cutoff < 2 {
		cutoff = 2
	}
	var plan LexicalPlan
	var common []string
	for _, f := range fields {
		docs, err := df(idx, f, cutoff)
		if err != nil {
			return "", LexicalPlan{}, err
		}
		switch {
		case docs == 0:
			plan.Unmatched = append(plan.Unmatched, f)
		case docs >= cutoff:
			common = append(common, f)
		default:
			plan.Terms = append(plan.Terms, f)
		}
	}
	if len(plan.Terms) == 0 && len(plan.Unmatched) == 0 && len(common) > 0 {
		// EVERY term of the query is common — "the project" against a corpus
		// that says both words everywhere. The query carries no information to
		// discriminate with, so search it as written rather than answering
		// nothing: precision comes from ranking, and bm25 still prefers the
		// documents matching more of it.
		plan.AllCommon = true
		plan.Terms = common
	} else {
		plan.Common = common
	}
	// Otherwise Terms may stay empty, and the expression matches nothing. That
	// is the honest answer to a query whose content words name nothing in the
	// corpus: falling back to its function words would return documents chosen
	// for containing "the", which is the "OR became match-anything" failure.
	// The asymmetry is deliberate — a query with nothing but common terms is
	// answered; a query that ALSO brought terms the corpus has never seen is
	// not answered on the strength of the leftovers.
	return ftsDisjunction(plan.Terms), plan, nil
}

// LexicalPlansForTest computes the D11 term-discrimination plan for a raw
// query BOTH ways — through D14's fts5vocab lookup and through the bounded
// probe that preceded it — so a test can assert the two decide identically.
// Exported for the same reason VectorTopKBruteForce is: the superseded
// implementation is the new one's oracle, and an oracle that cannot be
// addressed from a test is not an oracle. `derivatives` selects the
// attachment-text index instead of the revisions index.
func (p *Projection) LexicalPlansForTest(raw string, derivatives bool) (vocab, probe LexicalPlan, err error) {
	idx := ftsRevisionsIndex
	if derivatives {
		idx = ftsDerivativesIndex
	}
	_, vocab, err = p.lexicalMatchWith(idx, raw, p.termDF)
	if err != nil {
		return LexicalPlan{}, LexicalPlan{}, err
	}
	_, probe, err = p.lexicalMatchWith(idx, raw, func(i ftsIndex, term string, limit int) (int, error) {
		return p.termDocs(i, ftsDisjunction([]string{term}), limit)
	})
	if err != nil {
		return LexicalPlan{}, LexicalPlan{}, err
	}
	return vocab, probe, nil
}

// IndexTokenForTest exposes the query-term fold (D14) so its ASCII-only
// contract can be pinned directly, not only through its effect on a plan.
func IndexTokenForTest(term string) (string, bool) { return indexToken(term) }

// dedupeFields keeps the first occurrence of each term: a repeated term costs
// an extra df probe and tells bm25 nothing new.
func dedupeFields(fields []string) []string {
	if len(fields) < 2 {
		return fields
	}
	seen := make(map[string]bool, len(fields))
	out := fields[:0:0]
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// FTSTrigramQuery is FTSQuery's counterpart for the C2 companion index. A
// quoted term becomes a phrase of consecutive trigrams, which is what makes
// the match a SUBSTRING match rather than a token match. Terms shorter than
// the trigram width tokenize to nothing, so they are dropped rather than
// silently widening the match; "" reports that the query has nothing this
// index can answer, and the caller skips it.
func FTSTrigramQuery(raw string) string {
	var quoted []string
	for _, f := range strings.Fields(raw) {
		if utf8.RuneCountInString(f) < config.FTSTrigramMinTerm {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " ")
}

// SearchLexical runs an FTS5 MATCH over HEAD revisions. Retracted messages
// are excluded by default and included only with includeRetracted
// (capability-gated at the caller — spec §5.3).
func (p *Projection) SearchLexical(query string, k int, includeRetracted bool) ([]SearchResult, error) {
	query, _, err := p.lexicalMatch(ftsRevisionsIndex, query)
	if err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}
	rows, err := p.db.Query(`
		SELECT m.message_id, r.revision_id, r.body_hash, m.text_class, m.declared_priority,
		       m.created_at, m.retracted, bm25(fts_revisions) AS score
		FROM fts_revisions
		JOIN fts_map map ON fts_revisions.rowid = map.rowid
		JOIN revisions r ON r.revision_id = map.revision_id
		JOIN messages m ON m.message_id = r.message_id AND m.head_revision_id = r.revision_id
		WHERE fts_revisions MATCH ? AND (m.retracted = 0 OR ?)
		ORDER BY score, m.message_id, r.revision_id
		LIMIT ?`, query, boolInt(includeRetracted), k)
	if err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		var retracted int
		if err := rows.Scan(&r.MessageID, &r.RevisionID, &r.BodyHash, &r.TextClass, &r.Priority,
			&r.CreatedAt, &retracted, &r.Score); err != nil {
			return nil, err
		}
		r.Retracted = retracted == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// VisibleLinks returns the live (observed-remove) topic link set for a
// message, ordered deterministically.
func (p *Projection) VisibleLinks(messageID string) ([]string, error) {
	rows, err := p.db.Query(`SELECT topic_id FROM topic_links WHERE message_id=? AND removed=0 ORDER BY topic_id, link_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LiveLinkIDs returns the live link assertion ids for a message (observed-
// remove set membership), ordered by link_id.
func (p *Projection) LiveLinkIDs(messageID string) ([]string, error) {
	rows, err := p.db.Query(`SELECT link_id FROM topic_links WHERE message_id=? AND removed=0 ORDER BY link_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ActivePins reports whether any active pin intent exists for an object
// (spec §5.5: pinned while ANY active pin exists).
func (p *Projection) ActivePins(objectHash string) (int, error) {
	var n int
	err := p.db.QueryRow(`SELECT count(*) FROM pins WHERE object_hash=? AND removed=0`, objectHash).Scan(&n)
	return n, err
}

// ObjectRefs feeds ephemeral-TTL housekeeping (M2's object.HousekeepEphemeral):
// every revision's body reference with its message text class and creation
// time. Pinned objects are excluded (an active pin keeps the object).
func (p *Projection) ObjectRefs() ([]object.Ref, error) {
	rows, err := p.db.Query(`
		SELECT r.body_hash, m.text_class, r.created_at
		FROM revisions r JOIN messages m ON m.message_id = r.message_id
		WHERE NOT EXISTS (SELECT 1 FROM pins pn WHERE pn.object_hash = r.body_hash AND pn.removed = 0)
		ORDER BY r.body_hash, r.revision_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []object.Ref
	for rows.Next() {
		var hash, class, created string
		if err := rows.Scan(&hash, &class, &created); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("unparseable created_at %q: %w", created, err)
		}
		out = append(out, object.Ref{Hash: hash, TextClass: class, CreatedAt: t})
	}
	return out, rows.Err()
}

// EphemeralOnlyObject reports whether an object hash is known to this
// projection ONLY as ephemeral-classed content: every revision-body reference
// (message text class), attachment reference (durability class), and
// derivative-text reference (the source attachment's durability) is ephemeral,
// and at least one such reference exists. R50: such objects are never served
// to peers (get_object) and never accepted from them (put_object) — ephemeral
// bytes move only inside the live-gossip window with their event. Unknown
// hashes return false (nothing to classify).
func (p *Projection) EphemeralOnlyObject(hash string) (bool, error) {
	var revAll, revNon, attAll, attNon, derAll, derNon int
	err := p.db.QueryRow(`SELECT
		(SELECT count(*) FROM revisions r JOIN messages m ON m.message_id=r.message_id WHERE r.body_hash=?1),
		(SELECT count(*) FROM revisions r JOIN messages m ON m.message_id=r.message_id WHERE r.body_hash=?1 AND m.text_class<>'ephemeral'),
		(SELECT count(*) FROM attachments WHERE object_hash=?1),
		(SELECT count(*) FROM attachments WHERE object_hash=?1 AND durability<>'ephemeral'),
		(SELECT count(*) FROM derivatives d JOIN attachments a ON a.object_hash=d.blob_hash WHERE d.text_hash=?1),
		(SELECT count(*) FROM derivatives d JOIN attachments a ON a.object_hash=d.blob_hash WHERE d.text_hash=?1 AND a.durability<>'ephemeral')`,
		hash).Scan(&revAll, &revNon, &attAll, &attNon, &derAll, &derNon)
	if err != nil {
		return false, err
	}
	known := revAll+attAll+derAll > 0
	return known && revNon+attNon+derNon == 0, nil
}

// AppliedEvent is a projected event row (receipt regeneration).
type AppliedEvent struct {
	EventID   string
	EventType string
	Payload   []byte
}

// EventsByCorrelation returns the events created under one outbox
// request_id (correlation_id), in origin-sequence order — the idempotency
// lookup: a duplicate bundle regenerates its receipt from these (rulings §8).
func (p *Projection) EventsByCorrelation(correlationID string) ([]AppliedEvent, error) {
	rows, err := p.db.Query(`SELECT event_id, event_type, payload_json FROM events
		WHERE correlation_id=? ORDER BY origin_device_id, origin_generation, origin_sequence`, correlationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppliedEvent
	for rows.Next() {
		var e AppliedEvent
		var payload string
		if err := rows.Scan(&e.EventID, &e.EventType, &payload); err != nil {
			return nil, err
		}
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MessageInfo is peek-level metadata for one logical message.
type MessageInfo struct {
	MessageID        string `json:"message_id"`
	ThreadID         string `json:"thread_id,omitempty"`
	ReplyToMessageID string `json:"reply_to_message_id,omitempty"`
	HeadRevisionID   string `json:"head_revision_id"`
	BodyHash         string `json:"body_hash"`
	BodyLen          int64  `json:"body_len"`
	BodyMime         string `json:"body_mime"`
	TextClass        string `json:"text_class"`
	Priority         int    `json:"declared_priority"`
	Sender           string `json:"sender,omitempty"`
	CreatedAt        string `json:"created_at"`
	CreatedEventID   string `json:"created_event_id"`
	Retracted        bool   `json:"retracted"`
}

// MessageInfo returns metadata for a message and its head revision.
func (p *Projection) MessageInfo(messageID string) (*MessageInfo, error) {
	var mi MessageInfo
	var thread, reply, sender sql.NullString
	var retracted int
	err := p.db.QueryRow(`
		SELECT m.message_id, m.thread_id, m.reply_to_message_id, m.head_revision_id,
		       r.body_hash, r.body_len, r.body_mime, m.text_class, m.declared_priority,
		       m.sender_principal_id, m.created_at, m.created_event_id, m.retracted
		FROM messages m JOIN revisions r ON r.revision_id = m.head_revision_id
		WHERE m.message_id = ?`, messageID).Scan(
		&mi.MessageID, &thread, &reply, &mi.HeadRevisionID,
		&mi.BodyHash, &mi.BodyLen, &mi.BodyMime, &mi.TextClass, &mi.Priority,
		&sender, &mi.CreatedAt, &mi.CreatedEventID, &retracted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("message %s not found", messageID)
	}
	if err != nil {
		return nil, err
	}
	mi.ThreadID, mi.ReplyToMessageID, mi.Sender = thread.String, reply.String, sender.String
	mi.Retracted = retracted == 1
	return &mi, nil
}

// RevisionInfo is one revision row (export-ingest base validation).
type RevisionInfo struct {
	RevisionID string
	MessageID  string
	BodyHash   string
	BodyLen    int64
	BodyMime   string
}

// RevisionInfo looks up a revision by id.
func (p *Projection) RevisionInfo(revisionID string) (*RevisionInfo, error) {
	var ri RevisionInfo
	err := p.db.QueryRow(`SELECT revision_id, message_id, body_hash, body_len, body_mime
		FROM revisions WHERE revision_id=?`, revisionID).Scan(
		&ri.RevisionID, &ri.MessageID, &ri.BodyHash, &ri.BodyLen, &ri.BodyMime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("revision %s not found", revisionID)
	}
	if err != nil {
		return nil, err
	}
	return &ri, nil
}

// SourceRefMessage returns the message imported from a source path
// (M9 ingest idempotency lookup); "" if the path was never imported.
func (p *Projection) SourceRefMessage(path string) (string, error) {
	var id string
	err := p.db.QueryRow(`SELECT message_id FROM source_refs WHERE path=?`, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// TopicIDByName resolves a topic name; "" if absent.
func (p *Projection) TopicIDByName(name string) (string, error) {
	var id string
	err := p.db.QueryRow(`SELECT topic_id FROM topics WHERE name=?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ParkedEvent is one quarantined event (F1 ruling 3).
type ParkedEvent struct {
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	Origin    string `json:"origin"`
	Sequence  int64  `json:"sequence"`
	Error     string `json:"error"`
	ParkedAt  string `json:"parked_at"`
	// Retryable (R49): a missing intra-mesh reference that a later event may
	// satisfy (informational within R49's grace window), vs terminal corruption.
	Retryable bool `json:"retryable"`
}

// ParkedEvents lists the quarantine, oldest first.
func (p *Projection) ParkedEvents() ([]ParkedEvent, error) {
	rows, err := p.db.Query(`SELECT event_id, event_type,
		origin_device_id || '/' || origin_generation, origin_sequence, error, parked_at, retryable
		FROM parked_events ORDER BY parked_at, event_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ParkedEvent
	for rows.Next() {
		var pe ParkedEvent
		var retryable int
		if err := rows.Scan(&pe.EventID, &pe.EventType, &pe.Origin, &pe.Sequence, &pe.Error, &pe.ParkedAt, &retryable); err != nil {
			return nil, err
		}
		pe.Retryable = retryable != 0
		out = append(out, pe)
	}
	return out, rows.Err()
}

// TopicNameByID resolves a topic id; "" if absent.
func (p *Projection) TopicNameByID(id string) (string, error) {
	var name string
	err := p.db.QueryRow(`SELECT name FROM topics WHERE topic_id=?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// LinkActive reports whether a live (unremoved) link assertion exists.
func (p *Projection) LinkActive(linkID string) (bool, error) {
	var n int
	err := p.db.QueryRow(`SELECT count(*) FROM topic_links WHERE link_id=? AND removed=0`, linkID).Scan(&n)
	return n > 0, err
}

// PinActiveByID reports whether a pin intent exists and is unremoved.
func (p *Projection) PinActiveByID(pinID string) (bool, error) {
	var n int
	err := p.db.QueryRow(`SELECT count(*) FROM pins WHERE pin_id=? AND removed=0`, pinID).Scan(&n)
	return n > 0, err
}
