package daemon_test

// N7 acceptance: blob replication + durability acknowledgement (buildpack N7;
// spec §6.3; R31/R32). Reuses the N6 two-node ceremony pair.
//
//	1. attachment sent on A → ack accepted_locally with replication pending
//	   (1/2); reaches durability 2/2 when B connects and reconciles.
//	2. fetch on B of an A-origin blob verifies (content-address) and
//	   re-advertises (B becomes a holder; A learns it).
//	3. doctor reports match reality through a kill-9 mid-blob-transfer: the
//	   interrupted state (event present, blob absent) is reported accurately as
//	   pending, a corrupt present copy is flagged, and re-sync converges.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/projection"
)

// removeObject deletes a content-addressed object from a node's store,
// emulating a blob transfer that was killed (the atomic store leaves nothing
// partial — either the whole object or none).
func removeObject(portableDir, hash string) error {
	path := object.NewStore(fsx.OS{}, portableDir).Path(hash)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// removeDurabilityRegistry drops the peer-holder registry (crash before flush).
func removeDurabilityRegistry(t *testing.T, portableDir string) {
	t.Helper()
	_ = os.Remove(filepath.Join(portableDir, ".cairn", "durability.json"))
}

func durOf(t *testing.T, d *daemon.Daemon, hash string) daemon.BlobDurability {
	t.Helper()
	st, err := d.DurabilityStatus()
	if err != nil {
		t.Fatalf("durability status: %v", err)
	}
	for _, b := range st {
		if b.Hash == hash {
			return b
		}
	}
	return daemon.BlobDurability{Hash: hash}
}

func TestN7BlobDurability(t *testing.T) {
	p := setupN6Pair(t)
	warnA, warnB := &syncBuf{}, &syncBuf{}
	dA, cancelA, addrA := startSyncNode(t, p.baseA, p.dirA, warnA)
	dB, cancelB, _ := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelA(); dA.Close() }()
	defer func() { cancelB(); dB.Close() }()

	// A publishes a message WITH an attachment at durability=normal (target 2).
	blob := []byte("N7 attachment payload — a binary-ish blob \x00\x01\x02 unique-zz7")
	res, err := dA.Publish(daemon.PublishRequest{
		Actor: "operator", Body: "message carrying a durable attachment",
		Attachments: []daemon.AttachmentIn{{Data: blob, Filename: "report.bin", Mime: "application/octet-stream"}},
		Durability:  "normal",
	})
	if err != nil {
		t.Fatal(err)
	}

	// (1) ack surfaces replication: accepted_locally, pending 1/2.
	if res.Replication == nil {
		t.Fatal("drill1: PublishResult carried no replication state for an attachment message")
	}
	if res.Replication.Target != 2 || res.Replication.Have != 1 || !res.Replication.Pending {
		t.Fatalf("drill1: ack replication = %+v, want target 2 / have 1 / pending", res.Replication)
	}

	st, err := dA.DurabilityStatus()
	if err != nil || len(st) != 1 {
		t.Fatalf("drill1: A durability status = %v (%d blobs), want 1", err, len(st))
	}
	hash := st[0].Hash
	if st[0].Satisfied || st[0].Have != 1 || st[0].Target != 2 {
		t.Fatalf("drill1: A blob before sync = %+v, want 1/2 unsatisfied", st[0])
	}
	if !dA.Store().Exists(hash) {
		t.Fatal("drill1: A does not hold its own attachment blob")
	}
	if dB.Store().Exists(hash) {
		t.Fatal("drill1: B holds the blob before any sync")
	}

	// (1)+(2): B reconciles — pulls the event (N6) AND fetches the blob (N7),
	// verifying it (R31) and re-advertising. Durability reaches 2/2 on BOTH.
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("B reconcile: %v\nA:\n%s\nB:\n%s", err, warnA.String(), warnB.String())
	}
	if !dB.Store().Exists(hash) {
		t.Fatalf("drill2: B did not fetch the blob\nB warn:\n%s", warnB.String())
	}
	// the fetched bytes verify (content-address): store.Get re-verifies.
	if got, err := dB.Store().Get(hash); err != nil || string(got) != string(blob) {
		t.Fatalf("drill2: B's fetched blob failed verification: %v", err)
	}
	if b := durOf(t, dB, hash); !b.Satisfied || b.Have != 2 {
		t.Fatalf("drill2: B durability = %+v, want 2/2 satisfied", b)
	}
	// A learned B holds it (re-advertise propagated during the same dial).
	if b := durOf(t, dA, hash); !b.Satisfied || b.Have != 2 {
		t.Fatalf("drill2: A durability after B fetched = %+v, want 2/2 satisfied", b)
	}

	// deep doctor on both: clean, and durability reported satisfied.
	for name, dir := range map[string]string{"A": p.dirA, "B": p.dirB} {
		probs, infos, err := daemon.DeepDoctor(fsx.OS{}, dir, projection.DBPath(dir), time.Now())
		if err != nil {
			t.Fatalf("deep doctor %s: %v", name, err)
		}
		if len(probs) != 0 {
			t.Fatalf("deep doctor %s problems: %v", name, probs)
		}
		if !containsSubstr(infos, "SATISFIED") {
			t.Fatalf("deep doctor %s durability infos missing SATISFIED: %v", name, infos)
		}
	}

	// (3) kill-9 mid-blob-transfer accuracy: emulate B having synced the EVENT
	// but the blob transfer being killed — the atomic object store leaves
	// nothing partial, so B holds the event but not the blob. Doctor must
	// report this accurately (pending), never a false satisfied.
	cancelB()
	dB.Close()
	// drop B's blob object + the peer-holder registry (crash before flush)
	if err := removeObject(p.dirB, hash); err != nil {
		t.Fatal(err)
	}
	removeDurabilityRegistry(t, p.dirB)

	// deep doctor over B's files (no daemon): the blob is a known target but
	// absent locally with no known peers → pending 0/2, and NOT a problem
	// (a missing attachment blob is legitimate — it replicates lazily).
	probs, infos, err := daemon.DeepDoctor(fsx.OS{}, p.dirB, projection.DBPath(p.dirB), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(probs) != 0 {
		t.Fatalf("drill3: deep doctor flagged a MISSING (lazily-replicated) blob as a problem: %v", probs)
	}
	if !containsSubstr(infos, "pending") {
		t.Fatalf("drill3: deep doctor did not report the interrupted blob as pending: %v", infos)
	}

	// re-sync converges: B re-fetches the blob and returns to 2/2.
	dB, cancelB2, _ := startSyncNode(t, p.baseB, p.dirB, warnB)
	defer func() { cancelB2(); dB.Close() }()
	if _, err := dB.SyncWith(addrA); err != nil {
		t.Fatalf("drill3 re-sync: %v", err)
	}
	if !dB.Store().Exists(hash) {
		t.Fatal("drill3: B did not re-fetch the blob after the interrupted transfer")
	}
	if b := durOf(t, dB, hash); !b.Satisfied || b.Have != 2 {
		t.Fatalf("drill3: B durability after re-sync = %+v, want 2/2", b)
	}
}

func containsSubstr(lines []string, sub string) bool {
	for _, l := range lines {
		for i := 0; i+len(sub) <= len(l); i++ {
			if l[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
