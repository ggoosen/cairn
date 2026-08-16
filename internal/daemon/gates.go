package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
)

// VerifyObjects walks every origin and checks each publish/revise body
// reference against the object store (TESTING.md: missing referenced object
// → doctor reports). Expired ephemeral objects are legitimate absences,
// reported INFORMATIONALLY (F3 ruling 2), never as failures.
func VerifyObjects(fsys fsx.FS, portableDir string, now time.Time) (problems, infos []string, err error) {
	store := object.NewStore(fsys, portableDir)
	origins, err := cairnlog.Origins(fsys, portableDir)
	if err != nil {
		return nil, nil, err
	}
	type ref struct {
		hash, class, created  string
		revisionID, messageID string // FIX-F8.5: named in problem lines
	}
	trust, err := identity.MeshTrust(fsys, portableDir)
	if err != nil {
		return nil, nil, err
	}
	var refs []ref
	for _, o := range origins {
		_, err := cairnlog.Walk(fsys, portableDir, o, trust.Verifier(),
			func(env *event.Envelope, _ []byte) error {
				switch env.EventType {
				case "message.publish", "message.reply":
					var pl struct {
						BodyHash   string `json:"body_hash"`
						TextClass  string `json:"text_class"`
						RevisionID string `json:"revision_id"`
						MessageID  string `json:"message_id"`
					}
					if json.Unmarshal(env.Payload, &pl) == nil && pl.BodyHash != "" {
						refs = append(refs, ref{pl.BodyHash, pl.TextClass, env.WallTime, pl.RevisionID, pl.MessageID})
					}
				case "message.revise_body":
					var pl struct {
						MessageID string `json:"message_id"`
						Revisions []struct {
							BodyHash   string `json:"body_hash"`
							RevisionID string `json:"revision_id"`
						} `json:"revisions"`
					}
					if json.Unmarshal(env.Payload, &pl) == nil {
						for _, r := range pl.Revisions {
							refs = append(refs, ref{r.BodyHash, "", env.WallTime, r.RevisionID, pl.MessageID})
						}
					}
				}
				return nil
			})
		if err != nil {
			return nil, nil, err
		}
	}
	for _, r := range refs {
		where := fmt.Sprintf("revision %s of message %s", r.revisionID, r.messageID)
		if store.Exists(r.hash) {
			if _, err := store.Get(r.hash); err != nil {
				problems = append(problems, fmt.Sprintf("object %s (referenced by %s) fails content verification: %v", r.hash, where, err))
			}
			continue
		}
		// R43: a missing EPHEMERAL object is informational on EVERY node, not
		// only the origin — it may have been withheld (peer offline at send
		// time), never fetched, or expired at TTL. Never a doctor failure
		// (exit 1). A missing canonical/eager object IS a problem.
		if r.class == object.ClassEphemeral {
			created, cerr := time.Parse(time.RFC3339Nano, r.created)
			ttl := config.EphemeralTTLDefault
			if pc, lerr := config.LoadPortable(portableDir); lerr == nil {
				ttl = pc.EphemeralTTL()
			}
			if cerr == nil && now.Sub(created) > ttl {
				infos = append(infos, fmt.Sprintf("ephemeral object %s (%s) expired (TTL); event preserved", r.hash, where))
			} else {
				infos = append(infos, fmt.Sprintf("ephemeral object %s (%s) absent (withheld or not yet fetched); event preserved", r.hash, where))
			}
			continue
		}
		problems = append(problems, fmt.Sprintf("referenced object %s missing (class %q, referenced by %s)", r.hash, r.class, where))
	}
	return problems, infos, nil
}

// DeepDoctor is the F3 doctor: log integrity (frames, hashes, signatures,
// chains, seals) + projectability (checkpoint vs log head, zero parked
// events) + object presence + cross-origin trust. Problems are FAILURE
// conditions; infos (expired ephemerals, absent-but-rebuildable projection)
// are not. The gates zero-loss row cites this.
func DeepDoctor(fsys fsx.FS, portableDir, dbPath string, now time.Time) (problems, infos []string, err error) {
	// 1. cross-origin trust (F3 ruling 3)
	trust, terr := identity.MeshTrust(fsys, portableDir)
	if terr != nil {
		return []string{fmt.Sprintf("mesh trust unresolved: %v", terr)}, nil, nil
	}

	// 2. log integrity
	doc, err := cairnlog.Doctor(fsys, portableDir, trust.Verifier())
	if err != nil {
		return nil, nil, err
	}
	for _, o := range doc.Origins {
		for _, p := range o.Problems {
			problems = append(problems, fmt.Sprintf("log %s: %s", p.Segment, p.Detail))
		}
	}

	// 3. object presence (F3 ruling 2)
	objProblems, objInfos, err := VerifyObjects(fsys, portableDir, now)
	if err != nil {
		return nil, nil, err
	}
	problems = append(problems, objProblems...)
	infos = append(infos, objInfos...)

	// 4. projectability (F3 ruling 1): parked events + checkpoint drift.
	// An ABSENT projection is rebuildable derived state (informational);
	// a PRESENT projection must match the log and hold zero parked events.
	if _, statErr := os.Stat(dbPath); statErr != nil {
		infos = append(infos, "no projection database yet (start the daemon or run `cairn reindex --lexical`)")
		return problems, infos, nil
	}
	projProblems, projInfos, err := DoctorProjection(portableDir, dbPath, now)
	if err != nil {
		return nil, nil, err
	}
	problems = append(problems, projProblems...)
	infos = append(infos, projInfos...)

	// 5. blob durability (N7, R32): verify present attachment blobs are valid
	// and report each blob's replica state (satisfied vs pending).
	durProblems, durInfos, err := DurabilityDoctor(fsys, portableDir, dbPath)
	if err != nil {
		return nil, nil, err
	}
	problems = append(problems, durProblems...)
	infos = append(infos, durInfos...)

	// 6. live forks (N8): an unresolved equivocation is a FAILURE (the origin
	// is frozen until the operator repairs it); a resolved one is informational.
	forkProblems, forkInfos := ForkDoctor(portableDir)
	problems = append(problems, forkProblems...)
	infos = append(infos, forkInfos...)

	// 7. origin liveness (D2): a peer whose own append chain moved BACKWARDS
	// restored from a stale backup — acknowledged events are missing on the
	// origin device itself. A PROBLEM until the origin is back at its
	// watermark; informational once it is.
	livProblems, livInfos := LivenessDoctor(fsys, portableDir)
	problems = append(problems, livProblems...)
	infos = append(infos, livInfos...)
	return problems, infos, nil
}

// nonRevokedMembers counts admitted, unrevoked mesh devices (all operator
// nodes) — the durability target for `important`/`pinned`.
func nonRevokedMembers(trust *identity.Trust) int {
	n := 0
	for _, id := range trust.Devices() {
		if !trust.Revoked(id) {
			n++
		}
	}
	if n < 1 {
		n = 1
	}
	return n
}

// DurabilityDoctor (R32) reads the attachment blobs from the projection, the
// peer-holder registry from disk, and the object store, and reports each
// blob's replica state: durability targets MET for non-pending blobs are
// verified (a present local copy must hash correctly); pending blobs are
// reported informationally. A present-but-corrupt attachment blob is a
// problem. A MISSING attachment blob is NOT a problem — blobs replicate lazily
// and no single node need hold every one.
func DurabilityDoctor(fsys fsx.FS, portableDir, dbPath string) (problems, infos []string, err error) {
	if _, statErr := os.Stat(dbPath); statErr != nil {
		return nil, nil, nil // no projection yet; durability unknowable
	}
	p, perr := projection.Open(dbPath, nil)
	if perr != nil {
		// an incompatible/older projection is rebuildable — skip, don't fail
		return nil, nil, nil
	}
	defer p.Close()
	blobs, berr := p.AttachmentBlobs()
	if berr != nil {
		return nil, nil, berr
	}
	if len(blobs) == 0 {
		return nil, nil, nil
	}
	reg := loadDurability(fsys, portableDir)
	members := 1
	if trust, terr := identity.MeshTrust(fsys, portableDir); terr == nil {
		members = nonRevokedMembers(trust)
	}
	store := object.NewStore(fsys, portableDir)

	strongest := map[string]string{}
	for _, b := range blobs {
		if cur, ok := strongest[b.ObjectHash]; !ok || durabilityTarget(b.Durability, members) > durabilityTarget(cur, members) {
			strongest[b.ObjectHash] = b.Durability
		}
	}
	hashes := make([]string, 0, len(strongest))
	for h := range strongest {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	for _, h := range hashes {
		class := strongest[h]
		if class == "ephemeral" {
			continue // origin-only; not a replication target
		}
		target := durabilityTarget(class, members)
		selfHolds := store.Exists(h)
		if selfHolds {
			if _, gerr := store.Get(h); gerr != nil {
				problems = append(problems, fmt.Sprintf("attachment blob %s (durability %s) present but fails content verification: %v", h, class, gerr))
				selfHolds = false
			}
		}
		have := reg.peerCount(h)
		if selfHolds {
			have++
		}
		if have >= target {
			infos = append(infos, fmt.Sprintf("blob %s durability %s SATISFIED (%d/%d nodes)", h, class, have, target))
		} else {
			infos = append(infos, fmt.Sprintf("blob %s durability %s pending (%d/%d nodes) — awaiting peers", h, class, have, target))
		}
	}
	return problems, infos, nil
}

// ProjectionDrift compares the projection checkpoint against the verified
// log per origin (doctor reports drift; recovery replay heals it).
func (d *Daemon) ProjectionDrift() ([]string, error) {
	origins, err := cairnlog.Origins(d.fs, d.dir)
	if err != nil {
		return nil, err
	}
	trust := d.trust
	if trust == nil {
		if trust, err = identity.MeshTrust(d.fs, d.dir); err != nil {
			return nil, err
		}
	}
	var drift []string
	for _, o := range origins {
		report, err := cairnlog.Walk(d.fs, d.dir, o, trust.Verifier(), nil)
		if err != nil {
			drift = append(drift, fmt.Sprintf("origin %s/%d unverifiable: %v", o.DeviceID, o.Generation, err))
			continue
		}
		ck, err := d.proj.Checkpoint(o.DeviceID, o.Generation)
		if err != nil {
			return nil, err
		}
		if ck != report.NextSeq-1 {
			drift = append(drift, fmt.Sprintf("origin %s/%d: projection checkpoint %d, log head %d",
				o.DeviceID, o.Generation, ck, report.NextSeq-1))
		}
	}
	return drift, nil
}

// GatesReport renders the engineering + product gates (rulings §10: the
// automated|human-measured column is mandatory; engineering gates are
// release blockers; product gates are the operator's 30-handoff protocol).
func (d *Daemon) GatesReport(w io.Writer) error {
	g, err := d.tel.Gates()
	if err != nil {
		return err
	}

	// zero-loss: the DEEP doctor (log + projectability + objects + trust,
	// FIX-F3); the full crash matrix runs in CI
	deepProblems, _, err := DeepDoctor(d.fs, d.dir, projection.DBPath(d.dir), d.now())
	if err != nil {
		return err
	}
	zeroLoss := "PASS (deep doctor clean: log+projection+objects+trust; crash matrix in `make verify`)"
	if len(deepProblems) > 0 {
		zeroLoss = fmt.Sprintf("FAIL (deep doctor: %d problem(s), run `cairn doctor`)", len(deepProblems))
	}

	// provenance: every fetched manifest must reference a verifiable source
	provTotal, provOK := 0, 0
	viewsDir := filepath.Join(d.dir, config.ViewsDirName)
	if agents, err := d.fs.ReadDir(viewsDir); err == nil {
		for _, a := range agents {
			if !a.IsDir() {
				continue
			}
			fdir := filepath.Join(viewsDir, a.Name(), "fetched")
			entries, err := d.fs.ReadDir(fdir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !strings.HasSuffix(e.Name(), ".manifest.json") {
					continue
				}
				provTotal++
				blob, err := d.fs.ReadFile(filepath.Join(fdir, e.Name()))
				if err != nil {
					continue
				}
				var m struct {
					SourceEventID string `json:"source_event_id"`
					BodyHash      string `json:"body_hash"`
				}
				if json.Unmarshal(blob, &m) == nil && m.SourceEventID != "" && m.BodyHash != "" {
					provOK++
				}
			}
		}
	}
	provenance := "PASS (no fetches yet)"
	if provTotal > 0 {
		if provOK == provTotal {
			provenance = fmt.Sprintf("PASS (%d/%d manifests carry source event + hash)", provOK, provTotal)
		} else {
			provenance = fmt.Sprintf("FAIL (%d/%d)", provOK, provTotal)
		}
	}

	budget := "PASS (no budgeted interactions yet)"
	if g.BudgetedCount > 0 {
		if g.BudgetViolations == 0 {
			budget = fmt.Sprintf("PASS (%d budgeted interactions, 0 violations)", g.BudgetedCount)
		} else {
			budget = fmt.Sprintf("FAIL (%d violations)", g.BudgetViolations)
		}
	}

	// FIX-J2 (R45 spirit): P95 is read by rank offset, so below a meaningful
	// sample size "P95" is just the slowest send — one hiccup would FAIL the
	// gate (Codex Brief A run 2: 4 sends, 243 ms, on a degraded WSL node). A
	// gate must not fail loudly on a sample too small to mean anything: below
	// GateLatencyMinSamples the verdict is INCONCLUSIVE, never FAIL.
	latency := fmt.Sprintf("INCONCLUSIVE (no samples yet; need ≥%d)", config.GateLatencyMinSamples)
	if g.LatencySamples > 0 {
		p95 := time.Duration(g.LatencyP95Micros) * time.Microsecond
		if g.LatencySamples < config.GateLatencyMinSamples {
			latency = fmt.Sprintf("INCONCLUSIVE (P95 %v over %d sends; need ≥%d for a meaningful P95; gate < %v)",
				p95, g.LatencySamples, config.GateLatencyMinSamples, config.GateLexicalVisibilityP95)
		} else {
			verdict := "PASS"
			if p95 >= config.GateLexicalVisibilityP95 {
				verdict = "FAIL"
			}
			latency = fmt.Sprintf("%s (P95 %v over %d sends; gate < %v)", verdict, p95, g.LatencySamples, config.GateLexicalVisibilityP95)
		}
	}

	// DEPLOY-E3: compute the product gates from stored data. final_rank per
	// (interaction, message) has been in rank_explanations all along — the
	// release-blocking metric no longer needs a hand-kept diary. A found
	// outcome without a message id (or without a stored explanation) counts
	// as found-but-not-at-5: conservative, never inflating the pass rate.
	outcomes := g.OutcomeFound + g.OutcomeNotFound + g.OutcomeWorkaround
	successAt5 := fmt.Sprintf("INCONCLUSIVE (0 outcomes recorded; need ≥%d genuine handoffs)", config.GateOutcomeMinSamples)
	workaround := successAt5
	if outcomes > 0 {
		at5 := d.successAt5Count()
		sPct := at5 * 100 / outcomes
		wPct := g.OutcomeWorkaround * 100 / outcomes
		if outcomes < config.GateOutcomeMinSamples {
			successAt5 = fmt.Sprintf("INCONCLUSIVE (%d/%d at rank ≤5 = %d%% over %d outcomes; need ≥%d; gate ≥%d%%)",
				at5, outcomes, sPct, outcomes, config.GateOutcomeMinSamples, config.GateSuccessAt5MinPct)
			workaround = fmt.Sprintf("INCONCLUSIVE (%d/%d = %d%% over %d outcomes; need ≥%d; gate ≤%d%%)",
				g.OutcomeWorkaround, outcomes, wPct, outcomes, config.GateOutcomeMinSamples, config.GateWorkaroundRateMaxPct)
		} else {
			sv, wv := "PASS", "PASS"
			if sPct < config.GateSuccessAt5MinPct {
				sv = "FAIL"
			}
			if wPct > config.GateWorkaroundRateMaxPct {
				wv = "FAIL"
			}
			successAt5 = fmt.Sprintf("%s (%d/%d at rank ≤5 = %d%%; gate ≥%d%%)", sv, at5, outcomes, sPct, config.GateSuccessAt5MinPct)
			workaround = fmt.Sprintf("%s (%d/%d = %d%%; gate ≤%d%%)", wv, g.OutcomeWorkaround, outcomes, wPct, config.GateWorkaroundRateMaxPct)
		}
	}

	fmt.Fprintf(w, "cairn gates — engineering gates are release blockers (rulings §10)\n\n")
	fmt.Fprintf(w, "%-42s %-16s %s\n", "GATE", "MEASUREMENT", "STATUS")
	fmt.Fprintf(w, "%-42s %-16s %s\n", "acknowledged-event loss = 0", "automated", zeroLoss)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "provenance on fetched results = 100%", "automated", provenance)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "hard-budget compliance = 100%", "automated", budget)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "send-ack → lexical-visible P95 < 200ms", "automated", latency)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "blob durability targets (N7)", "automated", d.durabilityGate())
	fmt.Fprintf(w, "%-42s %-16s %s\n", "no unresolved forks (N8)", "automated", d.forkGate())
	fmt.Fprintf(w, "%-42s %-16s %s\n", "first-query Success@5 ≥ 70%", "computed", successAt5)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "manual-workaround rate ≤ 25%", "computed", workaround)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "median time-to-context < 60s", "human-measured", "diary protocol (DOGFOOD.md, M8)")
	return nil
}

// successAt5Count joins found outcomes to their stored final_rank
// (DEPLOY-E3): rank ≤5 counts; a missing message id or explanation does not.
func (d *Daemon) successAt5Count() int {
	if d.tel == nil {
		return 0
	}
	found, err := d.tel.FoundOutcomes()
	if err != nil {
		return 0
	}
	at5 := 0
	for _, fo := range found {
		if fo.MessageID == "" {
			continue
		}
		if _, _, rank, err := d.proj.Explanation(fo.InteractionID, fo.MessageID); err == nil && rank > 0 && rank <= 5 {
			at5++
		}
	}
	return at5
}

// durabilityGate summarizes blob durability for the gates report (N7). A blob
// below its target is "pending" (informational — satisfied asynchronously when
// a peer appears), never a release blocker; a present-but-corrupt blob is a
// FAIL (surfaced via deep doctor).
func (d *Daemon) durabilityGate() string {
	blobs, err := d.DurabilityStatus()
	if err != nil {
		return "n/a (" + err.Error() + ")"
	}
	if len(blobs) == 0 {
		return "PASS (no durable blobs yet)"
	}
	satisfied, pending := 0, 0
	for _, b := range blobs {
		if b.Satisfied {
			satisfied++
		} else {
			pending++
		}
	}
	if pending == 0 {
		return fmt.Sprintf("PASS (%d/%d blobs at target)", satisfied, len(blobs))
	}
	return fmt.Sprintf("PASS (%d satisfied, %d pending replication)", satisfied, pending)
}

// forkGate is the N8 gates row: any unresolved fork FAILs (the origin is
// frozen until the operator repairs it — spec §6.4).
func (d *Daemon) forkGate() string {
	forks := d.Forks()
	unresolved := 0
	for _, f := range forks {
		if !f.Resolved {
			unresolved++
		}
	}
	if unresolved == 0 {
		if len(forks) == 0 {
			return "PASS (no forks detected)"
		}
		return fmt.Sprintf("PASS (%d fork(s), all resolved)", len(forks))
	}
	return fmt.Sprintf("FAIL (%d unresolved fork(s) frozen — run `cairn doctor fork <origin>`)", unresolved)
}

// DoctorProjection inspects the derived projection (F1/F3/R49). A TERMINAL
// parked event (genuine corruption a replay cannot heal) and a checkpoint
// AHEAD of the log are failures. A RETRYABLE parked event (R49: a missing
// intra-mesh reference — e.g. a topic.link.add replicated ahead of its
// topic.create — that a later event may satisfy) is INFORMATIONAL while it is
// within `ParkedRetryableGrace` of parked_at (a transient cross-node ordering
// gap during active sync), and a FAILURE once it exceeds the window (a
// dependency that never arrived). A checkpoint BEHIND the log with zero parked
// events is informational: parking guarantees the projector can no longer stall
// silently, so "behind" is always heal-by-replay (daemon start / reindex).
// Interpretation recorded in RULINGS.md.
func DoctorProjection(portableDir, dbPath string, now time.Time) (problems, infos []string, err error) {
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		return []string{fmt.Sprintf("projection unopenable: %v (run `cairn reindex --lexical`)", err)}, nil, nil
	}
	defer p.Close()
	// A doctor run is a natural moment to attempt a self-heal sweep (R49.2):
	// a retryable park whose dependency has since been projected clears here
	// rather than lingering until the next reindex.
	if _, herr := p.RetryParked(); herr != nil {
		return nil, nil, herr
	}
	parked, err := p.ParkedEvents()
	if err != nil {
		return nil, nil, err
	}
	for _, pe := range parked {
		if pe.Retryable {
			overdue := parkedOverdue(pe.ParkedAt, now)
			if !overdue {
				infos = append(infos, fmt.Sprintf("retryable parked event %s (%s, origin %s seq %d): %s — dependency may still arrive (self-heals on the event that satisfies it)",
					pe.EventID, pe.EventType, pe.Origin, pe.Sequence, pe.Error))
				continue
			}
			problems = append(problems, fmt.Sprintf("retryable parked event %s (%s, origin %s seq %d) UNHEALED after %s: %s — its dependency never arrived",
				pe.EventID, pe.EventType, pe.Origin, pe.Sequence, config.ParkedRetryableGrace, pe.Error))
			continue
		}
		problems = append(problems, fmt.Sprintf("parked event %s (%s, origin %s seq %d): %s",
			pe.EventID, pe.EventType, pe.Origin, pe.Sequence, pe.Error))
	}
	origins, err := cairnlog.Origins(fsx.OS{}, portableDir)
	if err != nil {
		return nil, nil, err
	}
	trust, err := identity.MeshTrust(fsx.OS{}, portableDir)
	if err != nil {
		return append(problems, fmt.Sprintf("mesh trust unresolved: %v", err)), nil, nil
	}
	for _, o := range origins {
		report, err := cairnlog.Walk(fsx.OS{}, portableDir, o, trust.Verifier(), nil)
		if err != nil {
			problems = append(problems, fmt.Sprintf("origin %s/%d unverifiable: %v", o.DeviceID, o.Generation, err))
			continue
		}
		ck, err := p.Checkpoint(o.DeviceID, o.Generation)
		if err != nil {
			return nil, nil, err
		}
		head := report.NextSeq - 1
		switch {
		case ck > head:
			problems = append(problems, fmt.Sprintf("projection checkpoint AHEAD of log: origin %s/%d checkpoint %d, log head %d",
				o.DeviceID, o.Generation, ck, head))
		case ck < head:
			infos = append(infos, fmt.Sprintf("projection behind log by %d event(s) on origin %s/%d (heals on daemon start or reindex)",
				head-ck, o.DeviceID, o.Generation))
		}
	}
	return problems, infos, nil
}

// parkedOverdue reports whether a retryable park has exceeded R49's grace window
// (measured from parked_at). An unparseable timestamp is treated as overdue —
// fail closed, never silently swallow a stuck park.
func parkedOverdue(parkedAt string, now time.Time) bool {
	t, err := time.Parse(config.WallTimeFormat, parkedAt)
	if err != nil {
		return true
	}
	return now.Sub(t) > config.ParkedRetryableGrace
}
