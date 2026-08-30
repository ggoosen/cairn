package projection

// D16 (S19) — the current-state supersession surface.
//
// The relation itself is not cached anywhere. `supersessions` stores the
// assertions the log made; everything derived from them — "is this message
// still current?", "what replaced it?", "how much of the corpus has gone
// stale?" — is a QUERY over those rows joined against `messages.retracted`.
// That is deliberate and it is the D15 lesson applied: a duplicated "is
// superseded" flag would be a second copy of a mutable fact that decides what
// an agent is shown, and a missed update would be a stale fact presented as
// current with nothing necessarily noticing. A join cannot drift.
//
// The consequence worth stating: retracting the SUCCESSOR revives its
// predecessor automatically, because "superseded" was never stored — it was
// always the answer to a question, and the answer changed.

import (
	"database/sql"
	"fmt"

	"github.com/ggoosen/cairn/internal/config"
)

// liveSupersessionJoin is the one definition of "a live supersession": an
// assertion whose SUCCESSOR still exists and has not been retracted. Written
// once and referenced, because two copies of a filter are two copies that
// drift (D15).
const liveSupersessionJoin = `supersessions sp
	JOIN messages sm ON sm.message_id = sp.superseded_by_message_id AND sm.retracted = 0`

// SupersessionCensus is the consolidation pass's output: the state of the
// supersession graph as one set of counts. It is a REPORT, not a cache —
// nothing reads it back to make a ranking decision, so a census that is five
// minutes old cannot make a stale fact look current.
//
// These are the quantities BUILD-PLAN §3.4 E9 needs to compute its
// supersession-accuracy and stale-confidence metrics against a mesh that can
// express supersession at all, which is what D16 exists to make possible.
type SupersessionCensus struct {
	// Assertions is every message.supersede row projected, live or not.
	Assertions int `json:"assertions"`
	// LiveAssertions are those whose successor still exists un-retracted.
	LiveAssertions int `json:"live_assertions"`
	// LiftedAssertions are those whose successor was itself retracted, so the
	// predecessor reverted to current. Counted because a mesh where this is
	// large is a mesh where supersession is being used to correct mistakes.
	LiftedAssertions int `json:"lifted_assertions"`
	// SupersededMessages is how many distinct live (non-retracted) messages a
	// live assertion currently demotes — the size of the stale set.
	SupersededMessages int `json:"superseded_messages"`
	// CurrentMessages is how many live messages nothing supersedes.
	CurrentMessages int `json:"current_messages"`
	// MultiplySuperseded counts messages more than one live assertion replaced.
	// Two agents independently superseding one fact is normal in a mesh; a lot
	// of it is a signal worth an operator's eye.
	MultiplySuperseded int `json:"multiply_superseded"`
	// MaxChainDepth is the longest supersession chain found (A→B→C = 2 hops).
	MaxChainDepth int `json:"max_chain_depth"`
	// Cycles is how many messages sit on a supersession CYCLE — A replaced by
	// B replaced by A. Two devices can each assert their message replaced the
	// other's without either having seen the other, so this is a real mesh
	// state rather than corruption. Both ends carry the demotion, so their
	// relative order is unchanged; it is reported, never silently repaired.
	Cycles int `json:"cycles"`
}

// SupersessionCensus computes the current state of the supersession graph.
// Read-only, and cheap enough to run on a timer: every count below is a
// covered lookup through idx_supersessions_msg / idx_supersessions_by except
// the chain walk, which visits only messages that are actually superseded.
func (p *Projection) SupersessionCensus() (SupersessionCensus, error) {
	var c SupersessionCensus
	scalar := func(q string, dst *int) error { return p.db.QueryRow(q).Scan(dst) }
	for _, pair := range []struct {
		sql string
		dst *int
	}{
		{`SELECT count(*) FROM supersessions`, &c.Assertions},
		{`SELECT count(*) FROM ` + liveSupersessionJoin, &c.LiveAssertions},
		{`SELECT count(DISTINCT sp.message_id) FROM ` + liveSupersessionJoin + `
		   JOIN messages m ON m.message_id = sp.message_id AND m.retracted = 0`, &c.SupersededMessages},
		{`SELECT count(*) FROM messages m WHERE m.retracted = 0
		    AND NOT EXISTS (SELECT 1 FROM ` + liveSupersessionJoin + ` WHERE sp.message_id = m.message_id)`, &c.CurrentMessages},
		{`SELECT count(*) FROM (
		    SELECT sp.message_id FROM ` + liveSupersessionJoin + `
		     GROUP BY sp.message_id HAVING count(DISTINCT sp.superseded_by_message_id) > 1)`, &c.MultiplySuperseded},
	} {
		if err := scalar(pair.sql, pair.dst); err != nil {
			return c, err
		}
	}
	c.LiftedAssertions = c.Assertions - c.LiveAssertions

	// Chain depth and cycles: walk from every superseded message. The walk is
	// bounded by SupersessionMaxChainDepth rather than trusted to terminate,
	// because a mesh can produce a cycle and an unbounded recursive query on a
	// cycle is a hung daemon, not an error.
	rows, err := p.db.Query(`SELECT DISTINCT sp.message_id FROM ` + liveSupersessionJoin)
	if err != nil {
		return c, err
	}
	starts, err := scanIDs(rows)
	if err != nil {
		return c, err
	}
	for _, id := range starts {
		chain, cyc, err := p.supersessionChain(id)
		if err != nil {
			return c, err
		}
		if hops := len(chain) - 1; hops > c.MaxChainDepth {
			c.MaxChainDepth = hops
		}
		if cyc {
			c.Cycles++
		}
	}
	return c, nil
}

// CurrentVersion follows the supersession chain from a message to the fact that
// is current NOW, and returns the whole path so a caller can show its working.
// The first element is the message asked about and the last is the current
// version; a single-element result means nothing supersedes it.
//
// This is the "current-state view as a QUERY rather than a written file" half
// of D16 part 3: `cairn compact` writes a markdown snapshot, and this answers
// the same question per message, live, without a file to regenerate.
//
// cycled reports that the walk returned to a message it had already visited
// (or hit SupersessionMaxChainDepth), in which case the path returned is the
// prefix before the repeat. A cycle has no current version by definition, and
// saying so is better than picking one of the two arbitrarily.
func (p *Projection) CurrentVersion(messageID string) (chain []string, cycled bool, err error) {
	return p.supersessionChain(messageID)
}

func (p *Projection) supersessionChain(messageID string) ([]string, bool, error) {
	seen := map[string]bool{messageID: true}
	chain := []string{messageID}
	cur := messageID
	for depth := 0; depth < config.SupersessionMaxChainDepth; depth++ {
		var next sql.NullString
		err := p.db.QueryRow(`SELECT sp.superseded_by_message_id FROM `+liveSupersessionJoin+`
			WHERE sp.message_id = ? ORDER BY sp.valid_until, sp.event_id LIMIT 1`, cur).Scan(&next)
		if err == sql.ErrNoRows || (err == nil && !next.Valid) {
			return chain, false, nil
		}
		if err != nil {
			return chain, false, err
		}
		if seen[next.String] {
			return chain, true, nil
		}
		seen[next.String] = true
		chain = append(chain, next.String)
		cur = next.String
	}
	// The bound was reached without closing a loop or running out of
	// successors. Treated as a cycle for reporting purposes: either way the
	// answer to "what is current?" is "the graph is not telling you".
	return chain, true, nil
}

// SupersessionHistory lists every assertion recorded against one message — the
// ones still live and the ones a retracted successor lifted — so "what was true
// last March, and who said otherwise" is answerable from the projection and not
// only by replaying the log.
type SupersessionAssertion struct {
	EventID      string `json:"event_id"`
	SupersededBy string `json:"superseded_by_message_id"`
	Reason       string `json:"reason,omitempty"`
	Actor        string `json:"actor_principal_id,omitempty"`
	ValidUntil   string `json:"valid_until"`
	Live         bool   `json:"live"` // false = the successor was retracted, so this no longer ends the fact
}

// SupersessionHistory returns the assertions against messageID, oldest end date
// first. Attribution rides along: an assertion that demotes an agent's memory
// names the event and the principal that made it.
func (p *Projection) SupersessionHistory(messageID string) ([]SupersessionAssertion, error) {
	rows, err := p.db.Query(`
		SELECT sp.event_id, sp.superseded_by_message_id, sp.reason, sp.actor_principal_id, sp.valid_until,
		       EXISTS(SELECT 1 FROM messages sm WHERE sm.message_id = sp.superseded_by_message_id AND sm.retracted = 0)
		  FROM supersessions sp WHERE sp.message_id = ?
		 ORDER BY sp.valid_until, sp.event_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SupersessionAssertion
	for rows.Next() {
		var a SupersessionAssertion
		var reason, actor sql.NullString
		var live int
		if err := rows.Scan(&a.EventID, &a.SupersededBy, &reason, &actor, &a.ValidUntil, &live); err != nil {
			return nil, err
		}
		a.Reason, a.Actor, a.Live = reason.String, actor.String, live == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// SupersessionExists reports whether an identical assertion is already on
// record, so a repeated `cairn supersede` is a no-op instead of a second event
// asserting what the first one did.
func (p *Projection) SupersessionExists(messageID, by string) (bool, error) {
	var n int
	if err := p.db.QueryRow(`SELECT count(*) FROM supersessions
		WHERE message_id = ? AND superseded_by_message_id = ?`, messageID, by).Scan(&n); err != nil {
		return false, fmt.Errorf("checking supersession: %w", err)
	}
	return n > 0, nil
}
