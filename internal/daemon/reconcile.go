package daemon

// N6 reconciliation + text replication (buildpack N6; spec §6.2; R29/R30).
//
// After the peer.Server handshake authenticates a connection (R27), the two
// nodes speak this newline-delimited JSON protocol over the SAME connection:
//
//	frontier      — each side's per-origin highest-contiguous frontier
//	get_range     — "send me [from,to) of this origin" (a PULL)
//	push_records  — "here are [from,to) of this origin" (a PUSH; ingest)
//	records       — the response to get_range
//	ack           — the response to push_records (peer's new frontier)
//	done          — initiator finished; responder returns
//
// The INITIATOR (SyncWith) drives BOTH directions over one connection, so a
// single dial fully converges both nodes: it pushes origins the peer is behind
// on and pulls origins it is itself behind on. The responder (serveSync) only
// answers. Ingest is idempotent by (origin, sequence): a record at or below
// our frontier is a no-op; every record is hash+signature verified BEFORE it
// is appended or indexed (spec §6.2). Durable-log internals (framing, signing,
// sealing) are untouched — foreign-origin ingest reuses the public log.Append,
// which enforces contiguity, chaining, framing, fsync, and sealing exactly as
// the local write path does.
//
// Text-replication policy (buildpack N6): canonical + eager message bodies
// replicate to all full nodes (shipped on both PULL and PUSH); ephemeral
// bodies ship only on a live PUSH (currently-connected peer) and are never
// backfilled via a PULL response.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/event"
	cairnlog "github.com/ggoosen/cairn/internal/log"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/peer"
)

// originRef names one append chain on the wire.
type originRef struct {
	DeviceID   string `json:"device_id"`
	Generation int    `json:"generation"`
}

func (o originRef) log() cairnlog.Origin {
	return cairnlog.Origin{DeviceID: o.DeviceID, Generation: o.Generation}
}
func refOf(o cairnlog.Origin) originRef {
	return originRef{DeviceID: o.DeviceID, Generation: o.Generation}
}

// originFrontier is one origin's highest-contiguous position (next expected
// sequence + the current chain head id).
type originFrontier struct {
	originRef
	NextSeq     int64  `json:"next_seq"`
	LastEventID string `json:"last_event_id,omitempty"`
}

// syncMsg is one reconciliation message.
type syncMsg struct {
	Type     string            `json:"type"`
	Frontier []originFrontier  `json:"frontier,omitempty"`
	Origin   *originRef        `json:"origin,omitempty"`
	FromSeq  int64             `json:"from_seq,omitempty"`
	ToSeq    int64             `json:"to_seq,omitempty"` // exclusive
	Records  [][]byte          `json:"records,omitempty"`
	Bodies   map[string][]byte `json:"bodies,omitempty"` // body_hash → text bytes
	Bulk     bool              `json:"bulk,omitempty"`   // R30 bulk catch-up marker
	Err      string            `json:"err,omitempty"`
	Role     string            `json:"role,omitempty"` // P3-3: sender's node role (full/thin), advertised on the frontier

	// N7 blob replication
	Hashes  []string `json:"hashes,omitempty"`  // blob_inv: blobs the sender holds
	Hash    string   `json:"hash,omitempty"`    // get_object / put_object / object
	Data    []byte   `json:"data,omitempty"`    // object / put_object bytes
	Missing bool     `json:"missing,omitempty"` // object: the sender lacks it
}

func writeSync(w io.Writer, m syncMsg) error {
	blob, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(blob, '\n'))
	return err
}

func readSync(r *bufio.Reader) (*syncMsg, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var m syncMsg
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// activeOrigin is this node's own writable origin.
func (d *Daemon) activeOrigin() cairnlog.Origin {
	return cairnlog.Origin{DeviceID: d.loaded.Device.DeviceID, Generation: d.loaded.Device.OriginGeneration}
}

// Frontiers snapshots the per-origin frontier under the writer lock. The
// active origin's position comes from the live append handle; every foreign
// origin has a cached append handle (opened at recover / lazily on ingest).
func (d *Daemon) Frontiers() ([]originFrontier, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.frontiersLocked()
}

func (d *Daemon) frontiersLocked() ([]originFrontier, error) {
	out := []originFrontier{}
	if d.lg != nil {
		out = append(out, originFrontier{
			originRef:   refOf(d.activeOrigin()),
			NextSeq:     d.lg.NextSeq(),
			LastEventID: d.lg.LastEventID(),
		})
	}
	for o, lg := range d.logs {
		out = append(out, originFrontier{
			originRef:   refOf(o),
			NextSeq:     lg.NextSeq(),
			LastEventID: lg.LastEventID(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceID != out[j].DeviceID {
			return out[i].DeviceID < out[j].DeviceID
		}
		return out[i].Generation < out[j].Generation
	})
	return out, nil
}

// foreignLog returns the append handle for a foreign origin, opening (and
// caching) it on first use. A brand-new origin (a peer's, never seen) opens
// fresh at sequence 1. Caller holds d.mu.
func (d *Daemon) foreignLog(o cairnlog.Origin) (*cairnlog.Log, error) {
	if o == d.activeOrigin() {
		return nil, errors.New("refusing an append handle for our OWN active origin via replication")
	}
	if lg, ok := d.logs[o]; ok {
		return lg, nil
	}
	lg, _, err := cairnlog.Open(d.fs, d.dir, o, d.trust.Verifier(), nil)
	if err != nil {
		return nil, err
	}
	d.logs[o] = lg
	return lg, nil
}

// replBody is one text object referenced by an event, with its class.
type replBody struct {
	hash  string
	class string
}

// bodiesForRepl returns the message-body objects an event references (spec
// §5 text classes). N6 replicates message.publish / message.reply bodies and
// revise_body revision bodies; attachments/derivative text are N7 (lazy blob
// fetch + local re-derivation). Unknown event types reference no text bodies.
func bodiesForRepl(env *event.Envelope) []replBody {
	var out []replBody
	switch env.EventType {
	case "message.publish", "message.reply":
		var pl struct {
			BodyHash  string `json:"body_hash"`
			TextClass string `json:"text_class"`
		}
		if json.Unmarshal(env.Payload, &pl) == nil && pl.BodyHash != "" {
			out = append(out, replBody{hash: pl.BodyHash, class: pl.TextClass})
		}
	case "revise_body":
		var pl struct {
			Revisions []struct {
				BodyHash  string `json:"body_hash"`
				TextClass string `json:"text_class"`
			} `json:"revisions"`
			TextClass string `json:"text_class"`
		}
		if json.Unmarshal(env.Payload, &pl) == nil {
			for _, r := range pl.Revisions {
				if r.BodyHash != "" {
					cls := r.TextClass
					if cls == "" {
						cls = pl.TextClass
					}
					out = append(out, replBody{hash: r.BodyHash, class: cls})
				}
			}
		}
	}
	return out
}

func replicatesToFullNode(class string) bool {
	// canonical + eager text replicate to all full nodes (buildpack N6).
	// An empty class defaults to canonical (the daemon's publish default).
	return class == object.ClassCanonical || class == object.ClassEager || class == ""
}

// readRange reads verified records [from,to) of an origin from its segment
// files, plus the body objects they reference under the replication policy.
// livePush marks a PUSH to a currently-connected peer: ephemeral bodies are
// shipped ONLY on a live push AND only while the event is inside the R50
// live-delivery window — a catch-up push after a rejoin is a backfill, not
// live gossip, and must not carry ephemeral content. A PULL response never
// backfills ephemeral text. Caller holds d.mu.
func (d *Daemon) readRange(o cairnlog.Origin, from, to int64, livePush bool) ([][]byte, map[string][]byte, error) {
	if to <= from {
		return nil, nil, nil
	}
	dir := cairnlog.SegmentDir(d.dir, o.DeviceID, o.Generation)
	entries, err := d.fs.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var firsts []int64
	for _, e := range entries {
		if fs, ok := parseSegSeq(e.Name()); ok {
			firsts = append(firsts, fs)
		}
	}
	sort.Slice(firsts, func(i, j int) bool { return firsts[i] < firsts[j] })

	records := [][]byte{}
	bodies := map[string][]byte{}
	verify := d.trust.Verifier()
	for _, first := range firsts {
		// records in this segment cover [first, first+count)
		recs, err := cairnlog.ReadSegment(d.fs, filepath.Join(dir, cairnlog.SegmentName(first)))
		if err != nil {
			return nil, nil, fmt.Errorf("reading segment %d of origin %s/%d: %w", first, o.DeviceID, o.Generation, err)
		}
		last := first + int64(len(recs)) // exclusive
		if last <= from || first >= to {
			continue // no overlap
		}
		for i, rec := range recs {
			seq := first + int64(i)
			if seq < from || seq >= to {
				continue
			}
			records = append(records, rec)
			env, verr := verify(rec)
			if verr != nil {
				// our own on-disk records are already verified; a failure here
				// is corruption we must not silently ship.
				return nil, nil, fmt.Errorf("origin %s/%d seq %d fails verification on read: %w", o.DeviceID, o.Generation, seq, verr)
			}
			for _, b := range bodiesForRepl(env) {
				// R50 sender gate: an ephemeral body is OFFERED only on a live
				// push while its event is inside the live-delivery window.
				// Outside it, the event ships without its content — the peer
				// was not a live recipient at publish time.
				if !replicatesToFullNode(b.class) &&
					!(livePush && b.class == object.ClassEphemeral &&
						d.withinEphemeralWindow(env.WallTime, d.ephemeralSendWindow())) {
					continue
				}
				if _, have := bodies[b.hash]; have {
					continue
				}
				if data, err := d.store.Get(b.hash); err == nil {
					bodies[b.hash] = data
				}
				// a missing local body is not fatal: the peer indexes what it
				// can and re-fetches lazily (N7). Ephemeral may have expired.
			}
		}
	}
	return records, bodies, nil
}

// ingestRecords verifies and appends a contiguous run of foreign-origin
// records (idempotent by sequence), storing any shipped body objects first
// (durability ordering: objects → event → index). It returns how many were
// newly appended. Caller holds d.mu. Records for OUR active origin are refused
// (equivocation is N8, never silently ingested).
func (d *Daemon) ingestRecords(recs [][]byte, bodies map[string][]byte, peerHint string) (int, error) {
	if len(recs) == 0 {
		return 0, nil
	}
	verify := d.trust.Verifier()
	appended := 0
	for _, rec := range recs {
		env, err := verify(rec)
		if err != nil {
			return appended, fmt.Errorf("rejecting unverifiable replicated record (hash/sig): %w", err)
		}
		o := cairnlog.Origin{DeviceID: env.OriginDeviceID, Generation: env.OriginGeneration}
		if d.isFrozen(o) {
			continue // R33: a frozen origin ingests nothing until resolved
		}
		if o == d.activeOrigin() {
			// We are the SOLE legitimate writer of our active origin. A peer
			// event that conflicts with what we hold (different id at a seq we
			// have) OR extends beyond our head is equivocation — a device clone
			// wrote it (N8). Freeze + quarantine; never ingest.
			mineID, qerr := d.proj.EventIDAt(o.DeviceID, o.Generation, env.OriginSequence)
			if qerr != nil {
				return appended, qerr
			}
			if env.OriginSequence < d.lg.NextSeq() && mineID == env.EventID {
				continue // an exact copy of our own event — idempotent no-op
			}
			if err := d.recordForkFromIngest(o, env, rec, d.lg.NextSeq(), d.lg.LastEventID(), peerHint); err != nil {
				return appended, err
			}
			return appended, &forkError{fmt.Sprintf("equivocation on OUR active origin %s/%d at seq %d (device clone)", o.DeviceID, o.Generation, env.OriginSequence)}
		}
		lg, err := d.foreignLog(o)
		if err != nil {
			return appended, err
		}
		next := lg.NextSeq()
		if env.OriginSequence < next {
			// we already have this sequence — is it the SAME event?
			mineID, qerr := d.proj.EventIDAt(o.DeviceID, o.Generation, env.OriginSequence)
			if qerr != nil {
				return appended, qerr
			}
			if mineID == env.EventID {
				continue // identical (at-least-once → idempotent)
			}
			// same coordinate, different id, both valid → equivocation (N8)
			if err := d.recordForkFromIngest(o, env, rec, next, lg.LastEventID(), peerHint); err != nil {
				return appended, err
			}
			return appended, &forkError{fmt.Sprintf("equivocation on origin %s/%d at seq %d", o.DeviceID, o.Generation, env.OriginSequence)}
		}
		if env.OriginSequence > next {
			// a gap: the caller must deliver contiguously from our frontier.
			return appended, fmt.Errorf("replication gap on origin %s/%d: have up to %d, got %d",
				o.DeviceID, o.Generation, next-1, env.OriginSequence)
		}
		// objects before event (rulings §3 durability ordering)
		for _, b := range bodiesForRepl(env) {
			// R50 accept gate (defense in depth): an ephemeral body is STORED
			// only when its event's wall time is inside the accept window —
			// a nonconforming sender's late backfill is refused here even
			// though a conforming sender never offers it. The event itself is
			// always ingested (chain data).
			if b.class == object.ClassEphemeral &&
				!d.withinEphemeralWindow(env.WallTime, d.ephemeralAcceptWindow()) {
				if _, offered := bodies[b.hash]; offered {
					fmt.Fprintf(d.warn, "sync: REFUSED ephemeral body %s from %s — event %s is outside the live-delivery window (R50: ephemeral is never backfilled)\n",
						b.hash, peerHint, env.EventID)
				}
				continue
			}
			if data, ok := bodies[b.hash]; ok && !d.store.Exists(b.hash) {
				if _, err := d.store.Put(data); err != nil {
					return appended, fmt.Errorf("storing replicated body %s: %w", b.hash, err)
				}
			}
		}
		if err := lg.Append(rec, env); err != nil {
			// A contiguous event whose previous_origin_event_id does not match
			// our chain head chains to a DIFFERENT history at seq-1 →
			// equivocation the frontier hid (N8 backstop).
			if env.PreviousOriginEventID != lg.LastEventID() {
				if ferr := d.recordForkFromIngest(o, env, rec, next, lg.LastEventID(), peerHint); ferr != nil {
					return appended, ferr
				}
				return appended, &forkError{fmt.Sprintf("equivocation on origin %s/%d at seq %d (chain diverges from our head)", o.DeviceID, o.Generation, env.OriginSequence)}
			}
			return appended, fmt.Errorf("appending replicated event %s: %w", env.EventID, err)
		}
		d.applyProjection(env, rec)
		appended++
	}
	// R49.2: after ingesting a reconcile batch, sweep retryable parks — a
	// cross-origin topic.link.add that parked when it arrived ahead of its
	// topic.create self-heals now that this batch may have delivered the create.
	// One bounded sweep per batch, not per event (avoids O(events×parked)).
	if appended > 0 {
		if _, err := d.proj.RetryParked(); err != nil {
			fmt.Fprintf(d.warn, "WARNING: retry-parked sweep after ingest failed: %v\n", err)
		}
	}
	return appended, nil
}

// serveSync is the responder side of one authenticated connection: answer the
// initiator's frontier/get_range/push_records requests until it says done or
// the connection closes. It NEVER holds the writer lock across network I/O.
func (d *Daemon) serveSync(conn net.Conn, r *bufio.Reader, peerDevice string) {
	for {
		req, err := readSync(r)
		if err != nil {
			return // connection closed / initiator done
		}
		switch req.Type {
		case "frontier":
			fr, err := d.Frontiers()
			if err != nil {
				writeSync(conn, syncMsg{Type: "error", Err: err.Error()})
				return
			}
			// G7.3: symmetric fork detection. The initiator now sends its
			// frontier; if it collides with ours (same next-seq, different head)
			// this is equivocation the server would otherwise miss. Log loudly
			// and kick a pull-back so the precise investigation + quarantine runs.
			if d.detectFrontierForkFromPeer(req.Frontier, peerDevice) {
				d.kickSync()
			}
			// P3-3: learn the initiator's node role (durability excludes thin).
			d.mu.Lock()
			d.recordPeerRole(peerDevice, req.Role)
			d.mu.Unlock()
			if err := writeSync(conn, syncMsg{Type: "frontier", Frontier: fr, Role: d.myRole()}); err != nil {
				return
			}
		case "get_range":
			if req.Origin == nil {
				writeSync(conn, syncMsg{Type: "error", Err: "get_range without origin"})
				return
			}
			d.mu.Lock()
			recs, bodies, err := d.readRange(req.Origin.log(), req.FromSeq, req.ToSeq, false)
			d.mu.Unlock()
			if err != nil {
				fmt.Fprintf(d.warn, "sync: serving range %s/%d [%d,%d) to %s failed: %v\n",
					req.Origin.DeviceID, req.Origin.Generation, req.FromSeq, req.ToSeq, peerDevice, err)
				writeSync(conn, syncMsg{Type: "error", Err: err.Error()})
				return
			}
			if err := writeSync(conn, syncMsg{Type: "records", Origin: req.Origin, Records: recs, Bodies: bodies}); err != nil {
				return
			}
		case "push_records":
			d.mu.Lock()
			n, err := d.ingestRecords(req.Records, req.Bodies, peerDevice)
			var fr []originFrontier
			if err == nil {
				fr, _ = d.frontiersLocked()
			}
			d.mu.Unlock()
			if fe := asForkError(err); fe != nil {
				// N8: a peer pushed a forked branch. We froze + quarantined it;
				// tell the peer this origin is forked (its push is refused) but
				// the connection continues for other origins (R33).
				fmt.Fprintf(d.warn, "sync: FORK detected on push from %s: %v\n", peerDevice, err)
				if werr := writeSync(conn, syncMsg{Type: "ack", Frontier: nil, Err: "origin frozen: " + fe.Error()}); werr != nil {
					return
				}
				continue
			}
			if err != nil {
				fmt.Fprintf(d.warn, "sync: ingest from %s failed: %v\n", peerDevice, err)
				writeSync(conn, syncMsg{Type: "error", Err: err.Error()})
				return
			}
			if n > 0 {
				fmt.Fprintf(d.warn, "sync: ingested %d event(s) from %s\n", n, peerDevice)
			}
			if err := writeSync(conn, syncMsg{Type: "ack", Frontier: fr}); err != nil {
				return
			}
		case "blob_inv":
			// the initiator advertised the blobs it holds — record it as a
			// holder of each, then reply with our own inventory.
			d.mu.Lock()
			for _, h := range req.Hashes {
				d.durab.addPeerHolder(h, peerDevice)
			}
			targets, err := d.targetBlobs()
			var held []string
			if err == nil {
				for h := range targets {
					if d.store.Exists(h) {
						held = append(held, h)
					}
				}
			}
			d.mu.Unlock()
			if err != nil {
				writeSync(conn, syncMsg{Type: "error", Err: err.Error()})
				return
			}
			d.durab.save(d.fs, d.dir)
			sort.Strings(held)
			if err := writeSync(conn, syncMsg{Type: "blob_inv", Hashes: held}); err != nil {
				return
			}
		case "get_object":
			// serve a blob, verifying the bytes hash to the address BEFORE
			// serving (R31). The initiator is fetching it → it will hold it.
			// R50 serve gate: an object known ONLY as ephemeral content is
			// NEVER served — not even by a cache that legitimately received it
			// live (cache-then-advertise stops at ephemeral). Answer Missing,
			// exactly as if we did not hold it.
			ephOnly, cerr := d.proj.EphemeralOnlyObject(req.Hash)
			if cerr != nil {
				writeSync(conn, syncMsg{Type: "error", Err: cerr.Error()})
				return
			}
			if ephOnly {
				fmt.Fprintf(d.warn, "sync: withheld ephemeral object %s from %s (R50: ephemeral is never re-served)\n", req.Hash, peerDevice)
				if err := writeSync(conn, syncMsg{Type: "object", Hash: req.Hash, Missing: true}); err != nil {
					return
				}
				continue
			}
			d.mu.Lock()
			data, gerr := d.store.Get(req.Hash) // Get re-verifies content-address
			if gerr == nil {
				d.durab.addPeerHolder(req.Hash, peerDevice)
			}
			d.mu.Unlock()
			if gerr != nil {
				if err := writeSync(conn, syncMsg{Type: "object", Hash: req.Hash, Missing: true}); err != nil {
					return
				}
				continue
			}
			d.durab.save(d.fs, d.dir)
			if err := writeSync(conn, syncMsg{Type: "object", Hash: req.Hash, Data: data}); err != nil {
				return
			}
		case "put_object":
			// verify (content-address) before storing (R31); the initiator
			// holds it (it pushed it).
			if len(req.Data) > config.SyncMaxObjectBytes {
				writeSync(conn, syncMsg{Type: "error", Err: "blob exceeds transfer cap"})
				return
			}
			// R50 accept gate: bytes our projection knows ONLY as ephemeral
			// content are refused — ephemeral is never delivered by blob push,
			// only with its event inside the live window. A conforming peer
			// never sends these (targetBlobs excludes ephemeral).
			if ephOnly, cerr := d.proj.EphemeralOnlyObject(req.Hash); cerr != nil {
				writeSync(conn, syncMsg{Type: "error", Err: cerr.Error()})
				return
			} else if ephOnly {
				fmt.Fprintf(d.warn, "sync: REFUSED pushed ephemeral object %s from %s (R50: ephemeral is never backfilled)\n", req.Hash, peerDevice)
				writeSync(conn, syncMsg{Type: "error", Err: "ephemeral object refused (R50: ephemeral content is never backfilled)"})
				return
			}
			d.mu.Lock()
			got, perr := d.store.Put(req.Data)
			if perr == nil && got == req.Hash {
				d.durab.addPeerHolder(req.Hash, peerDevice)
			}
			d.mu.Unlock()
			if perr != nil {
				writeSync(conn, syncMsg{Type: "error", Err: perr.Error()})
				return
			}
			if got != req.Hash {
				fmt.Fprintf(d.warn, "sync: rejected pushed blob from %s: hashes to %s, claimed %s\n", peerDevice, got, req.Hash)
				writeSync(conn, syncMsg{Type: "error", Err: "pushed blob hash mismatch"})
				return
			}
			d.durab.save(d.fs, d.dir)
			if err := writeSync(conn, syncMsg{Type: "put_ack", Hash: req.Hash}); err != nil {
				return
			}
		case "done":
			return
		default:
			writeSync(conn, syncMsg{Type: "error", Err: "unknown sync message " + req.Type})
			return
		}
	}
}

// SyncWith dials a peer, authenticates (R27), and runs one full bidirectional
// reconciliation: it pushes every origin the peer trails on and pulls every
// origin it trails on. Returns the number of events it ingested.
func (d *Daemon) SyncWith(addr string) (int, error) {
	d.mu.Lock()
	if d.lg == nil && !d.readOnly {
		d.mu.Unlock()
		return 0, errors.New("daemon not ready")
	}
	ident := d.syncIdentity()
	trust := d.trust
	tr := d.transport
	d.mu.Unlock()
	if trust == nil {
		return 0, errors.New("no mesh trust")
	}
	if tr == nil {
		return 0, errors.New("sync transport unavailable (see the daemon log)")
	}

	pc, err := peer.DialWithTransport(tr, addr, ident, trust)
	if err != nil {
		return 0, err
	}
	defer pc.Close()

	// 1. frontier exchange. G7.3: include OUR frontier in the request so the
	// responder can run the SAME equal-next-seq/different-head fork check — an
	// equivocating push where neither side trails the other would otherwise be
	// invisible to the server (nothing is behind, so nothing is pushed) until
	// the honest node happens to initiate a pull.
	mine, err := d.Frontiers()
	if err != nil {
		return 0, err
	}
	if err := writeSync(pc, syncMsg{Type: "frontier", Frontier: mine, Role: d.myRole()}); err != nil {
		return 0, err
	}
	resp, err := readSync(pc.R)
	if err != nil {
		return 0, fmt.Errorf("peer closed before frontier: %w", err)
	}
	if resp.Type == "error" {
		return 0, fmt.Errorf("peer error: %s", resp.Err)
	}
	if resp.Type != "frontier" {
		return 0, fmt.Errorf("expected frontier, got %q", resp.Type)
	}
	// P3-3: learn the responder's node role (durability accounting excludes thin).
	d.mu.Lock()
	d.recordPeerRole(pc.PeerDevice, resp.Role)
	d.mu.Unlock()
	peerFr := map[cairnlog.Origin]originFrontier{}
	for _, f := range resp.Frontier {
		peerFr[f.log()] = f
	}

	myFr := map[cairnlog.Origin]originFrontier{}
	for _, f := range mine {
		myFr[f.log()] = f
	}

	// deterministic origin order across both directions
	seen := map[cairnlog.Origin]bool{}
	var origins []cairnlog.Origin
	for o := range myFr {
		if !seen[o] {
			seen[o] = true
			origins = append(origins, o)
		}
	}
	for o := range peerFr {
		if !seen[o] {
			seen[o] = true
			origins = append(origins, o)
		}
	}
	sort.Slice(origins, func(i, j int) bool {
		if origins[i].DeviceID != origins[j].DeviceID {
			return origins[i].DeviceID < origins[j].DeviceID
		}
		return origins[i].Generation < origins[j].Generation
	})

	ingested := 0
	for _, o := range origins {
		// N8/R33: a frozen (forked) origin is skipped entirely; every OTHER
		// origin continues to sync.
		d.mu.Lock()
		frozen := d.isFrozen(o)
		d.mu.Unlock()
		if frozen {
			continue
		}
		mf, mok := myFr[o]
		pf, pok := peerFr[o]
		// N8 fork detection (frontier): both sides reached the SAME next
		// sequence but on DIFFERENT chain heads → the chains diverged =
		// equivocation. Investigate (find the divergence, quarantine the
		// remote branch), freeze, and move on (R33).
		if mok && pok && mf.NextSeq == pf.NextSeq && mf.LastEventID != "" && pf.LastEventID != "" && mf.LastEventID != pf.LastEventID {
			if err := d.investigateFrontierFork(pc, o, mf, pf); err != nil {
				fmt.Fprintf(d.warn, "sync: FORK suspected on origin %s/%d but investigation failed: %v\n", o.DeviceID, o.Generation, err)
			}
			continue
		}
		myNext := int64(config.FirstSequence)
		if mok {
			myNext = mf.NextSeq
		}
		peerNext := int64(config.FirstSequence)
		if pok {
			peerNext = pf.NextSeq
		}
		// PULL: we trail the peer on this origin.
		if myNext < peerNext {
			n, err := d.pullOrigin(pc, o, myNext, peerNext)
			if forked := asForkError(err); forked != nil {
				// N8 backstop: the pulled branch chains to a different history
				// than ours (equivocation at a length the frontier hid). The
				// origin is now frozen; continue with other origins (R33).
				fmt.Fprintf(d.warn, "sync: FORK detected while pulling origin %s/%d: %v\n", o.DeviceID, o.Generation, forked)
				ingested += n
				continue
			}
			if err != nil {
				return ingested, err
			}
			ingested += n
		}
		// PUSH: the peer trails us on this origin.
		if peerNext < myNext {
			if err := d.pushOrigin(pc, o, peerNext, myNext); err != nil {
				return ingested, err
			}
		}
	}
	// N7: blob replication runs AFTER events converge (both sides now know
	// every attachment's durability class). Best-effort — a blob failure does
	// not undo the event convergence.
	if err := d.blobPhase(pc); err != nil {
		fmt.Fprintf(d.warn, "sync: blob phase with %s incomplete: %v\n", pc.PeerDevice, err)
	}
	writeSync(pc, syncMsg{Type: "done"})
	return ingested, nil
}

// pullOrigin requests [from,to) of an origin from the peer in batches,
// re-reading our frontier after each batch (ingest advances it). R30: a large
// deficit is logged as a bulk catch-up.
func (d *Daemon) pullOrigin(pc *peer.Conn, o cairnlog.Origin, from, to int64) (int, error) {
	batch := int64(config.SyncRangeBatch)
	if to-from > int64(d.bulkThreshold()) {
		// R30: a far-behind node is caught up by streaming whole sealed
		// segments rather than per-event ranges — use a segment-sized batch.
		batch = int64(config.SegmentSealEvents)
		fmt.Fprintf(d.warn, "sync: bulk catch-up — pulling %d events of origin %s/%d (streaming segments)\n",
			to-from, o.DeviceID, o.Generation)
	}
	ingested := 0
	next := from
	ref := refOf(o)
	for next < to {
		hi := next + batch
		if hi > to {
			hi = to
		}
		if err := writeSync(pc, syncMsg{Type: "get_range", Origin: &ref, FromSeq: next, ToSeq: hi}); err != nil {
			return ingested, err
		}
		resp, err := readSync(pc.R)
		if err != nil {
			return ingested, fmt.Errorf("peer closed during range transfer: %w", err)
		}
		if resp.Type == "error" {
			return ingested, fmt.Errorf("peer error on range: %s", resp.Err)
		}
		if resp.Type != "records" {
			return ingested, fmt.Errorf("expected records, got %q", resp.Type)
		}
		if len(resp.Records) == 0 {
			break // peer has nothing more for this window
		}
		d.mu.Lock()
		n, err := d.ingestRecords(resp.Records, resp.Bodies, pc.PeerDevice)
		var advanced int64 = -1
		if err == nil {
			if lg, ok := d.logs[o]; ok {
				advanced = lg.NextSeq()
			}
		}
		d.mu.Unlock()
		if err != nil {
			return ingested, err
		}
		ingested += n
		if advanced <= next {
			break // no forward progress (defensive against a stuck peer)
		}
		next = advanced
	}
	return ingested, nil
}

// pushOrigin streams [from,to) of an origin to the peer in batches. It reads
// the peer's ack frontier after each batch and stops when the peer has caught
// up. Ephemeral bodies are shipped (live PUSH to a connected peer).
func (d *Daemon) pushOrigin(pc *peer.Conn, o cairnlog.Origin, from, to int64) error {
	bulk := to-from > int64(d.bulkThreshold())
	batch := int64(config.SyncRangeBatch)
	if bulk {
		// R30: segment-sized batches when the peer is far behind.
		batch = int64(config.SegmentSealEvents)
		fmt.Fprintf(d.warn, "sync: bulk catch-up — pushing %d events of origin %s/%d (streaming segments)\n",
			to-from, o.DeviceID, o.Generation)
	}
	next := from
	ref := refOf(o)
	for next < to {
		hi := next + batch
		if hi > to {
			hi = to
		}
		d.mu.Lock()
		recs, bodies, err := d.readRange(o, next, hi, true)
		d.mu.Unlock()
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			break
		}
		if err := writeSync(pc, syncMsg{Type: "push_records", Origin: &ref, FromSeq: next, ToSeq: hi, Records: recs, Bodies: bodies, Bulk: bulk}); err != nil {
			return err
		}
		resp, err := readSync(pc.R)
		if err != nil {
			return fmt.Errorf("peer closed during push: %w", err)
		}
		if resp.Type == "error" {
			return fmt.Errorf("peer error on push: %s", resp.Err)
		}
		if resp.Type != "ack" {
			return fmt.Errorf("expected ack, got %q", resp.Type)
		}
		next = hi
	}
	return nil
}

// antiEntropyLoop runs a reconcile sweep on the R29 timer AND on every
// push-on-append kick (debounced), until ctx is done.
func (d *Daemon) antiEntropyLoop(ctx context.Context) {
	ticker := time.NewTicker(config.SyncAntiEntropyInterval)
	defer ticker.Stop()
	// an initial sweep so a just-started node converges without waiting a tick
	d.SyncAllPeers()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.SyncAllPeers()
		case <-d.syncKick:
			// debounce a burst of appends into one sweep
			t := time.NewTimer(config.SyncPushDebounce)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			// drain any kicks that arrived during the debounce window
			select {
			case <-d.syncKick:
			default:
			}
			d.SyncAllPeers()
		}
	}
}

// SyncAllPeers runs one reconcile against every configured peer, logging
// per-peer outcomes. Peers that are offline/unreachable are logged and left
// for the next sweep (at-least-once convergence).
func (d *Daemon) SyncAllPeers() {
	d.mu.Lock()
	peers := append([]string(nil), d.loaded.Device.SyncPeers...)
	d.mu.Unlock()
	for _, addr := range peers {
		n, err := d.SyncWith(addr)
		if err != nil {
			fmt.Fprintf(d.warn, "sync: reconcile with %s did not complete: %v\n", addr, err)
			continue
		}
		if n > 0 {
			fmt.Fprintf(d.warn, "sync: reconcile with %s ingested %d event(s)\n", addr, n)
		}
	}
}

func (d *Daemon) bulkThreshold() int {
	if d.syncBulkThreshold > 0 {
		return d.syncBulkThreshold
	}
	return config.SyncBulkCatchupThreshold
}

// --- N7 blob replication ----------------------------------------------------

// targetBlobs returns the set of attachment blob hashes with a NON-ephemeral
// durability class (ephemeral blobs are origin-only, never replicated). A blob
// referenced by more than one message is a target if ANY reference is
// non-ephemeral. Caller holds d.mu.
func (d *Daemon) targetBlobs() (map[string]bool, error) {
	blobs, err := d.proj.AttachmentBlobs()
	if err != nil {
		return nil, err
	}
	targets := map[string]bool{}
	for _, b := range blobs {
		if b.Durability != "ephemeral" {
			targets[b.ObjectHash] = true
		}
	}
	return targets, nil
}

// blobPhase reconciles attachment blobs after the event/text phase: exchange
// inventories, then (as a full node) fetch every target blob it lacks and push
// every target blob the peer lacks. Every transfer is hash-verified before it
// is stored or served (R31); a node that fetches a blob advertises it
// (cache-then-advertise) by recording itself as a holder. Best-effort: blob
// errors are logged, not fatal (events already converged).
// BlobPhaseForTest drives the blob phase with no peer connection — only valid
// when a degradation level ≥ DelayBlobRepl makes it short-circuit before any
// protocol I/O (FIX-H6 rung 5). Panics-safe because the gate returns first.
func (d *Daemon) BlobPhaseForTest() error { return d.blobPhase(nil) }

func (d *Daemon) blobPhase(pc *peer.Conn) error {
	// H6/rung 5 (spec §8.2): under disk pressure, DELAY proactive blob
	// replication — skip the inventory exchange entirely (the initiator then
	// sends `done`; the responder handles it). Durability is eventual: a later
	// sweep replicates the targets once pressure eases, so nothing is lost.
	// Inert on a healthy disk (the cached level is Healthy).
	if lvl := d.DegradationLevel(); lvl.DelayBlobRepl() {
		if pc != nil {
			fmt.Fprintf(d.warn, "sync: degradation %s — delaying blob replication to %s (disk pressure; durability catches up when it eases)\n", lvl, pc.PeerDevice)
		}
		return nil
	}
	d.mu.Lock()
	targets, err := d.targetBlobs()
	if err != nil {
		d.mu.Unlock()
		return err
	}
	held := []string{}
	for h := range targets {
		if d.store.Exists(h) {
			held = append(held, h)
		}
	}
	d.mu.Unlock()
	sort.Strings(held)

	// 1. exchange inventories
	if err := writeSync(pc, syncMsg{Type: "blob_inv", Hashes: held}); err != nil {
		return err
	}
	resp, err := readSync(pc.R)
	if err != nil {
		return fmt.Errorf("peer closed before blob inventory: %w", err)
	}
	if resp.Type == "error" {
		return fmt.Errorf("peer error on blob inventory: %s", resp.Err)
	}
	if resp.Type != "blob_inv" {
		return fmt.Errorf("expected blob_inv, got %q", resp.Type)
	}
	peerHas := map[string]bool{}
	for _, h := range resp.Hashes {
		peerHas[h] = true
	}
	// record the peer as a holder of everything it advertised
	d.mu.Lock()
	for _, h := range resp.Hashes {
		d.durab.addPeerHolder(h, pc.PeerDevice)
	}
	heldSet := map[string]bool{}
	for _, h := range held {
		heldSet[h] = true
	}
	d.mu.Unlock()

	// 2. PULL: fetch every target blob I lack that the peer advertises
	for h := range targets {
		if heldSet[h] || !peerHas[h] {
			continue
		}
		if err := d.fetchBlob(pc, h); err != nil {
			fmt.Fprintf(d.warn, "sync: fetching blob %s from %s failed: %v\n", h, pc.PeerDevice, err)
			continue
		}
	}

	// 3. PUSH: send every target blob the peer lacks that I hold
	for _, h := range held {
		if peerHas[h] {
			continue
		}
		if err := d.pushBlob(pc, h); err != nil {
			fmt.Fprintf(d.warn, "sync: pushing blob %s to %s failed: %v\n", h, pc.PeerDevice, err)
			continue
		}
	}
	return d.durab.save(d.fs, d.dir)
}

// fetchBlob pulls one blob by hash, verifies it (R31 — content-address match),
// and stores it. On success this node holds the blob (cache-then-advertise:
// the object store IS the local advertisement; the peer recorded us as a
// holder when it served the get_object).
func (d *Daemon) fetchBlob(pc *peer.Conn, hash string) error {
	if err := writeSync(pc, syncMsg{Type: "get_object", Hash: hash}); err != nil {
		return err
	}
	resp, err := readSync(pc.R)
	if err != nil {
		return err
	}
	if resp.Type == "error" {
		return fmt.Errorf("peer error: %s", resp.Err)
	}
	if resp.Type != "object" || resp.Hash != hash {
		return fmt.Errorf("expected object %s, got %q/%s", hash, resp.Type, resp.Hash)
	}
	if resp.Missing {
		return fmt.Errorf("peer no longer holds %s", hash)
	}
	if len(resp.Data) > config.SyncMaxObjectBytes {
		return fmt.Errorf("blob %s exceeds transfer cap", hash)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// store.Put is content-addressed: it computes the hash and refuses a
	// collision with different bytes — verify-before-serve (R31).
	got, err := d.store.Put(resp.Data)
	if err != nil {
		return err
	}
	if got != hash {
		return fmt.Errorf("received blob hashes to %s, expected %s (rejected)", got, hash)
	}
	return nil
}

// pushBlob sends one blob to the peer; the peer verifies + stores it and acks.
func (d *Daemon) pushBlob(pc *peer.Conn, hash string) error {
	d.mu.Lock()
	data, err := d.store.Get(hash)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if err := writeSync(pc, syncMsg{Type: "put_object", Hash: hash, Data: data}); err != nil {
		return err
	}
	resp, err := readSync(pc.R)
	if err != nil {
		return err
	}
	if resp.Type == "error" {
		return fmt.Errorf("peer refused blob: %s", resp.Err)
	}
	if resp.Type != "put_ack" || resp.Hash != hash {
		return fmt.Errorf("expected put_ack %s, got %q/%s", hash, resp.Type, resp.Hash)
	}
	// the peer now holds it — advertise (cache-then-advertise for the peer)
	d.mu.Lock()
	d.durab.addPeerHolder(hash, pc.PeerDevice)
	d.mu.Unlock()
	return nil
}

// SetSyncBulkThresholdForTest lowers the R30 bulk-catch-up threshold so the
// segment-streaming path is exercisable without a 10k-event fixture. TEST HOOK.
func (d *Daemon) SetSyncBulkThresholdForTest(n int) {
	d.mu.Lock()
	d.syncBulkThreshold = n
	d.mu.Unlock()
}

// --- R50: ephemeral live-delivery windows -------------------------------------

// ephemeralSendWindow / ephemeralAcceptWindow return the R50 windows (config
// defaults unless test-overridden). Caller holds d.mu or reads are benign.
func (d *Daemon) ephemeralSendWindow() time.Duration {
	if d.ephSendWindow > 0 {
		return d.ephSendWindow
	}
	return config.EphemeralLiveDeliveryWindow
}

func (d *Daemon) ephemeralAcceptWindow() time.Duration {
	if d.ephAcceptWindow > 0 {
		return d.ephAcceptWindow
	}
	return config.EphemeralLiveAcceptWindow
}

// withinEphemeralWindow reports whether an event's wall time is within w of
// local now (absolute — tolerant of small skew in either direction). An
// unparseable wall time is NOT within the window: the gate fails CLOSED —
// content is withheld, never leaked; the event itself always replicates.
func (d *Daemon) withinEphemeralWindow(wallTime string, w time.Duration) bool {
	t, err := time.Parse(time.RFC3339Nano, wallTime)
	if err != nil {
		return false
	}
	dt := d.now().Sub(t)
	if dt < 0 {
		dt = -dt
	}
	return dt <= w
}

// SetEphemeralWindowsForTest overrides the R50 live-delivery windows so the
// rejoin drill doesn't sleep out the real 60 s window. TEST HOOK.
func (d *Daemon) SetEphemeralWindowsForTest(send, accept time.Duration) {
	d.mu.Lock()
	d.ephSendWindow, d.ephAcceptWindow = send, accept
	d.mu.Unlock()
}

// ProbeGetObjectForTest dials addr over the authenticated sync protocol and
// requests one object by hash WITHOUT storing it — a probe for the R50 serve
// gate (does the peer serve these bytes at all?). TEST HOOK.
func (d *Daemon) ProbeGetObjectForTest(addr, hash string) (served bool, err error) {
	d.mu.Lock()
	ident := d.syncIdentity()
	trust := d.trust
	tr := d.transport
	d.mu.Unlock()
	pc, err := peer.DialWithTransport(tr, addr, ident, trust)
	if err != nil {
		return false, err
	}
	defer pc.Close()
	if err := writeSync(pc, syncMsg{Type: "get_object", Hash: hash}); err != nil {
		return false, err
	}
	resp, err := readSync(pc.R)
	if err != nil {
		return false, err
	}
	if resp.Type != "object" {
		return false, fmt.Errorf("expected object, got %q (%s)", resp.Type, resp.Err)
	}
	served = !resp.Missing && len(resp.Data) > 0
	writeSync(pc, syncMsg{Type: "done"})
	return served, nil
}

// ProbePutObjectForTest dials addr and pushes raw object bytes — a probe for
// the R50 accept gate (does the peer store these bytes at all?). A protocol
// refusal returns (false, nil); transport failures return an error. TEST HOOK.
func (d *Daemon) ProbePutObjectForTest(addr, hash string, data []byte) (accepted bool, err error) {
	d.mu.Lock()
	ident := d.syncIdentity()
	trust := d.trust
	tr := d.transport
	d.mu.Unlock()
	pc, err := peer.DialWithTransport(tr, addr, ident, trust)
	if err != nil {
		return false, err
	}
	defer pc.Close()
	if err := writeSync(pc, syncMsg{Type: "put_object", Hash: hash, Data: data}); err != nil {
		return false, err
	}
	resp, err := readSync(pc.R)
	if err != nil {
		return false, err
	}
	if resp.Type == "error" {
		return false, nil // refused — the gate's expected outcome
	}
	if resp.Type != "put_ack" {
		return false, fmt.Errorf("expected put_ack, got %q", resp.Type)
	}
	writeSync(pc, syncMsg{Type: "done"})
	return true, nil
}

// --- N8 fork detection ------------------------------------------------------

// forkError signals that an equivocation was detected (and the origin frozen)
// during ingest. The reconcile loop catches it per-origin so other origins
// keep syncing (R33).
type forkError struct{ msg string }

func (e *forkError) Error() string { return e.msg }

func asForkError(err error) *forkError {
	if err == nil {
		return nil
	}
	var fe *forkError
	if errors.As(err, &fe) {
		return fe
	}
	return nil
}

// investigateFrontierFork handles a frontier-level fork signal (same next
// sequence, different chain head): fetch the peer's overlapping branch, find
// the first divergent sequence, quarantine the peer's divergent suffix, and
// record + freeze the fork. Caller must NOT hold d.mu.
// detectFrontierForkFromPeer runs the equal-next-seq/different-head fork check
// against a peer-supplied frontier (G7.3 — the RESPONDER side of a sync). It
// only DETECTS + logs; the precise branch probe/quarantine needs us to be the
// initiator, so the caller kicks a pull-back. Returns true if any origin
// collides. Never holds d.mu across the whole scan.
func (d *Daemon) detectFrontierForkFromPeer(peerFr []originFrontier, peerDevice string) bool {
	if len(peerFr) == 0 {
		return false
	}
	mine, err := d.Frontiers()
	if err != nil {
		return false
	}
	byOrigin := make(map[cairnlog.Origin]originFrontier, len(mine))
	for _, f := range mine {
		byOrigin[f.log()] = f
	}
	found := false
	for _, pf := range peerFr {
		o := pf.log()
		mf, ok := byOrigin[o]
		if !ok {
			continue
		}
		d.mu.Lock()
		frozen := d.isFrozen(o)
		d.mu.Unlock()
		if frozen {
			continue // already known; no need to re-log
		}
		if mf.NextSeq == pf.NextSeq && mf.LastEventID != "" && pf.LastEventID != "" && mf.LastEventID != pf.LastEventID {
			fmt.Fprintf(d.warn, "sync: FORK SUSPECTED on origin %s/%d from peer %s (equal frontier at seq %d but heads differ: mine %s vs theirs %s) — equivocation a push would hide. Scheduling a pull-back to investigate + quarantine; run `cairn doctor fork %s`.\n",
				o.DeviceID, o.Generation, peerDevice, mf.NextSeq-1, short(mf.LastEventID), short(pf.LastEventID), o.DeviceID)
			found = true
		}
	}
	return found
}

func (d *Daemon) investigateFrontierFork(pc *peer.Conn, o cairnlog.Origin, mine, peer originFrontier) error {
	// fetch the peer's records over the whole overlap [FirstSequence, N)
	from := int64(config.FirstSequence)
	if config.ForkProbeWindow > 0 && peer.NextSeq-int64(config.ForkProbeWindow) > from {
		from = peer.NextSeq - int64(config.ForkProbeWindow)
	}
	peerRecs, err := d.fetchPeerRange(pc, o, from, peer.NextSeq)
	if err != nil {
		return err
	}
	// find the first sequence where the peer's event id differs from ours
	d.mu.Lock()
	defer d.mu.Unlock()
	divSeq := int64(-1)
	for _, pr := range peerRecs {
		mineID, err := d.proj.EventIDAt(o.DeviceID, o.Generation, pr.seq)
		if err != nil {
			return err
		}
		if mineID == "" || mineID != pr.env.EventID {
			divSeq = pr.seq
			break
		}
	}
	if divSeq < 0 {
		// heads differ but no divergence found in the window — divergence is
		// older than the probe window; record a coarse fork at the window edge.
		divSeq = from
	}
	return d.recordForkLocked(o, divSeq, mine, peer, pc.PeerDevice, peerRecs)
}

// peerRecord is one verified record fetched from a peer during fork probing.
type peerRecord struct {
	seq int64
	env *event.Envelope
	rec []byte
}

// fetchPeerRange requests [from,to) of an origin from the peer and returns the
// verified records (used for fork investigation; the peer serves them via the
// ordinary get_range path). Caller must NOT hold d.mu.
func (d *Daemon) fetchPeerRange(pc *peer.Conn, o cairnlog.Origin, from, to int64) ([]peerRecord, error) {
	d.mu.Lock()
	verify := d.trust.Verifier()
	d.mu.Unlock()
	ref := refOf(o)
	var out []peerRecord
	next := from
	for next < to {
		hi := next + int64(config.SyncRangeBatch)
		if hi > to {
			hi = to
		}
		if err := writeSync(pc, syncMsg{Type: "get_range", Origin: &ref, FromSeq: next, ToSeq: hi}); err != nil {
			return nil, err
		}
		resp, err := readSync(pc.R)
		if err != nil {
			return nil, err
		}
		if resp.Type == "error" {
			return nil, fmt.Errorf("peer error: %s", resp.Err)
		}
		if resp.Type != "records" {
			return nil, fmt.Errorf("expected records, got %q", resp.Type)
		}
		if len(resp.Records) == 0 {
			break
		}
		for _, rec := range resp.Records {
			env, verr := verify(rec)
			if verr != nil {
				// a peer branch that does NOT verify is corruption, not
				// equivocation (both branches must be validly signed).
				return nil, fmt.Errorf("peer branch record at ~seq %d does not verify (corruption, not equivocation): %w", next, verr)
			}
			out = append(out, peerRecord{seq: env.OriginSequence, env: env, rec: rec})
		}
		next = out[len(out)-1].seq + 1
	}
	return out, nil
}

// recordForkLocked builds, quarantines, persists, and freezes a fork. Caller
// holds d.mu. Idempotent: an existing unresolved fork for the origin is left
// as-is (the first detection wins; re-detection does not churn the record).
func (d *Daemon) recordForkLocked(o cairnlog.Origin, divSeq int64, mine, peer originFrontier, peerDevice string, peerRecs []peerRecord) error {
	if f, ok := d.forks[o]; ok && !f.Resolved {
		return nil // already frozen
	}
	// quarantine the peer's divergent suffix [divSeq, peer.NextSeq) forever
	var remoteBranch []ForkBranchEvent
	security := false
	for _, pr := range peerRecs {
		if pr.seq < divSeq {
			continue
		}
		if err := quarantineFrame(d.dir, o, pr.seq, pr.env.EventID, pr.rec); err != nil {
			return fmt.Errorf("quarantining forked frame: %w", err)
		}
		remoteBranch = append(remoteBranch, ForkBranchEvent{Seq: pr.seq, EventID: pr.env.EventID, EventType: pr.env.EventType})
		if isSecurityType(pr.env.EventType) {
			security = true
		}
	}
	// my divergent suffix summary (my branch [divSeq, mine.NextSeq))
	localBranch, err := d.proj.EventsInRange(o.DeviceID, o.Generation, divSeq, mine.NextSeq)
	if err != nil {
		return err
	}
	lb := make([]ForkBranchEvent, 0, len(localBranch))
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
		LocalHead:     mine.LastEventID,
		LocalHeadSeq:  mine.NextSeq - 1,
		RemoteHead:    peer.LastEventID,
		RemoteHeadSeq: peer.NextSeq - 1,
		DetectedPeer:  peerDevice,
		DetectedAt:    d.now().UTC().Format(config.WallTimeFormat),
		SecurityOps:   security,
		RemoteBranch:  remoteBranch,
		LocalBranch:   lb,
	}
	d.forks[o] = f
	if err := saveForkRecord(d.dir, f); err != nil {
		return err
	}
	fmt.Fprintf(d.warn, "FORK DETECTED: origin %s/%d equivocates — common ancestor seq %d; local head %s (seq %d), remote head %s (seq %d) advertised by %s%s. Ingest FROZEN for this origin (others unaffected). Run `cairn doctor fork %s` and repair per DOGFOOD §14. The losing branch is quarantined, never deleted.\n",
		o.DeviceID, o.Generation, divSeq-1, short(f.LocalHead), f.LocalHeadSeq, short(f.RemoteHead), f.RemoteHeadSeq, peerDevice,
		map[bool]string{true: " [SECURITY OPS ON A BRANCH]", false: ""}[security], o.DeviceID)
	return nil
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12] + "…"
	}
	return id
}

// parseSegSeq extracts the first sequence from a "seg_00000001.seg" name.
func parseSegSeq(name string) (int64, bool) {
	if !strings.HasPrefix(name, "seg_") || !strings.HasSuffix(name, ".seg") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, "seg_"), ".seg"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
