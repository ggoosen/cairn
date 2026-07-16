package daemon

// N7 blob durability tracking (spec §6.3; buildpack N7; R31/R32).
//
// Durability is a per-blob replica target: the class (from the message.publish
// attachment payload) maps to a required number of holding nodes —
//
//	ephemeral → 1 (origin only, never replicated)
//	normal    → DurabilityNormalMin (default 2)
//	important → all non-revoked member nodes (all operator nodes)
//	pinned    → all non-revoked member nodes (per-policy; conservative = all)
//
// A blob's CURRENT holder count is this node itself (iff the object store holds
// the bytes) plus every PEER known to hold it. Peer holdership is local,
// non-replicated runtime knowledge (spec §4.5: not events, not replicated),
// learned during reconciliation and persisted to .cairn/durability.json so a
// separate `cairn doctor` invocation sees the same picture. The registry is
// derived/cache-class: it is rebuilt by re-advertisement on the next sweep if
// lost, and self-holdership is always recomputed from the object store (never
// trusted from the file), so a deleted or corrupt blob is never miscounted.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
)

// atomicOverwrite writes a MUTABLE derived file (temp → rename-over). Unlike
// fsx.WriteFileAtomic (the write-once primitive for immutable objects/receipts,
// which refuses to replace an existing file), this replaces in place — the
// durability registry is rewritten on every reconcile.
func atomicOverwrite(fsys fsx.FS, path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	fsys.Remove(tmp)
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		fsys.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		fsys.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		fsys.Remove(tmp)
		return err
	}
	if err := fsys.Rename(tmp, path); err != nil { // POSIX rename replaces
		fsys.Remove(tmp)
		return err
	}
	return fsys.SyncDir(filepath.Dir(path))
}

// durabilityRegistry maps a blob hash to the set of PEER device ids known to
// hold it (self is never stored — it is derived from the object store).
type durabilityRegistry struct {
	mu          sync.Mutex
	PeerHolders map[string]map[string]bool `json:"-"`
	// Serialized form: hash → sorted peer device ids.
	Raw map[string][]string `json:"peer_holders"`
}

func newDurabilityRegistry() *durabilityRegistry {
	return &durabilityRegistry{PeerHolders: map[string]map[string]bool{}}
}

func durabilityPath(portableDir string) string {
	return filepath.Join(portableDir, config.DerivedDirName, config.DurabilityRegistry)
}

// loadDurability reads the persisted registry (empty if absent/unparseable —
// it is rebuildable, so a bad file is never fatal).
func loadDurability(fsys fsx.FS, portableDir string) *durabilityRegistry {
	r := newDurabilityRegistry()
	blob, err := fsys.ReadFile(durabilityPath(portableDir))
	if err != nil {
		return r
	}
	var disk struct {
		Raw map[string][]string `json:"peer_holders"`
	}
	if json.Unmarshal(blob, &disk) != nil {
		return r
	}
	for h, peers := range disk.Raw {
		set := map[string]bool{}
		for _, p := range peers {
			set[p] = true
		}
		r.PeerHolders[h] = set
	}
	return r
}

// save atomically persists the registry (cache-class; a lost write just costs
// re-advertisement next sweep).
func (r *durabilityRegistry) save(fsys fsx.FS, portableDir string) error {
	r.mu.Lock()
	raw := map[string][]string{}
	for h, set := range r.PeerHolders {
		peers := make([]string, 0, len(set))
		for p := range set {
			peers = append(peers, p)
		}
		sort.Strings(peers)
		raw[h] = peers
	}
	r.mu.Unlock()
	blob, err := json.Marshal(struct {
		Raw map[string][]string `json:"peer_holders"`
	}{Raw: raw})
	if err != nil {
		return err
	}
	return atomicOverwrite(fsys, durabilityPath(portableDir), blob, config.FilePerm)
}

// addPeerHolder records that a peer device holds a blob.
func (r *durabilityRegistry) addPeerHolder(hash, deviceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.PeerHolders[hash]
	if set == nil {
		set = map[string]bool{}
		r.PeerHolders[hash] = set
	}
	set[deviceID] = true
}

// peerCount returns the number of DISTINCT peers known to hold a blob.
func (r *durabilityRegistry) peerCount(hash string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.PeerHolders[hash])
}

// durabilityTarget maps a class to its required holder count. memberCount is
// the number of non-revoked mesh member devices (all operator nodes).
func durabilityTarget(class string, memberCount int) int {
	switch class {
	case "ephemeral":
		return 1
	case "normal":
		return config.DurabilityNormalMin
	case "important", "pinned":
		if memberCount < 1 {
			return 1
		}
		return memberCount
	default:
		return config.DurabilityNormalMin
	}
}

// memberCount is the number of non-revoked mesh member devices that back
// durability: FULL nodes only (P3-3 — a thin node is not counted toward the
// durability target, spec §7). Caller holds d.mu (reads trust + peerRoles).
// Falls back to 1 without trust.
func (d *Daemon) memberCount() int {
	if d.trust == nil {
		return 1
	}
	return countDurabilityMembers(d.trust.Devices(), d.trust.Revoked, d.deviceIsThin)
}

// blobHolderCount is the current holder count for a blob: self (iff the object
// store holds valid bytes) plus known peers. Caller holds d.mu.
func (d *Daemon) blobHolderCount(hash string) int {
	n := d.durab.peerCount(hash)
	if d.store.Exists(hash) {
		n++
	}
	return n
}

// pendingBlobMessages returns the set of message ids whose attachment blobs
// are below their durability target (for the digest [replication-pending]
// marker). Best-effort: an error yields an empty set (no markers).
func (d *Daemon) pendingBlobMessages() map[string]bool {
	out := map[string]bool{}
	dur, err := d.DurabilityStatus()
	if err != nil {
		return out
	}
	pendingHash := map[string]bool{}
	for _, b := range dur {
		if !b.Satisfied {
			pendingHash[b.Hash] = true
		}
	}
	if len(pendingHash) == 0 {
		return out
	}
	blobs, err := d.proj.AttachmentBlobs()
	if err != nil {
		return out
	}
	for _, b := range blobs {
		if pendingHash[b.ObjectHash] {
			out[b.MessageID] = true
		}
	}
	return out
}

// BlobDurability is one attachment blob's live durability state (spec §6.3).
type BlobDurability struct {
	Hash      string `json:"hash"`
	Class     string `json:"durability"`
	Target    int    `json:"target"`
	Have      int    `json:"have"`
	SelfHolds bool   `json:"self_holds"`
	Satisfied bool   `json:"satisfied"`
}

// DurabilityStatus reports the live per-blob durability state (self + known
// peers vs target). Used by `cairn sync status`, gates, and the N7 tests. A
// blob shared across messages takes its STRONGEST class.
func (d *Daemon) DurabilityStatus() ([]BlobDurability, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	blobs, err := d.proj.AttachmentBlobs()
	if err != nil {
		return nil, err
	}
	members := d.memberCount()
	strongest := map[string]string{}
	for _, b := range blobs {
		if cur, ok := strongest[b.ObjectHash]; !ok || durabilityTarget(b.Durability, members) > durabilityTarget(cur, members) {
			strongest[b.ObjectHash] = b.Durability
		}
	}
	out := make([]BlobDurability, 0, len(strongest))
	hashes := make([]string, 0, len(strongest))
	for h := range strongest {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	for _, h := range hashes {
		class := strongest[h]
		target := durabilityTarget(class, members)
		have := d.blobHolderCount(h)
		out = append(out, BlobDurability{
			Hash: h, Class: class, Target: target, Have: have,
			SelfHolds: d.store.Exists(h), Satisfied: have >= target,
		})
	}
	return out, nil
}
