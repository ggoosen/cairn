package daemon

// D2 — the origin-liveness beacon (spec §13.2, deferred in P0 for want of
// peers; BUILD-PLAN §4 D2).
//
// The failure this catches is real and, until now, silent. An origin's append
// chain only ever grows: (generation, sequence) is monotonic for the life of a
// device. Two operator accidents break that.
//
//   - A device restored from portable data ONLY mints a new origin generation
//     (R9/identity migrate) — visible, and already handled.
//   - A device restored from a STALE BACKUP comes back on the SAME origin with
//     a LOWER sequence. Nothing noticed. `detectFrontierForkFromPeer` compares
//     chains that reached the same sequence with different heads; a chain that
//     simply moved BACKWARDS collides with nothing, so reconciliation reads it
//     as an ordinary lagging peer, pushes the "missing" events back — and the
//     restored device REFUSES them, because ingestRecords treats any peer event
//     at or beyond the head of our OWN active origin as a device clone (N8) and
//     freezes the origin. Measured, not assumed: see TestD2OriginLivenessBeacon
//     drill 5. So the operator's first news of a stale restore is an
//     equivocation alarm on the restored machine, blaming a clone that does not
//     exist, with nothing anywhere naming the actual cause.
//
// So: persist the highest (generation, sequence) ever observed per origin, and
// on every frontier exchange compare what a peer advertises against it.
//
// WHAT IS COMPARED, AND WHY IT IS ONLY THIS. A peer advertises frontiers for
// every origin it holds, and holding less of SOMEBODY ELSE'S origin is the
// normal state of the mesh — it is what "behind" means, it is what a thin node
// is, and it is what every node looks like mid-catch-up. Only a device's own
// origin is a statement about itself: a device is the sole appender to its own
// chain, so it can never legitimately hold less of it than it once did. The
// beacon therefore compares ONLY the frontiers a peer advertises for origins
// whose device id IS that peer. That single restriction is what makes ordinary
// catch-up, ordinary restart and a thin node structurally incapable of raising
// this alarm, rather than merely unlikely to.
//
// (A thin node today holds its own log in full — the "recent window" of spec §7
// is about what it keeps of the MESH. If self-pruning is ever added, this check
// has to learn the peer's role; the restriction above is not enough on its own.)
//
// ALARM, NEVER QUARANTINE. A regression means an operator restored a backup, so
// the response is to say so loudly and durably and let the operator decide. The
// events are still valid, still signed, and still wanted; freezing the origin
// would turn a recoverable mistake into an outage. Equivocation — two different
// events at the same sequence — is a different failure with a different answer,
// and fork.go already owns it.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// OriginWatermark is the highest position ever observed for one origin.
type OriginWatermark struct {
	OriginDevice string `json:"origin_device"`
	OriginGen    int    `json:"origin_generation"`
	NextSeq      int64  `json:"next_seq"` // highest next-expected sequence ever seen
	LastEventID  string `json:"last_event_id,omitempty"`
	ObservedAt   string `json:"observed_at"`
	ObservedFrom string `json:"observed_from"` // "local-log" or the advertising device id
}

func (w OriginWatermark) origin() cairnlog.Origin {
	return cairnlog.Origin{DeviceID: w.OriginDevice, Generation: w.OriginGen}
}

// LivenessAlarm is one recorded frontier regression. It is durable and it is
// not cleared automatically: when the origin comes back at or above its
// watermark the alarm is marked RECOVERED (doctor demotes it to a note) but the
// record of the loss stays, because "it fixed itself" is exactly the kind of
// thing an operator should still get to see.
type LivenessAlarm struct {
	OriginDevice string `json:"origin_device"`
	OriginGen    int    `json:"origin_generation"`
	WatermarkSeq int64  `json:"watermark_next_seq"`
	WatermarkAt  string `json:"watermark_observed_at"`
	ObservedSeq  int64  `json:"observed_next_seq"` // -1: the peer no longer advertises this origin at all
	Peer         string `json:"peer"`              // the device that advertised the regression (== origin device)
	DetectedAt   string `json:"detected_at"`
	LastSeenAt   string `json:"last_seen_at"`
	Observations int    `json:"observations"`
	Recovered    bool   `json:"recovered,omitempty"`
	RecoveredAt  string `json:"recovered_at,omitempty"`
}

// Lost reports how many acknowledged events the regression accounts for.
func (a LivenessAlarm) Lost() int64 {
	if a.ObservedSeq < 0 {
		return a.WatermarkSeq - int64(config.FirstSequence)
	}
	return a.WatermarkSeq - a.ObservedSeq
}

func (a LivenessAlarm) origin() cairnlog.Origin {
	return cairnlog.Origin{DeviceID: a.OriginDevice, Generation: a.OriginGen}
}

// Describe renders one alarm for an operator surface (`cairn net`, doctor).
func (a LivenessAlarm) Describe() string {
	held := fmt.Sprintf("now advertises next_seq %d", a.ObservedSeq)
	if a.ObservedSeq < 0 {
		held = "no longer advertises this origin AT ALL"
	}
	state := "UNRESOLVED"
	if a.Recovered {
		state = "recovered at " + a.RecoveredAt
	}
	return fmt.Sprintf("origin %s/%d regressed: watermark next_seq %d (observed %s), %s — %d event(s) missing on the origin device itself [%s]",
		a.OriginDevice, a.OriginGen, a.WatermarkSeq, a.WatermarkAt, held, a.Lost(), state)
}

// livenessRegistry holds the watermarks and alarms. Cache-class on disk (the
// local floor is recomputed from our own verified log at every observation), so
// a lost or unparseable file is never fatal.
type livenessRegistry struct {
	mu         sync.Mutex
	watermarks map[cairnlog.Origin]OriginWatermark
	alarms     map[cairnlog.Origin]*LivenessAlarm
}

// livenessFile is the on-disk shape (sorted slices — a registry that reorders
// itself between saves is needlessly hard to diff).
type livenessFile struct {
	Watermarks []OriginWatermark `json:"watermarks"`
	Alarms     []LivenessAlarm   `json:"alarms,omitempty"`
}

func newLivenessRegistry() *livenessRegistry {
	return &livenessRegistry{
		watermarks: map[cairnlog.Origin]OriginWatermark{},
		alarms:     map[cairnlog.Origin]*LivenessAlarm{},
	}
}

func livenessPath(portableDir string) string {
	return filepath.Join(portableDir, config.DerivedDirName, config.LivenessRegistry)
}

func loadLiveness(fsys fsx.FS, portableDir string) *livenessRegistry {
	r := newLivenessRegistry()
	blob, err := fsys.ReadFile(livenessPath(portableDir))
	if err != nil {
		return r
	}
	var disk livenessFile
	if json.Unmarshal(blob, &disk) != nil {
		return r
	}
	for _, w := range disk.Watermarks {
		r.watermarks[w.origin()] = w
	}
	for i := range disk.Alarms {
		a := disk.Alarms[i]
		r.alarms[a.origin()] = &a
	}
	return r
}

func (r *livenessRegistry) snapshot() livenessFile {
	var out livenessFile
	for _, w := range r.watermarks {
		out.Watermarks = append(out.Watermarks, w)
	}
	for _, a := range r.alarms {
		out.Alarms = append(out.Alarms, *a)
	}
	sort.Slice(out.Watermarks, func(i, j int) bool {
		return originKey(out.Watermarks[i].origin()) < originKey(out.Watermarks[j].origin())
	})
	sort.Slice(out.Alarms, func(i, j int) bool { return originKey(out.Alarms[i].origin()) < originKey(out.Alarms[j].origin()) })
	return out
}

func (r *livenessRegistry) save(fsys fsx.FS, portableDir string) error {
	r.mu.Lock()
	snap := r.snapshot()
	r.mu.Unlock()
	blob, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := fsys.MkdirAll(filepath.Join(portableDir, config.DerivedDirName), config.DirPerm); err != nil {
		return err
	}
	return atomicOverwrite(fsys, livenessPath(portableDir), blob, config.FilePerm)
}

// originKey is the stable sort/lookup key for an origin.
func originKey(o cairnlog.Origin) string { return forkKey(o) }

// raiseLocked records or refreshes a watermark. Watermarks only ever RISE.
func (r *livenessRegistry) raiseLocked(o cairnlog.Origin, nextSeq int64, headID, from, at string) {
	cur, ok := r.watermarks[o]
	if ok && nextSeq <= cur.NextSeq {
		return
	}
	r.watermarks[o] = OriginWatermark{
		OriginDevice: o.DeviceID, OriginGen: o.Generation,
		NextSeq: nextSeq, LastEventID: headID, ObservedAt: at, ObservedFrom: from,
	}
}

// observeLocal raises the watermark from our OWN verified log. Frames we hold
// are proof the origin reached that sequence, which makes the beacon work on
// the very first reconcile after the registry file is lost — and makes a peer
// unable to lower the floor by advertising less.
func (r *livenessRegistry) observeLocal(mine []originFrontier, at string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range mine {
		r.raiseLocked(f.log(), f.NextSeq, f.LastEventID, "local-log", at)
	}
}

// observePeer compares a peer's SELF-advertised frontiers against the
// watermarks and returns any regressions (newly raised or re-observed). Every
// non-regressing self-advertisement raises the watermark, and an origin that
// comes back at or above its watermark marks its alarm recovered.
func (r *livenessRegistry) observePeer(peerFr []originFrontier, peerDevice, at string) []LivenessAlarm {
	if peerDevice == "" || len(peerFr) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	advertised := map[cairnlog.Origin]originFrontier{}
	for _, f := range peerFr {
		if f.DeviceID != peerDevice {
			continue // only a device is authoritative about its OWN chain
		}
		advertised[f.log()] = f
	}
	if len(advertised) == 0 {
		// The peer told us nothing about itself. Every node advertises its own
		// active origin (frontiersLocked always includes it), so this is a
		// malformed or truncated exchange, not a regression — say nothing.
		return nil
	}

	var raised []LivenessAlarm
	for o, w := range r.watermarks {
		if o.DeviceID != peerDevice {
			continue
		}
		seq := int64(-1) // the origin is gone from the peer entirely
		if f, ok := advertised[o]; ok {
			seq = f.NextSeq
		}
		if seq >= w.NextSeq {
			if a, ok := r.alarms[o]; ok && !a.Recovered {
				a.Recovered, a.RecoveredAt = true, at
			}
			continue
		}
		a, ok := r.alarms[o]
		if !ok {
			a = &LivenessAlarm{
				OriginDevice: o.DeviceID, OriginGen: o.Generation,
				WatermarkSeq: w.NextSeq, WatermarkAt: w.ObservedAt,
				Peer: peerDevice, DetectedAt: at,
			}
			r.alarms[o] = a
		}
		// a deeper or renewed regression re-opens a recovered alarm
		if a.Recovered {
			a.Recovered, a.RecoveredAt = false, ""
			a.DetectedAt = at
		}
		if w.NextSeq > a.WatermarkSeq {
			a.WatermarkSeq, a.WatermarkAt = w.NextSeq, w.ObservedAt
		}
		a.ObservedSeq, a.Peer, a.LastSeenAt = seq, peerDevice, at
		a.Observations++
		raised = append(raised, *a)
	}
	// raise from the peer's self-advertisement AFTER comparing, so a peer that
	// has legitimately moved on sets the next floor.
	for o, f := range advertised {
		r.raiseLocked(o, f.NextSeq, f.LastEventID, peerDevice, at)
	}
	sort.Slice(raised, func(i, j int) bool { return originKey(raised[i].origin()) < originKey(raised[j].origin()) })
	return raised
}

func (r *livenessRegistry) alarmList() []LivenessAlarm {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot().Alarms
}

func (r *livenessRegistry) watermarkList() []OriginWatermark {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot().Watermarks
}

// noteOriginLiveness is the beacon's one entry point, called from BOTH sides of
// a frontier exchange (initiator and responder) so a regression is caught
// whichever node dials. It never holds d.mu across the disk write, and every
// failure here is logged and swallowed: a diagnostic must not break replication.
func (d *Daemon) noteOriginLiveness(peerFr []originFrontier, peerDevice string) {
	if d.liveness == nil {
		return
	}
	at := d.now().UTC().Format(config.WallTimeFormat)
	if mine, err := d.Frontiers(); err == nil {
		d.liveness.observeLocal(mine, at)
	}
	for _, a := range d.liveness.observePeer(peerFr, peerDevice, at) {
		if a.Observations > 1 {
			continue // already reported; the durable record carries the detail
		}
		held := fmt.Sprintf("now advertises next_seq %d", a.ObservedSeq)
		if a.ObservedSeq < 0 {
			held = "no longer advertises that origin at all"
		}
		fmt.Fprintf(d.warn, "ORIGIN LIVENESS ALARM: device %s regressed on its OWN origin %s/%d — we have seen next_seq %d (at %s) but it %s, so %d acknowledged event(s) are missing THERE. That is a stale-backup restore, not equivocation: nothing is frozen HERE and this node still holds the events. They will NOT flow back on their own — a device refuses its own events from a peer as a clone (N8) and freezes that origin — so stop the restored device before it writes at those sequences and repair it per DOGFOOD §14. See `cairn net` and `cairn doctor`.\n",
			a.Peer, a.OriginDevice, a.OriginGen, a.WatermarkSeq, a.WatermarkAt, held, a.Lost())
	}
	if err := d.liveness.save(d.fs, d.dir); err != nil {
		fmt.Fprintf(d.warn, "origin-liveness beacon: persisting watermarks failed: %v\n", err)
	}
}

// LivenessAlarms returns the recorded regressions (sorted).
func (d *Daemon) LivenessAlarms() []LivenessAlarm {
	if d.liveness == nil {
		return nil
	}
	return d.liveness.alarmList()
}

// OriginWatermarks returns the highest position ever observed per origin.
func (d *Daemon) OriginWatermarks() []OriginWatermark {
	if d.liveness == nil {
		return nil
	}
	return d.liveness.watermarkList()
}

// ReadLivenessAlarms loads the persisted alarms from disk, so `cairn doctor`
// reports them with or without a running daemon (as it does for forks).
func ReadLivenessAlarms(fsys fsx.FS, portableDir string) []LivenessAlarm {
	return loadLiveness(fsys, portableDir).alarmList()
}

// LivenessDoctor reports unrecovered regressions as PROBLEMS and recovered ones
// informationally — the same split fork records get, for the same reason: the
// operator needs to know it happened even after it stopped mattering.
func LivenessDoctor(fsys fsx.FS, portableDir string) (problems, infos []string) {
	for _, a := range ReadLivenessAlarms(fsys, portableDir) {
		if a.Recovered {
			infos = append(infos, "origin-liveness: "+a.Describe())
			continue
		}
		problems = append(problems, fmt.Sprintf("ORIGIN LIVENESS: %s — the origin device restored from a stale backup or lost durable frames. This node still holds the events; they do NOT replicate back on their own (a device refuses its own events from a peer as a clone, N8). Stop the restored device before it writes at those sequences and repair per DOGFOOD §14",
			a.Describe()))
	}
	return problems, infos
}

// livenessAlarmStatus renders the alarms for the sync-status IPC payload
// (`cairn net`, `cairn sync status`).
func (d *Daemon) livenessAlarmStatus() []map[string]any {
	alarms := d.LivenessAlarms()
	if len(alarms) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(alarms))
	for _, a := range alarms {
		out = append(out, map[string]any{
			"origin":             a.OriginDevice + "/" + fmt.Sprint(a.OriginGen),
			"watermark_next_seq": a.WatermarkSeq,
			"observed_next_seq":  a.ObservedSeq,
			"missing_events":     a.Lost(),
			"peer":               a.Peer,
			"detected_at":        a.DetectedAt,
			"recovered":          a.Recovered,
			"detail":             a.Describe(),
		})
	}
	return out
}
