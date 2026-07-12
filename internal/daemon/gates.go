package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
		created, err := time.Parse(time.RFC3339Nano, r.created)
		ttl := config.EphemeralTTLDefault
		if pc, cerr := config.LoadPortable(portableDir); cerr == nil {
			ttl = pc.EphemeralTTL()
		}
		expired := err == nil && r.class == object.ClassEphemeral && now.Sub(created) > ttl
		if expired {
			infos = append(infos, fmt.Sprintf("ephemeral object %s (%s) expired (TTL); event preserved", r.hash, where))
		} else {
			problems = append(problems, fmt.Sprintf("referenced object %s missing (class %q, referenced by %s) — not explainable by ephemeral expiry", r.hash, r.class, where))
		}
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
	projProblems, projInfos, err := DoctorProjection(portableDir, dbPath)
	if err != nil {
		return nil, nil, err
	}
	problems = append(problems, projProblems...)
	infos = append(infos, projInfos...)
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
	zeroLoss := "PASS (deep doctor clean: log+projection+objects+trust; crash matrix in CI)"
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

	latency := "n/a (no samples)"
	latencyPass := ""
	if g.LatencySamples > 0 {
		p95 := time.Duration(g.LatencyP95Micros) * time.Microsecond
		latencyPass = "PASS"
		if p95 >= config.GateLexicalVisibilityP95 {
			latencyPass = "FAIL"
		}
		latency = fmt.Sprintf("%s (P95 %v over %d sends; gate < %v)", latencyPass, p95, g.LatencySamples, config.GateLexicalVisibilityP95)
	}

	outcomes := g.OutcomeFound + g.OutcomeNotFound + g.OutcomeWorkaround
	successAt5 := "pending (0 outcomes recorded)"
	workaround := successAt5
	if outcomes > 0 {
		successAt5 = fmt.Sprintf("%d/%d found (needs ≥30 genuine handoffs; diary protocol)", g.OutcomeFound, outcomes)
		workaround = fmt.Sprintf("%d/%d workarounds", g.OutcomeWorkaround, outcomes)
	}

	fmt.Fprintf(w, "cairn gates — engineering gates are release blockers (rulings §10)\n\n")
	fmt.Fprintf(w, "%-42s %-16s %s\n", "GATE", "MEASUREMENT", "STATUS")
	fmt.Fprintf(w, "%-42s %-16s %s\n", "acknowledged-event loss = 0", "automated", zeroLoss)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "provenance on fetched results = 100%", "automated", provenance)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "hard-budget compliance = 100%", "automated", budget)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "send-ack → lexical-visible P95 < 200ms", "automated", latency)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "first-query Success@5 ≥ 70%", "human-measured", successAt5)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "manual-workaround rate ≤ 25%", "human-measured", workaround)
	fmt.Fprintf(w, "%-42s %-16s %s\n", "median time-to-context < 60s", "human-measured", "diary protocol (DOGFOOD.md, M8)")
	return nil
}

// DoctorProjection inspects the derived projection (F1/F3). Parked events
// and a checkpoint AHEAD of the log are failures. A checkpoint BEHIND the
// log with zero parked events is informational: parking guarantees the
// projector can no longer stall silently, so "behind" is always
// heal-by-replay (daemon start / reindex), e.g. after an offline migrate
// appended security events. Interpretation recorded in RULINGS.md.
func DoctorProjection(portableDir, dbPath string) (problems, infos []string, err error) {
	p, err := projection.Open(dbPath, nil)
	if err != nil {
		return []string{fmt.Sprintf("projection unopenable: %v (run `cairn reindex --lexical`)", err)}, nil, nil
	}
	defer p.Close()
	parked, err := p.ParkedEvents()
	if err != nil {
		return nil, nil, err
	}
	for _, pe := range parked {
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
