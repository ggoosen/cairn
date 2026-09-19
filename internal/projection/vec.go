package projection

// D1 — sqlite-vec integration.
//
// The shape of it: the plain `vectors` table stays the SOURCE OF TRUTH and
// the brute-force cosine scan over it stays in the tree as the ORACLE (and as
// the fallback rulings §7 requires). A `vec0` virtual table is a DERIVED index
// over that table, mirrored inside the same transaction as every vector write,
// and rebuilt from `vectors` whenever the two disagree. So the vector path has
// an independent check — an equivalence test can ask both and demand the same
// answer — instead of being a silent-corruption surface.
//
// Three properties are load-bearing:
//
//  1. **Absence is not an error.** If the extension is not there, `Open`
//     succeeds, logs once, and serves from brute force. Most machines will
//     run the fast path, but the daemon must start and answer on one that
//     cannot.
//  2. **Scoping binds INSIDE the index query.** The topic/sender/thread scope
//     and the D3 capability grant are pushed into the KNN as a `rowid IN
//     (...)` constraint, so the top-K is computed over the permitted set —
//     never over the whole corpus and trimmed afterwards. A performance
//     change must not become a capability leak.
//  3. **Vectors are never compared across models.** The model id is a vec0
//     PARTITION KEY and every query constrains it, so a cross-model neighbour
//     is not merely filtered out, it is unreachable.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/embed"
)

func init() { registerVecExtension() }

// EnvVectorIndex=off forces the brute-force path on a build that HAS the
// extension. It exists for two reasons: an operator who suspects the index
// needs a way to take it out without a rebuild, and the fallback deserves to
// be exercisable on the shipping binary.
const EnvVectorIndex = "CAIRN_VECTOR_INDEX"

// vecMetaDim is the meta key recording the dimension the vec0 table was
// created at. vec0 fixes its dimension at CREATE time and we do not know it
// until the first vector arrives, so the table is created lazily and its
// dimension remembered here rather than parsed back out of the DDL.
const vecMetaDim = "vec_index_dim"

// vecState is the projection's vector-search capability.
type vecState struct {
	active bool   // vec0 is present and usable
	note   string // why not, or the extension version — surfaced by `cairn status`
}

// rowQuerier is satisfied by both *sql.DB and *sql.Tx. Which one matters: the
// projection holds ONE connection, so a *sql.DB query issued while a
// transaction is open on that connection deadlocks. Every helper below that
// can run inside a transaction takes this instead of reaching for p.db.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// VectorIndexActive reports whether the vec0 index answers vector queries
// (false = the brute-force fallback does).
func (p *Projection) VectorIndexActive() bool { return p.vec.active }

// VectorIndexNote explains the state in one operator-readable clause.
func (p *Projection) VectorIndexNote() string { return p.vec.note }

// vecProbe is the feature probe. A failure here is a normal, supported state:
// it means this build or this machine has no sqlite-vec, and brute force
// answers. It is never returned as an error.
func (p *Projection) vecProbe(disabled bool) {
	if disabled {
		p.vec = vecState{active: false, note: "disabled by " + EnvVectorIndex + "=off"}
		return
	}
	var v string
	if err := p.db.QueryRow(`SELECT vec_version()`).Scan(&v); err != nil {
		p.vec = vecState{active: false, note: "sqlite-vec unavailable (" + err.Error() + ")"}
		return
	}
	p.vec = vecState{active: true, note: "sqlite-vec " + v}
}

// vecIndexExists reports whether the vec0 table is present, and at what
// dimension we recorded it.
func vecIndexExists(q rowQuerier) (bool, int, error) {
	var n int
	if err := q.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='vec_index'`).Scan(&n); err != nil {
		return false, 0, err
	}
	if n == 0 {
		return false, 0, nil
	}
	var dim int
	err := q.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key=?`, vecMetaDim).Scan(&dim)
	if err == sql.ErrNoRows {
		return true, 0, nil // present but unrecorded: force a rebuild
	}
	return true, dim, err
}

// vecSync reconciles the derived index with `vectors` at Open. It matters
// because the capability can CHANGE between runs — a projection written by a
// build without the extension has vectors and no index; one written by a
// build with it, then run without, accumulates vectors the index never saw.
// Both are repaired by rebuilding from the source of truth.
func (p *Projection) vecSync() error {
	if !p.vec.active {
		return nil
	}
	var nVec, nMap, minDim, maxDim int
	if err := p.db.QueryRow(
		`SELECT count(*), COALESCE(min(dim),0), COALESCE(max(dim),0) FROM vectors`).Scan(&nVec, &minDim, &maxDim); err != nil {
		return err
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM vec_map`).Scan(&nMap); err != nil {
		return err
	}
	if nVec > 0 && minDim != maxDim {
		// One fixed-width index cannot hold both. This is unreachable through
		// the enrichment path (a model change invalidates first), so treat it
		// as a reason to stay on brute force rather than to guess.
		p.vec = vecState{active: false, note: fmt.Sprintf(
			"mixed vector dimensions %d..%d in the projection — run `cairn reindex --semantic`", minDim, maxDim)}
		return nil
	}
	exists, idxDim, err := vecIndexExists(p.db)
	if err != nil {
		return err
	}
	if nVec == 0 {
		if !exists && nMap == 0 {
			return nil
		}
		return p.vecRebuild(0)
	}
	if exists && idxDim == maxDim && nMap == nVec {
		return nil
	}
	return p.vecRebuild(maxDim)
}

// vecRebuild drops and refills the derived index from `vectors`, in ONE
// transaction: either the index matches the source of truth afterwards or it
// is untouched. dim=0 means "leave it dropped" (no vectors to index).
func (p *Projection) vecRebuild(dim int) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := vecDropTx(tx); err != nil {
		return err
	}
	if dim == 0 {
		return tx.Commit()
	}
	if err := vecCreateTx(tx, dim); err != nil {
		return err
	}
	// Paged so a large corpus is not materialized in memory to rebuild an
	// index whose whole purpose is not materializing a large corpus.
	last := ""
	for {
		type row struct {
			rev, model string
			vec        []byte
		}
		var batch []row
		rows, err := tx.Query(`SELECT revision_id, embedding_model_id, vec FROM vectors
			WHERE revision_id > ? ORDER BY revision_id LIMIT ?`, last, config.VecRebuildBatch)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.rev, &r.model, &r.vec); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			if err := vecPutTx(tx, r.rev, r.model, r.vec); err != nil {
				return err
			}
		}
		last = batch[len(batch)-1].rev
	}
	return tx.Commit()
}

func vecDropTx(tx *sql.Tx) error {
	if _, err := tx.Exec(`DROP TABLE IF EXISTS vec_index`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM vec_map`); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM meta WHERE key=?`, vecMetaDim)
	return err
}

func vecCreateTx(tx *sql.Tx, dim int) error {
	// PARTITION KEY on the model id is the storage-level expression of
	// "never compare across models" (schema.sql): rows of another model are
	// in another partition, not merely filtered late.
	if _, err := tx.Exec(fmt.Sprintf(
		`CREATE VIRTUAL TABLE vec_index USING vec0(
			embedding_model_id TEXT PARTITION KEY,
			embedding float[%d] distance_metric=cosine
		)`, dim)); err != nil {
		return fmt.Errorf("creating vec0 index: %w", err)
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES (?, ?)`, vecMetaDim, fmt.Sprintf("%d", dim))
	return err
}

// vecPutTx mirrors one vector into the index, idempotently. vec0 rejects
// INSERT OR REPLACE, so a re-embed of the same revision deletes first.
func vecPutTx(tx *sql.Tx, revisionID, modelID string, blob []byte) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO vec_map(revision_id) VALUES (?)`, revisionID); err != nil {
		return err
	}
	var rowid int64
	if err := tx.QueryRow(`SELECT rowid FROM vec_map WHERE revision_id=?`, revisionID).Scan(&rowid); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM vec_index WHERE rowid=?`, rowid); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO vec_index(rowid, embedding_model_id, embedding) VALUES (?,?,?)`,
		rowid, modelID, blob)
	return err
}

// vecInsertTx is the write-path hook, called from InsertVector INSIDE its
// transaction so the index cannot diverge from the vector it indexes.
func (p *Projection) vecInsertTx(tx *sql.Tx, revisionID, modelID string, blob []byte, dim int) error {
	if !p.vec.active {
		return nil
	}
	// Read the index state through the TRANSACTION, never a cached field: a
	// rolled-back create would leave a cache claiming an index that is not
	// there, and this costs one sqlite_master lookup per embedding — next to
	// nothing beside the model inference that produced the vector.
	exists, idxDim, err := vecIndexExists(tx)
	if err != nil {
		return err
	}
	if !exists {
		if err := vecCreateTx(tx, dim); err != nil {
			return err
		}
		idxDim = dim
	}
	if idxDim != dim {
		// Unreachable through enrichment (EnrichOnce refuses a foreign model
		// and ReindexSemantic invalidates first). Loud rather than silent: a
		// wrong-width vector in a fixed-width index is corruption.
		return fmt.Errorf("vector dimension %d does not match the vec0 index (%d): run `cairn reindex --semantic`",
			dim, idxDim)
	}
	return vecPutTx(tx, revisionID, modelID, blob)
}

// vecClearTx drops the index as part of InvalidateVectors' transaction: the
// source of truth is being emptied, so the derived index goes with it (and
// the next vector re-creates it at whatever dimension the new model uses).
func (p *Projection) vecClearTx(tx *sql.Tx) error {
	if !p.vec.active {
		// Still clear the bridge table: this build cannot touch vec_index,
		// but leaving a stale map behind would make the next extension-having
		// run think the index is in sync when it is not.
		_, err := tx.Exec(`DELETE FROM vec_map`)
		return err
	}
	return vecDropTx(tx)
}

// --- query -------------------------------------------------------------------

// vecHit is one scored candidate.
type vecHit struct {
	id  string
	sim float64
}

// rankVecHits imposes the ONE ordering both paths use: similarity desc,
// message id asc. Determinism here is what makes "identical top-K" a
// meaningful claim rather than a coincidence.
func rankVecHits(hits []vecHit, k int) []string {
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].sim != hits[b].sim {
			return hits[a].sim > hits[b].sim
		}
		return hits[a].id < hits[b].id
	})
	if k >= 0 && len(hits) > k {
		hits = hits[:k]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.id
	}
	return out
}

// VectorTopK returns the k head-revision message ids nearest the query
// vector, restricted to `scope` (nil = unrestricted; EMPTY = nothing, which
// is the whole point of a scope that resolved to no messages). It routes to
// the vec0 index when one is live and to the brute-force oracle otherwise;
// both answer identically.
func (p *Projection) VectorTopK(modelID string, q []float32, k int, includeRetracted bool, scope map[string]bool) ([]string, error) {
	// k above sqlite-vec's KNN ceiling is not a failure: the oracle answers.
	// Retrieval never asks for that many (FusionCandidatesVector = 100), but
	// a caller that does gets a correct answer rather than an error.
	if p.vec.active && k <= config.VectorIndexMaxK {
		return p.VectorTopKIndexed(modelID, q, k, includeRetracted, scope)
	}
	return p.VectorTopKBruteForce(modelID, q, k, includeRetracted, scope)
}

// VectorTopKBruteForce is the oracle AND the sanctioned fallback (rulings
// §7): every head vector into process memory, cosine each, sort. Exported
// because the equivalence test needs to ask it directly — it is not dead code
// in either role.
func (p *Projection) VectorTopKBruteForce(modelID string, q []float32, k int, includeRetracted bool, scope map[string]bool) ([]string, error) {
	heads, err := p.HeadVectors(modelID, includeRetracted)
	if err != nil {
		return nil, err
	}
	hits := make([]vecHit, 0, len(heads))
	for id, v := range heads {
		if scope != nil && !scope[id] {
			continue
		}
		hits = append(hits, vecHit{id: id, sim: embed.Cosine(q, v)})
	}
	return rankVecHits(hits, k), nil
}

// VectorTopKIndexed answers from the vec0 index. Exported so the equivalence
// test can force this path and compare it against the oracle above.
//
// It over-fetches and RE-SCORES in Go with the same float64 cosine the oracle
// uses, then cuts with the same comparator: vec0's KNN is exhaustive rather
// than approximate, so a superset scored identically yields an identical
// answer. The doubling retry covers the one remaining case — a tie group that
// straddles the fetch boundary, where what we fetched cannot settle the order.
func (p *Projection) VectorTopKIndexed(modelID string, q []float32, k int, includeRetracted bool, scope map[string]bool) ([]string, error) {
	if k <= 0 {
		return nil, nil
	}
	if k > config.VectorIndexMaxK {
		return nil, fmt.Errorf("k=%d exceeds the sqlite-vec KNN limit of %d", k, config.VectorIndexMaxK)
	}
	// The index is created lazily with the first vector, so an unembedded (or
	// freshly invalidated) projection legitimately has no table to query.
	exists, _, err := vecIndexExists(p.db)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	fetch := min(k+config.VectorIndexOverfetch, config.VectorIndexMaxK)
	for {
		hits, qerr := p.vecQuery(modelID, q, fetch, includeRetracted, scope)
		if qerr != nil {
			return nil, qerr
		}
		ids := rankVecHits(hits, -1)    // sorts `hits` in place; no cut yet
		settled := len(hits) < fetch || // the index is exhausted: nothing was left out
			len(hits) <= k || // fewer candidates than asked for
			hits[k-1].sim != hits[len(hits)-1].sim || // the K-boundary tie group is fully inside what we fetched
			fetch >= config.VectorIndexOverfetchMax
		if settled {
			if len(ids) > k {
				ids = ids[:k]
			}
			return ids, nil
		}
		fetch = min(fetch*2, config.VectorIndexOverfetchMax)
	}
}

// vecQuery is the KNN itself. Note where the filters live: retraction, the
// head-revision join and the caller's scope are all inside the `rowid IN`
// constraint, which sqlite-vec applies BEFORE choosing the k nearest — so the
// answer is the top-k of the permitted set, not the permitted part of the
// global top-k. The model id constrains the partition, so nothing from
// another model is reachable at all.
func (p *Projection) vecQuery(modelID string, q []float32, k int, includeRetracted bool, scope map[string]bool) ([]vecHit, error) {
	// The eligible-rowid subquery has two shapes, and the difference is not
	// cosmetic. Unscoped, it walks vec_map. Scoped, it must be DRIVEN BY the
	// scope — `message_id IN (SELECT value FROM json_each(...))` invites
	// SQLite to walk the corpus and re-evaluate the list per row, which is
	// quadratic; joining json_each as the outer table walks the scope instead.
	filter := `SELECT vm.rowid FROM vec_map vm
		JOIN messages m ON m.head_revision_id = vm.revision_id
		WHERE (m.retracted = 0 OR ?)`
	args := []any{modelID, VecBlob(q), k, boolInt(includeRetracted)}
	if scope != nil {
		ids := make([]string, 0, len(scope))
		for id, ok := range scope {
			if ok {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		b, err := json.Marshal(ids)
		if err != nil {
			return nil, err
		}
		filter = `SELECT vm.rowid FROM json_each(?) je
			CROSS JOIN messages m ON m.message_id = je.value
			CROSS JOIN vec_map vm ON vm.revision_id = m.head_revision_id
			WHERE (m.retracted = 0 OR ?)`
		args = []any{modelID, VecBlob(q), k, string(b), boolInt(includeRetracted)}
	}
	// TWO statements, deliberately. Hanging the projection joins directly off
	// the MATCH is the obvious shape, and its plan depends on indexes that
	// have nothing to do with the vector search: on a 6k corpus it measured
	// 64 ms before idx_messages_head existed and 8 ms after — i.e. one absent
	// index silently made the fast path slower than the brute force it
	// replaces. Running the KNN alone and resolving the ~160 rowids
	// afterwards keeps the vector query's cost its own.
	rows, err := p.db.Query(`
		SELECT rowid FROM vec_index
		WHERE embedding_model_id = ?
		  AND embedding MATCH ?
		  AND k = ?
		  AND rowid IN (`+filter+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("vec0 candidates: %w", err)
	}
	var rowids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		rowids = append(rowids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(rowids) == 0 {
		return nil, nil
	}
	idJSON, err := json.Marshal(rowids)
	if err != nil {
		return nil, err
	}
	// Driven by the rowid list, for the same reason as above. The vectors come
	// back too, so the candidates are re-scored with the SAME float64 cosine
	// the oracle uses rather than trusting vec0's float32 distance — that is
	// what makes "identical top-K" hold.
	rows2, err := p.db.Query(`
		SELECT m.message_id, v.vec
		FROM json_each(?) je
		CROSS JOIN vec_map vm ON vm.rowid = je.value
		CROSS JOIN messages m ON m.head_revision_id = vm.revision_id
		CROSS JOIN vectors v ON v.revision_id = vm.revision_id AND v.embedding_model_id = ?`,
		string(idJSON), modelID)
	if err != nil {
		return nil, fmt.Errorf("vec0 candidate rows: %w", err)
	}
	defer rows2.Close()
	out := make([]vecHit, 0, len(rowids))
	for rows2.Next() {
		var id string
		var blob []byte
		if err := rows2.Scan(&id, &blob); err != nil {
			return nil, err
		}
		out = append(out, vecHit{id: id, sim: embed.Cosine(q, blobVec(blob))})
	}
	return out, rows2.Err()
}
