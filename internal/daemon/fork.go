package daemon

// N8 live fork detection (spec §6.4; buildpack N8; R33).
//
// The P0 equivocation case — same (origin_device, generation, sequence),
// DIFFERENT event_id, both validly signed (a full-disk device clone that then
// wrote divergent events) — is undetectable on one node but becomes detectable
// when two logs meet under replication. On detection cairn:
//
//   - freezes ingest from the forked origin (it is quarantined; no branch is
//     ever extended or deleted automatically),
//   - quarantines the divergent branch's frames under .cairn/quarantine
//     (preserved forever — the losing branch is never silently deleted),
//   - records a ForkRecord under .cairn/forks and logs LOUDLY,
//   - keeps every OTHER origin syncing normally (R33).
//
// Repair is a deliberate operator ceremony (fork.go's Resolve): the operator
// chooses the canonical branch, the losing branch's useful events are reissued
// under a recovery origin carrying recovered_from_event_id + fork_resolution_id,
// a root-signed device.fork.resolve is recorded, and the cloned certificate is
// revoked (documented follow-up). Durable-log internals are untouched — the
// resolve ceremony appends through the public log API exactly as migrate does.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

// ForkRecord is the persisted state of one detected equivocation.
type ForkRecord struct {
	OriginDevice  string            `json:"origin_device"`
	OriginGen     int               `json:"origin_generation"`
	DivergenceSeq int64             `json:"divergence_seq"` // first differing seq; common ancestor = this-1
	LocalHead     string            `json:"local_head"`
	LocalHeadSeq  int64             `json:"local_head_seq"`
	RemoteHead    string            `json:"remote_head"`
	RemoteHeadSeq int64             `json:"remote_head_seq"`
	DetectedPeer  string            `json:"detected_peer"`
	DetectedAt    string            `json:"detected_at"`
	SecurityOps   bool              `json:"security_ops"` // either branch has device.* at/after divergence
	RemoteBranch  []ForkBranchEvent `json:"remote_branch"`
	LocalBranch   []ForkBranchEvent `json:"local_branch"`
	Resolved      bool              `json:"resolved"`
	ResolutionID  string            `json:"fork_resolution_id,omitempty"`
	Canonical     string            `json:"canonical,omitempty"` // "local" | "remote"
}

// ForkBranchEvent is one event on a branch (summary for doctor).
type ForkBranchEvent struct {
	Seq       int64  `json:"seq"`
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
}

func (f *ForkRecord) origin() cairnlog.Origin {
	return cairnlog.Origin{DeviceID: f.OriginDevice, Generation: f.OriginGen}
}

// forkKey is the stable filename stem for an origin's fork record.
func forkKey(o cairnlog.Origin) string {
	return o.DeviceID + "_" + strconv.Itoa(o.Generation)
}

func forksDir(portableDir string) string {
	return filepath.Join(portableDir, config.DerivedDirName, config.ForksDirName)
}
func quarantineDir(portableDir string, o cairnlog.Origin) string {
	return filepath.Join(portableDir, config.DerivedDirName, config.QuarantineDirName, forkKey(o))
}

// loadForks reads every persisted fork record (best-effort; a bad file is
// skipped, not fatal — the quarantine frames are the durable evidence).
func loadForks(portableDir string) map[cairnlog.Origin]*ForkRecord {
	out := map[cairnlog.Origin]*ForkRecord{}
	entries, err := os.ReadDir(forksDir(portableDir))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(forksDir(portableDir), e.Name()))
		if err != nil {
			continue
		}
		var f ForkRecord
		if json.Unmarshal(blob, &f) != nil {
			continue
		}
		out[f.origin()] = &f
	}
	return out
}

func saveForkRecord(portableDir string, f *ForkRecord) error {
	dir := forksDir(portableDir)
	if err := os.MkdirAll(dir, config.DirPerm); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicOverwriteOS(filepath.Join(dir, forkKey(f.origin())+".json"), blob)
}

// atomicOverwriteOS is atomicOverwrite over the real filesystem (fork state is
// always real-fs derived data; not part of the injected-fs durable path).
func atomicOverwriteOS(path string, data []byte) error {
	tmp := path + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, data, config.FilePerm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// quarantineFrame preserves one divergent-branch record forever (never
// deleted; the losing branch is recoverable). Idempotent by (seq,event_id).
func quarantineFrame(portableDir string, o cairnlog.Origin, seq int64, eventID string, rec []byte) error {
	dir := quarantineDir(portableDir, o)
	if err := os.MkdirAll(dir, config.DirPerm); err != nil {
		return err
	}
	name := fmt.Sprintf("%08d_%s.frame", seq, eventID)
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return nil // already quarantined
	}
	return atomicOverwriteOS(path, rec)
}

// isFrozen reports whether an origin has an UNRESOLVED fork (ingest frozen).
// Caller holds d.mu.
func (d *Daemon) isFrozen(o cairnlog.Origin) bool {
	f, ok := d.forks[o]
	return ok && !f.Resolved
}

// Forks returns all known fork records (doctor/gates). Caller holds d.mu.
func (d *Daemon) forksLocked() []*ForkRecord {
	out := make([]*ForkRecord, 0, len(d.forks))
	for _, f := range d.forks {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return forkKey(out[i].origin()) < forkKey(out[j].origin()) })
	return out
}

// Forks is the locked accessor.
func (d *Daemon) Forks() []*ForkRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.forksLocked()
}

// UnresolvedForks counts frozen origins. Caller need not hold d.mu.
func (d *Daemon) UnresolvedForks() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, f := range d.forks {
		if !f.Resolved {
			n++
		}
	}
	return n
}

// isSecurityType reports whether an event type is a security/structural op
// (its presence on a branch raises the stakes of the fork — §6.4).
func isSecurityType(t string) bool {
	return strings.HasPrefix(t, "device.") || t == "cairn.genesis"
}

// ReadForkRecords loads persisted fork records from disk (used by `cairn
// doctor fork` and DeepDoctor without a running daemon). Sorted by origin.
func ReadForkRecords(portableDir string) []*ForkRecord {
	m := loadForks(portableDir)
	out := make([]*ForkRecord, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return forkKey(out[i].origin()) < forkKey(out[j].origin()) })
	return out
}

// ForkDoctor (N8) reports unresolved forks as PROBLEMS and resolved ones
// informationally. Read from disk so it works with or without a daemon.
func ForkDoctor(portableDir string) (problems, infos []string) {
	for _, f := range ReadForkRecords(portableDir) {
		if f.Resolved {
			infos = append(infos, fmt.Sprintf("fork on origin %s/%d RESOLVED (canonical: %s, resolution %s) — losing branch preserved in quarantine",
				f.OriginDevice, f.OriginGen, f.Canonical, short(f.ResolutionID)))
			continue
		}
		sec := ""
		if f.SecurityOps {
			sec = " [SECURITY OPS ON A BRANCH]"
		}
		problems = append(problems, fmt.Sprintf("FORK on origin %s/%d frozen%s — common ancestor seq %d; local head %s vs remote %s (peer %s). Run `cairn doctor fork %s` and repair per DOGFOOD §14",
			f.OriginDevice, f.OriginGen, sec, f.DivergenceSeq-1, short(f.LocalHead), short(f.RemoteHead), f.DetectedPeer, f.OriginDevice))
	}
	return problems, infos
}

// FormatForkDetail renders `cairn doctor fork <origin>` output: common
// ancestor, per-branch unique events with types, the advertising peer, and
// whether security operations appear on either branch.
func FormatForkDetail(f *ForkRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "fork on origin %s/%d\n", f.OriginDevice, f.OriginGen)
	fmt.Fprintf(&b, "  status:          %s\n", map[bool]string{true: "RESOLVED (canonical: " + f.Canonical + ")", false: "FROZEN (unresolved)"}[f.Resolved])
	fmt.Fprintf(&b, "  detected:        %s (peer %s)\n", f.DetectedAt, f.DetectedPeer)
	fmt.Fprintf(&b, "  common ancestor: seq %d\n", f.DivergenceSeq-1)
	fmt.Fprintf(&b, "  security ops:    %v\n", f.SecurityOps)
	fmt.Fprintf(&b, "  LOCAL branch (head %s, seq %d):\n", short(f.LocalHead), f.LocalHeadSeq)
	for _, e := range f.LocalBranch {
		fmt.Fprintf(&b, "    seq %d  %-24s %s\n", e.Seq, e.EventType, short(e.EventID))
	}
	fmt.Fprintf(&b, "  REMOTE branch (head %s, seq %d; quarantined, preserved):\n", short(f.RemoteHead), f.RemoteHeadSeq)
	for _, e := range f.RemoteBranch {
		fmt.Fprintf(&b, "    seq %d  %-24s %s\n", e.Seq, e.EventType, short(e.EventID))
	}
	if f.Resolved {
		fmt.Fprintf(&b, "  resolution:      %s (losing branch preserved in quarantine — never deleted)\n", f.ResolutionID)
	} else {
		fmt.Fprintf(&b, "  repair:          `cairn fork resolve %s --canonical <local|remote> --root-key <path>` (DOGFOOD §14)\n", f.OriginDevice)
	}
	return b.String()
}

// recordForkFromIngest records a fork detected during ingest (the backstop
// path, when the frontier hid the divergence). It has only the ONE conflicting
// record, so the remote branch is that single frame; `cairn doctor fork` or a
// later frontier sync fills in the rest. Caller holds d.mu. Idempotent.
func (d *Daemon) recordForkFromIngest(o cairnlog.Origin, incoming *event.Envelope, rec []byte, myNext int64, myHead, peerDevice string) error {
	if f, ok := d.forks[o]; ok && !f.Resolved {
		return nil
	}
	divSeq := incoming.OriginSequence
	if err := quarantineFrame(d.dir, o, incoming.OriginSequence, incoming.EventID, rec); err != nil {
		return fmt.Errorf("quarantining forked frame: %w", err)
	}
	localBranch, err := d.proj.EventsInRange(o.DeviceID, o.Generation, divSeq, myNext)
	if err != nil {
		return err
	}
	lb := make([]ForkBranchEvent, 0, len(localBranch))
	security := isSecurityType(incoming.EventType)
	for _, e := range localBranch {
		lb = append(lb, ForkBranchEvent{Seq: e.Seq, EventID: e.EventID, EventType: e.EventType})
		if isSecurityType(e.EventType) {
			security = true
		}
	}
	f := &ForkRecord{
		OriginDevice:  o.DeviceID,
		OriginGen:     o.Generation,
		DivergenceSeq: divSeq,
		LocalHead:     myHead,
		LocalHeadSeq:  myNext - 1,
		RemoteHead:    incoming.EventID,
		RemoteHeadSeq: incoming.OriginSequence,
		DetectedPeer:  peerDevice,
		DetectedAt:    d.now().UTC().Format(config.WallTimeFormat),
		SecurityOps:   security,
		RemoteBranch:  []ForkBranchEvent{{Seq: incoming.OriginSequence, EventID: incoming.EventID, EventType: incoming.EventType}},
		LocalBranch:   lb,
	}
	d.forks[o] = f
	if err := saveForkRecord(d.dir, f); err != nil {
		return err
	}
	fmt.Fprintf(d.warn, "FORK DETECTED (ingest): origin %s/%d equivocates at seq %d — local head %s, remote %s from %s%s. Ingest FROZEN (others unaffected). `cairn doctor fork %s`; losing branch quarantined.\n",
		o.DeviceID, o.Generation, divSeq, short(myHead), short(incoming.EventID), peerDevice,
		map[bool]string{true: " [SECURITY OPS]", false: ""}[security], o.DeviceID)
	return nil
}
