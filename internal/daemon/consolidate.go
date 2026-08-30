package daemon

// D16 (S19) part 3 — the consolidation pass.
//
// Every comparable system has a consolidation stage and Cairn had none; its
// only answer to memory going stale was a freshness half-life, which handles
// AGE but not WRONGNESS. This is the stage, and it is deliberately the smallest
// thing that earns the name.
//
// WHAT IT DOES: recomputes the supersession graph's current state on a timer
// and records the census. That is the whole pass.
//
// WHAT IT DOES NOT DO, on purpose:
//
//   - It does not rewrite, summarise, merge or delete stored memory. §3.7
//     forbids it and R47/R51 make it impossible — a model's rewrite cannot be
//     reconciled by an external verifier, so a ranking built on one is the
//     black box §9 exists to forbid. Consolidation computes RELATIONS and
//     RANKINGS over immutable content. The sanctioned place for distillation
//     stays the agent-side handoff note (C1).
//   - It does not INFER supersession. Nothing here decides that message B
//     probably replaced message A; an assertion enters the graph only as a
//     signed message.supersede event somebody wrote. Inference is exactly the
//     part BUILD-PLAN's sequencing note says to let E9 size, and E9 sits behind
//     an operator gate.
//   - It does not cache anything ranking reads. The census is a REPORT. The
//     "is this superseded?" question every search asks is answered by a join
//     at query time (internal/projection/supersede.go), so a census that is
//     minutes stale cannot make a stale fact look current.
//
// It rides the enricher's goroutine — rulings §6, background work never blocks
// an agent — on its own, much slower cadence, because the graph only changes
// when a supersession event is applied.

import (
	"fmt"
	"time"

	"github.com/ggoosen/cairn/internal/projection"
)

// consolidationState is the last completed pass, kept in memory only. It is
// derived from the projection, which is derived from the log, so persisting it
// would be a third copy of a fact two places already hold.
type consolidationState struct {
	census projection.SupersessionCensus
	at     time.Time
	err    string
}

// ConsolidateOnce runs one consolidation pass and returns the census. Exported
// so it can be driven deterministically from a test and from `cairn status`
// rather than only by the timer — a background pass nothing can trigger is a
// background pass nothing can verify.
func (d *Daemon) ConsolidateOnce() (projection.SupersessionCensus, error) {
	c, err := d.proj.SupersessionCensus()
	st := consolidationState{census: c, at: d.now()}
	if err != nil {
		st.err = err.Error()
	}
	d.mu.Lock()
	d.consolidation = st
	d.mu.Unlock()
	if err != nil {
		return c, err
	}
	// A cycle is a real mesh state (two devices each superseding the other's
	// message without having seen it), not corruption — so it is announced, not
	// parked, and doctor is left alone. R45's discipline applies to the
	// announcement: a subsystem that finds something says so, with the remedy.
	if c.Cycles > 0 {
		fmt.Fprintf(d.warn, "consolidation: %d message(s) sit on a supersession CYCLE — each end demotes the other, so neither is current; resolve by retracting the wrong successor or superseding both with one message\n", c.Cycles)
	}
	return c, nil
}

// Consolidation reports the last completed pass (zero-valued before the first).
func (d *Daemon) Consolidation() (projection.SupersessionCensus, time.Time, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.consolidation.census, d.consolidation.at, d.consolidation.err
}

// Supersede records that superseder replaced messageID: the superseded fact
// gets an END DATE (this event's wall time) and nothing is deleted or
// rewritten. Both messages must exist locally, neither may be the other, and a
// duplicate assertion is a no-op rather than a second event saying the same
// thing.
//
// Refusing to supersede with a RETRACTED message is deliberate: a retracted
// successor does not end anything (the ranking join already filters it), so
// accepting the event would write an assertion that is dead on arrival.
func (d *Daemon) Supersede(messageID, supersededBy, reason string, req PublishRequest) (string, error) {
	if messageID == "" || supersededBy == "" {
		return "", fmt.Errorf("supersede: both the superseded message and its successor are required")
	}
	if messageID == supersededBy {
		return "", fmt.Errorf("supersede: a message cannot supersede itself (%s)", messageID)
	}
	old, err := d.proj.MessageInfo(messageID)
	if err != nil {
		return "", fmt.Errorf("supersede: superseded message: %w", err)
	}
	newer, err := d.proj.MessageInfo(supersededBy)
	if err != nil {
		return "", fmt.Errorf("supersede: superseding message: %w", err)
	}
	if newer.Retracted {
		return "", fmt.Errorf("supersede: %s is retracted and cannot supersede anything", supersededBy)
	}
	if old.Retracted {
		return "", fmt.Errorf("supersede: %s is retracted; retraction already removes it from retrieval", messageID)
	}
	exists, err := d.proj.SupersessionExists(messageID, supersededBy)
	if err != nil {
		return "", err
	}
	if exists {
		return "", fmt.Errorf("supersede: %s is already recorded as superseded by %s", messageID, supersededBy)
	}
	payload := map[string]any{
		"message_id":               messageID,
		"superseded_by_message_id": supersededBy,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	return d.SimpleEvent("message.supersede", "message", messageID, payload, req)
}
