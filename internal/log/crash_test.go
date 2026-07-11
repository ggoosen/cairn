package log_test

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
)

// sendPipeline is the M1 slice of the write path (rulings §3): persist the
// referenced object (temp → fsync → rename → dir-fsync; rows 1–3), append
// the event (frame → write → fdatasync; rows 4–5), ACK (row 6/7 boundary),
// then update the sequence-state cache (row 8). M2 swaps the object step for
// the real object store built on the same fsx primitive.
type pipelineOutcome struct {
	acked   bool
	eventID string
	objPath string
	err     error
}

func sendPipeline(fsys fsx.FS, dir string, lg *cairnlog.Log, c *chain, deviceDir string, body []byte) pipelineOutcome {
	store := object.NewStore(fsys, dir)
	hash := object.Hash(body)
	out := pipelineOutcome{objPath: store.Path(hash)}

	// 1. object first — an event must never reference a non-durable object
	if _, err := store.Put(body); err != nil {
		out.err = err
		return out
	}

	// 2. build + sign the event referencing the object
	env := &event.Envelope{
		SchemaVersion:         config.EventSchemaVersion,
		CairnID:               c.cairnID,
		EventType:             "message.publish",
		OriginDeviceID:        c.deviceID,
		OriginGeneration:      1,
		OriginSequence:        c.nextSeq,
		PreviousOriginEventID: c.lastID,
		ActorPrincipalID:      "operator",
		ObjectType:            "message",
		ObjectID:              "0190a1b2-c3d4-7e5f-8901-234567890bbb",
		WallTime:              "2026-07-11T00:00:02.000000Z",
		PayloadSchema:         config.PayloadSchemaID,
		Payload: json.RawMessage(fmt.Sprintf(
			`{"message_id":"0190a1b2-c3d4-7e5f-8901-234567890bbb","revision_id":"0190a1b2-c3d4-7e5f-8901-234567890bbc","body_hash":"%s","body_len":%d,"body_mime":"text/markdown","text_class":"canonical","declared_priority":1}`,
			hash, len(body))),
		SigningKeyID: c.keyID,
	}
	rec, err := env.Sign(c.devPriv)
	if err != nil {
		out.err = err
		return out
	}

	// 3. durable append; 4. ONLY THEN ack
	if err := lg.Append(rec, env); err != nil {
		out.err = err
		return out
	}
	out.acked = true
	out.eventID = env.EventID
	c.nextSeq++
	c.lastID = env.EventID

	// 5. sequence-state cache update (cache only; log is authoritative)
	if _, err := identity.ReconcileSeqState(fsys, deviceDir, c.deviceID, 1, c.nextSeq, io.Discard); err != nil {
		out.err = err // acked stays true: ack precedes cache update
	}
	return out
}

// buildBase creates the pre-crash state: initialized origin, genesis + 2
// events, everything durable.
func buildBase(t *testing.T) (*fsx.MemFS, *chain) {
	t.Helper()
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	m.MkdirAll("/dev", 0o700)
	c, lg := newLogWithGenesis(t, m, "/p", 2)
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	return m, c
}

// recoverAndCheck restarts after a crash and asserts the TESTING.md core
// invariant: every acked event exists exactly once by event_id, the origin
// chain is contiguous and signature-valid, referenced durable objects of
// acked events are fetchable and hash-valid, and doctor agrees.
func recoverAndCheck(t *testing.T, m *fsx.MemFS, c *chain, ackedBefore []string, out pipelineOutcome, label string) {
	t.Helper()

	var seen []string
	lg, _, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify,
		func(env *event.Envelope, _ []byte) error {
			seen = append(seen, env.EventID)
			return nil
		})
	if err != nil {
		t.Fatalf("%s: recovery failed: %v", label, err)
	}
	defer lg.Close()

	count := map[string]int{}
	for _, id := range seen {
		count[id]++
	}
	for _, id := range ackedBefore {
		if count[id] != 1 {
			t.Fatalf("%s: acked event %s present %d times after recovery", label, id, count[id])
		}
	}
	if out.acked {
		if count[out.eventID] != 1 {
			t.Fatalf("%s: acked pipeline event present %d times", label, count[out.eventID])
		}
		store := object.NewStore(m, "/p")
		hash := filepath.Base(filepath.Dir(out.objPath)) + filepath.Base(out.objPath)
		if _, err := store.Get(hash); err != nil {
			t.Fatalf("%s: acked event's object missing or invalid: %v", label, err)
		}
	}

	// row 8 / seq-cache rows: log wins regardless of cache state
	st, err := identity.ReconcileSeqState(m, "/dev", c.deviceID, 1, lg.NextSeq(), io.Discard)
	if err != nil {
		t.Fatalf("%s: reconcile: %v", label, err)
	}
	if st.NextSequence != lg.NextSeq() {
		t.Fatalf("%s: cache %d != log %d", label, st.NextSequence, lg.NextSeq())
	}

	doc, err := cairnlog.Doctor(m, "/p", identity.NewChainVerifier().Verify)
	if err != nil {
		t.Fatalf("%s: doctor: %v", label, err)
	}
	if !doc.Clean() {
		t.Fatalf("%s: doctor found problems after recovery: %+v", label, doc.Origins)
	}
}

// TestCrashMatrixRows1to8 enumerates a crash before EVERY mutating fs
// operation of the send pipeline, in both crash modes (SIGKILL = page cache
// survives; power-sim = unsynced state lost) — TESTING.md §1 rows 1–8.
func TestCrashMatrixRows1to8(t *testing.T) {
	// Count pipeline ops on a throwaway clone.
	base, c0 := buildBase(t)
	probe := base.Clone()
	cProbe := *c0
	lg, _, err := cairnlog.Open(probe, "/p", origin(&cProbe), identity.NewChainVerifier().Verify, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := probe.MutatingOps()
	if out := sendPipeline(probe, "/p", lg, &cProbe, "/dev", []byte("crash matrix body")); out.err != nil || !out.acked {
		t.Fatalf("clean pipeline failed: %+v", out)
	}
	lg.Close()
	totalOps := probe.MutatingOps() - before
	if totalOps < 8 {
		t.Fatalf("pipeline has suspiciously few fault points: %d", totalOps)
	}

	ackedBefore := baselineEventIDs(t, base, c0)

	for _, mode := range []string{"sigkill", "powersim"} {
		for i := 1; i <= totalOps; i++ {
			label := fmt.Sprintf("%s@op%d", mode, i)
			m := base.Clone()
			c := *c0

			lg, _, err := cairnlog.Open(m, "/p", origin(&c), identity.NewChainVerifier().Verify, nil)
			if err != nil {
				t.Fatalf("%s: pre-crash open: %v", label, err)
			}
			m.CrashAt(i)
			out := sendPipeline(m, "/p", lg, &c, "/dev", []byte("crash matrix body"))
			lg.Close()

			if mode == "powersim" {
				m.PowerCycle()
			} else {
				m.Restart()
			}
			recoverAndCheck(t, m, &c, ackedBefore, out, label)
		}
	}
}

// TestCrashAfterAck is row 7: run the pipeline to completion (acked), crash
// afterwards in both modes — event AND object must survive.
func TestCrashAfterAck(t *testing.T) {
	for _, mode := range []string{"sigkill", "powersim"} {
		m, c := buildBase(t)
		ackedBefore := baselineEventIDs(t, m, c)
		lg, _, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := sendPipeline(m, "/p", lg, c, "/dev", []byte("row seven body"))
		if !out.acked {
			t.Fatalf("pipeline not acked: %v", out.err)
		}
		lg.Close()
		if mode == "powersim" {
			m.PowerCycle()
		} else {
			m.Restart()
		}
		recoverAndCheck(t, m, c, ackedBefore, out, "after-ack/"+mode)
	}
}

// TestInjectedWriteFailures covers TESTING.md §2: ENOSPC / EIO / short
// writes at every pipeline op → never an ack, clear error, clean recovery,
// and a retry that succeeds.
func TestInjectedWriteFailures(t *testing.T) {
	base, c0 := buildBase(t)
	probe := base.Clone()
	cProbe := *c0
	lg, _, _ := cairnlog.Open(probe, "/p", origin(&cProbe), identity.NewChainVerifier().Verify, nil)
	before := probe.MutatingOps()
	sendPipeline(probe, "/p", lg, &cProbe, "/dev", []byte("fault body"))
	lg.Close()
	totalOps := probe.MutatingOps() - before

	faults := []struct {
		name  string
		err   error
		short int
	}{
		{"enospc", syscall.ENOSPC, 0},
		{"eio", syscall.EIO, 0},
		{"shortwrite", syscall.EIO, 3},
	}
	ackedBefore := baselineEventIDs(t, base, c0)

	for _, f := range faults {
		for i := 1; i <= totalOps; i++ {
			label := fmt.Sprintf("%s@op%d", f.name, i)
			m := base.Clone()
			c := *c0
			lg, _, err := cairnlog.Open(m, "/p", origin(&c), identity.NewChainVerifier().Verify, nil)
			if err != nil {
				t.Fatalf("%s: open: %v", label, err)
			}
			m.FailAt(i, f.err, f.short)
			out := sendPipeline(m, "/p", lg, &c, "/dev", []byte("fault body"))
			lg.Close()

			// A fault before the append's fdatasync must never ack. Faults in
			// the cache update (after ack) legitimately leave acked=true.
			if out.err == nil && !out.acked {
				t.Fatalf("%s: pipeline reported neither ack nor error", label)
			}

			recoverAndCheck(t, m, &c, ackedBefore, out, label)

			// retry after the transient fault must succeed
			lg2, _, err := cairnlog.Open(m, "/p", origin(&c), identity.NewChainVerifier().Verify, nil)
			if err != nil {
				t.Fatalf("%s: reopen: %v", label, err)
			}
			c2 := c
			c2.nextSeq = lg2.NextSeq()
			c2.lastID = lg2.LastEventID()
			out2 := sendPipeline(m, "/p", lg2, &c2, "/dev", []byte("fault body retry"))
			lg2.Close()
			if !out2.acked || out2.err != nil {
				t.Fatalf("%s: retry failed: %+v", label, out2)
			}
		}
	}
}

// baselineEventIDs reads the pre-crash acked event ids from a clone.
func baselineEventIDs(t *testing.T, base *fsx.MemFS, c *chain) []string {
	t.Helper()
	m := base.Clone()
	var ids []string
	lg, _, err := cairnlog.Open(m, "/p", origin(c), identity.NewChainVerifier().Verify,
		func(env *event.Envelope, _ []byte) error {
			ids = append(ids, env.EventID)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	lg.Close()
	return ids
}

// TestSealCrashMatrix is row 11: crash at every op of a threshold-crossing
// append+seal. After recovery: the open segment is valid OR exactly one
// valid sealed segment exists — and recovery completes a pending seal.
func TestSealCrashMatrix(t *testing.T) {
	const sealEvery = 4 // genesis + 3 → threshold crossing on the 4th append
	newBase := func(t *testing.T) (*fsx.MemFS, *chain, []*preppedEvent) {
		m := fsx.NewMemFS()
		m.MkdirAll("/p", 0o700)
		c, genv, grec := newChain(t)
		lg, err := cairnlog.Create(m, "/p", origin(c), cairnlog.WithSealThresholds(1<<30, sealEvery))
		if err != nil {
			t.Fatal(err)
		}
		if err := lg.Append(grec, genv); err != nil {
			t.Fatal(err)
		}
		var evs []*preppedEvent
		for i := 0; i < 2; i++ {
			env, rec := c.next(t)
			if err := lg.Append(rec, env); err != nil {
				t.Fatal(err)
			}
			evs = append(evs, &preppedEvent{env, rec})
		}
		lg.Close()
		return m, c, evs
	}

	// count ops of the sealing append
	m0, c0, _ := newBase(t)
	probe := m0.Clone()
	cp := *c0
	lg, _, err := cairnlog.Open(probe, "/p", origin(&cp), identity.NewChainVerifier().Verify, nil,
		cairnlog.WithSealThresholds(1<<30, sealEvery))
	if err != nil {
		t.Fatal(err)
	}
	before := probe.MutatingOps()
	env, rec := cp.next(t)
	if err := lg.Append(rec, env); err != nil {
		t.Fatal(err)
	}
	lg.Close()
	totalOps := probe.MutatingOps() - before
	// sanity: the clean run must actually have sealed
	if _, err := probe.ReadFile(filepath.Join(cairnlog.SegmentDir("/p", cp.deviceID, 1), "seg_00000001.seal.json")); err != nil {
		t.Fatal("clean run did not seal")
	}

	for _, mode := range []string{"sigkill", "powersim"} {
		for i := 1; i <= totalOps; i++ {
			label := fmt.Sprintf("seal/%s@op%d", mode, i)
			m := m0.Clone()
			c := *c0
			lg, _, err := cairnlog.Open(m, "/p", origin(&c), identity.NewChainVerifier().Verify, nil,
				cairnlog.WithSealThresholds(1<<30, sealEvery))
			if err != nil {
				t.Fatalf("%s: open: %v", label, err)
			}
			m.CrashAt(i)
			env, rec := c.next(t)
			appendErr := lg.Append(rec, env)
			lg.Close()
			if mode == "powersim" {
				m.PowerCycle()
			} else {
				m.Restart()
			}

			// recovery must succeed and leave a coherent seal state
			lg2, report, err := cairnlog.Open(m, "/p", origin(&c), identity.NewChainVerifier().Verify, nil,
				cairnlog.WithSealThresholds(1<<30, sealEvery))
			if err != nil {
				t.Fatalf("%s: recovery: %v", label, err)
			}
			lg2.Close()
			if appendErr == nil && report.Events != 4 {
				t.Fatalf("%s: acked sealing append lost (%d events)", label, report.Events)
			}
			// after recovery the segment must be sealed iff all 4 events are
			// present (recovery completes interrupted seals)
			sealPath := filepath.Join(cairnlog.SegmentDir("/p", c.deviceID, 1), "seg_00000001.seal.json")
			_, sealErr := m.ReadFile(sealPath)
			if report.Events == 4 && sealErr != nil {
				t.Fatalf("%s: 4 events recovered but segment not sealed", label)
			}
			if report.Events < 4 && sealErr == nil {
				t.Fatalf("%s: sealed with only %d events", label, report.Events)
			}

			doc, err := cairnlog.Doctor(m, "/p", identity.NewChainVerifier().Verify)
			if err != nil || !doc.Clean() {
				t.Fatalf("%s: doctor after seal recovery: err %v, clean %v", label, err, doc != nil && doc.Clean())
			}
		}
	}
}

type preppedEvent struct {
	env *event.Envelope
	rec []byte
}
