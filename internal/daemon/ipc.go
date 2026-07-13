package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/projection"
)

// IPC: one JSON request line per connection over a unix socket, one JSON
// response line back. The socket lives in device-local state territory but
// under a short path (unix sockets cap ~104 bytes): the daemon writes the
// actual socket path into <deviceDir>/daemon.sock.path for clients.

// Request is the IPC envelope.
type Request struct {
	Op string `json:"op"`

	// N2 capability session: the opaque handle this request runs under.
	// Absent = local operator CLI = tier-1 full (rulings §7.2).
	Session string `json:"session,omitempty"`

	// session-create / session-revoke parameters
	SessionName    string `json:"session_name,omitempty"`
	SessionProfile string `json:"session_profile,omitempty"`
	SessionPID     int    `json:"session_pid,omitempty"`
	TargetSession  string `json:"target_session,omitempty"`

	Publish *PublishRequest `json:"publish,omitempty"`

	// G6: streamed attachment staging. The daemon reads AttachMeta.ByteLen raw
	// bytes off the connection AFTER this JSON header line (not inlined/base64),
	// stores the object, and returns its hash — so large/multiple attachments
	// never inflate one IPC request past IPCMaxRequestBytes.
	AttachMeta *AttachMeta `json:"attach_meta,omitempty"`

	// simple mutations
	MessageID  string `json:"message_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
	TopicID    string `json:"topic_id,omitempty"`
	TopicName  string `json:"topic_name,omitempty"`
	LinkID     string `json:"link_id,omitempty"`
	Protected  bool   `json:"protected,omitempty"`
	PinID      string `json:"pin_id,omitempty"`
	ObjectRef  string `json:"object_ref,omitempty"` // object hash for pin
	Durability string `json:"durability,omitempty"`
	Kind       string `json:"kind,omitempty"` // signal kind
	Weight     int    `json:"weight,omitempty"`
	Actor      string `json:"actor,omitempty"`

	// exports
	Path string `json:"path,omitempty"`

	// telemetry / reserve
	Outcome string         `json:"outcome,omitempty"`
	Search2 *SearchOptions `json:"search_opts,omitempty"`

	// ingest (M9)
	Body       string `json:"body,omitempty"`
	SourcePath string `json:"source_path,omitempty"`

	// derivatives (N4)
	DerivativeID string `json:"derivative_id,omitempty"`

	// N6 sync: optional explicit peer address ("" = every configured peer)
	Peer string `json:"peer,omitempty"`

	// durable subscriptions (N3)
	Subscribe      *SubscribeRequest `json:"subscribe,omitempty"`
	SubUpdate      *SubUpdateRequest `json:"sub_update,omitempty"`
	SubscriptionID string            `json:"subscription_id,omitempty"`

	// reads
	Query            string `json:"query,omitempty"`
	K                int    `json:"k,omitempty"`
	BudgetChars      int    `json:"budget_chars,omitempty"`
	IncludeRetracted bool   `json:"include_retracted,omitempty"`
	AgentView        string `json:"agent_view,omitempty"` // fetch/digest view
	InteractionID    string `json:"interaction_id,omitempty"`
}

// Response is the IPC reply.
type Response struct {
	OK         bool                         `json:"ok"`
	Error      string                       `json:"error,omitempty"`
	Publish    *PublishResult               `json:"publish,omitempty"`
	EventID    string                       `json:"event_id,omitempty"`
	Results    []projection.SearchResult    `json:"results,omitempty"`
	Search     *SearchOutput                `json:"search,omitempty"`
	Digest     *DigestOutput                `json:"digest,omitempty"`
	Text       string                       `json:"text,omitempty"`
	Message    *projection.MessageInfo      `json:"message,omitempty"`
	Fetched    *FetchResult                 `json:"fetched,omitempty"`
	Ingest     *IngestResult                `json:"ingest,omitempty"`
	TopicID    string                       `json:"topic_id,omitempty"`
	Path       string                       `json:"path,omitempty"`
	MessageID2 string                       `json:"message_id,omitempty"`
	Status     map[string]any               `json:"status,omitempty"`
	Subs       []projection.SubscriptionRow `json:"subscriptions,omitempty"`
	Sub        *SubscribeResult             `json:"subscription,omitempty"`
	Derivs     []projection.DerivativeRow   `json:"derivatives,omitempty"`
	Summary    *projection.SummaryRow       `json:"summary,omitempty"`
	Staged     *StagedAttachment            `json:"staged,omitempty"` // G6: stage-attachment result
}

// StagedAttachment is the stage-attachment reply: the stored object's hash and
// metadata, referenced by a subsequent publish (G6).
type StagedAttachment struct {
	ObjectHash string `json:"object_hash"`
	ByteLen    int    `json:"byte_len"`
	Mime       string `json:"mime,omitempty"`
	Filename   string `json:"filename,omitempty"`
}

// SocketPath returns the daemon's unix socket location: short, deterministic
// per cairn (unix socket path length limits rule out Application Support).
// The FULL cairn id is required: a UUIDv7 prefix is a millisecond timestamp,
// and two meshes created in the same millisecond would collide — one
// daemon's startup would silently remove the other's live socket (the root
// cause of the TestF3 flake recorded in PROGRESS.md).
func SocketPath(cairnID string) string {
	return filepath.Join(os.TempDir(), "cairn-"+cairnID+".sock")
}

// socketPathFile records the socket location in device-local state.
func socketPathFile(deviceDir string) string {
	return filepath.Join(deviceDir, "daemon.sock.path")
}

// Serve listens on the unix socket until ctx is done.
func (d *Daemon) Serve(ctx context.Context) error {
	sock := SocketPath(d.loaded.Portable.CairnID)
	os.Remove(sock) // stale socket from a dead daemon; the flock is the true owner
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.WriteFile(socketPathFile(d.sockDir), []byte(sock), 0o600); err != nil {
		l.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
		os.Remove(sock)
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go d.handleConn(conn)
	}
}

func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := readLine(r, config.IPCMaxRequestBytes)
	if err != nil {
		writeResponse(conn, Response{Error: "bad request: " + err.Error()})
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, Response{Error: "bad request json: " + err.Error()})
		return
	}
	// G6: stage-attachment streams the raw attachment bytes on THIS connection,
	// immediately after the JSON header line — never inlined in the request.
	if req.Op == "stage-attachment" {
		writeResponse(conn, d.stageAttachment(req, r))
		return
	}
	writeResponse(conn, d.dispatch(req))
}

// stageAttachment reads exactly AttachMeta.ByteLen raw bytes off the connection
// (following the JSON header line), stores the content-addressed object, and
// returns its hash. Same capSend gate as publish; the bytes never inflate the
// JSON request, so many/large attachments cannot breach IPCMaxRequestBytes.
func (d *Daemon) stageAttachment(req Request, r *bufio.Reader) Response {
	if req.AttachMeta == nil {
		return Response{Error: "stage-attachment without attach_meta"}
	}
	n := req.AttachMeta.ByteLen
	if n <= 0 {
		return Response{Error: "stage-attachment with non-positive byte_len"}
	}
	if n > config.DeriveMaxBytes {
		return Response{Error: fmt.Sprintf("attachment is %d bytes (cap %d) — refused before ack", n, config.DeriveMaxBytes)}
	}
	// capability gate (parity with dispatch): a handle needs capSend.
	if req.Session != "" {
		sess, prof, err := d.sessions.resolve(req.Session, d.now())
		if err != nil {
			return Response{Error: "capability: " + err.Error()}
		}
		if need := capabilityFor("stage-attachment"); !prof.Allows(need) {
			return Response{Error: fmt.Sprintf("capability: profile %q does not allow %q — refused before ack", sess.Profile, need)}
		}
	}
	if err := d.writable(); err != nil {
		return Response{Error: err.Error()}
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Response{Error: fmt.Sprintf("reading %d attachment bytes: %v", n, err)}
	}
	d.mu.Lock()
	hash, err := d.store.Put(buf)
	d.mu.Unlock()
	if err != nil {
		return Response{Error: fmt.Sprintf("storing attachment: %v", err)}
	}
	mime := req.AttachMeta.Mime
	if mime == "" {
		mime = sniffMime(buf)
	}
	return Response{OK: true, Staged: &StagedAttachment{
		ObjectHash: hash, ByteLen: n, Mime: mime, Filename: req.AttachMeta.Filename,
	}}
}

func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > max {
			return nil, errors.New("request too large")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

func writeResponse(w io.Writer, resp Response) {
	blob, err := json.Marshal(resp)
	if err != nil {
		blob = []byte(`{"ok":false,"error":"response marshal failure"}`)
	}
	w.Write(append(blob, '\n'))
}

func (d *Daemon) dispatch(req Request) Response {
	fail := func(err error) Response { return Response{Error: err.Error()} }

	// --- N2 capability gate (rulings §7.2, RULINGS.md R21/R23) ------------
	// Runs BEFORE any op logic, so every refusal is structurally pre-ack.
	principal := "operator" // tier-1: local CLI without a handle
	var sess *Session
	if req.Session != "" {
		var prof *Profile
		var err error
		sess, prof, err = d.sessions.resolve(req.Session, d.now())
		if err != nil {
			return Response{Error: "capability: " + err.Error()}
		}
		principal = sess.Principal()
		if strings.HasPrefix(req.Op, "session-") {
			// R23: handles are non-delegable — a session can neither mint
			// nor revoke handles; that stays with the operator tier.
			return Response{Error: "capability: session handles are non-delegable (session ops require the operator tier)"}
		}
		if capNeeded := capabilityFor(req.Op); !prof.Allows(capNeeded) {
			return Response{Error: fmt.Sprintf(
				"capability: profile %q does not allow %q (op %q) — refused before ack", sess.Profile, capNeeded, req.Op)}
		}
		// a handle acts AS its leaf principal: the client-supplied actor is
		// overridden, and tier-1-only publish knobs are stripped
		req.Actor = sess.Name
		if req.Publish != nil {
			req.Publish.Actor = sess.Name
			req.Publish.OperatorOverride = false
			req.Publish.AutoCreateTopics = false
		}
	}

	pubReq := PublishRequest{Actor: req.Actor}

	switch req.Op {
	case "session-create":
		if req.SessionProfile == "" {
			return fail(errors.New("session_profile is required"))
		}
		name := req.SessionName
		if name == "" {
			name = req.SessionProfile
		}
		created, err := d.sessions.create(name, req.SessionProfile, principal, req.SessionPID, d.now())
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Status: map[string]any{
			"session": created.Token, "principal": created.Principal(),
			"profile": created.Profile, "expires_at": created.ExpiresAt,
		}}

	case "session-revoke":
		if err := d.sessions.revoke(req.TargetSession); err != nil {
			return fail(err)
		}
		return Response{OK: true}

	case "session-list":
		return Response{OK: true, Status: map[string]any{"sessions": d.sessions.list()}}

	case "subscribe-durable":
		if req.Subscribe == nil {
			return fail(errors.New("subscribe payload missing"))
		}
		req.Subscribe.Actor = req.Actor
		res, err := d.SubscribeDurable(*req.Subscribe)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Sub: res}

	case "subscription-update":
		if req.SubUpdate == nil {
			return fail(errors.New("sub_update payload missing"))
		}
		req.SubUpdate.Actor = req.Actor
		res, err := d.SubscriptionUpdate(*req.SubUpdate)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Sub: res}

	case "subscription-disable":
		id, err := d.SubscriptionDisable(req.SubscriptionID, req.Actor)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "subscription-delete":
		id, err := d.SubscriptionDelete(req.SubscriptionID, req.Actor)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "derivative-list":
		derivs, err := d.proj.DerivativesForMessage(req.MessageID)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Derivs: derivs}

	case "derivative-invalidate":
		id, err := d.DerivativeInvalidate(req.DerivativeID, req.Reason, req.Actor)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "summary-show":
		row, err := d.proj.SummaryForMessage(req.MessageID)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Summary: row}

	case "subscription-list":
		subs, err := d.proj.Subscriptions(req.AgentView, false)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Subs: subs}

	case "publish":
		if req.Publish == nil {
			return fail(errors.New("publish payload missing"))
		}
		res, err := d.Publish(*req.Publish)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Publish: res}

	case "retract":
		info, err := d.proj.MessageInfo(req.MessageID)
		if err != nil {
			return fail(err)
		}
		if info.Retracted {
			return fail(fmt.Errorf("message %s is already retracted", req.MessageID))
		}
		payload := map[string]any{"message_id": req.MessageID}
		if req.Reason != "" {
			payload["reason"] = req.Reason
		}
		id, err := d.SimpleEvent("message.retract", "message", req.MessageID, payload, pubReq)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "topic-create":
		id, err := d.SimpleEvent("topic.create", "topic", req.TopicID,
			map[string]any{"topic_id": req.TopicID, "name": req.TopicName}, pubReq)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "link":
		id, err := d.Link(req.MessageID, req.TopicID, req.Protected, req.Actor)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "unlink":
		id, err := d.Unlink(req.LinkID, req.Actor)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "pin":
		id, err := d.Pin(req.ObjectRef, req.Durability, req.Actor)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "unpin":
		id, err := d.Unpin(req.PinID, req.Actor)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "signal":
		id, err := d.Signal(req.MessageID, req.Kind, req.Weight, req.Actor)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "search":
		sopts := SearchOptions{
			Query: req.Query, K: req.K, BudgetChars: req.BudgetChars,
			IncludeRetracted: req.IncludeRetracted,
		}
		if req.Search2 != nil {
			sopts = *req.Search2
		}
		sopts.Principal = principal // dispatch-resolved; client value ignored
		out, err := d.Search(sopts)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Search: out}

	case "digest":
		out, err := d.Digest(DigestOptions{AgentView: req.AgentView, BudgetChars: req.BudgetChars, Principal: principal})
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Digest: out}

	case "why-ranked":
		text, err := d.WhyRanked(req.InteractionID, req.MessageID)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Text: text}

	case "revise":
		res, err := d.Revise(req.MessageID, req.Body)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Ingest: res}

	case "topic-ensure":
		id, _, err := d.TopicEnsure(req.TopicName)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, TopicID: id}

	case "source-ref":
		id, err := d.proj.SourceRefMessage(req.SourcePath)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: "", MessageID2: id}

	case "outcome":
		if err := d.Outcome(req.InteractionID, req.Outcome, req.MessageID); err != nil {
			return fail(err)
		}
		return Response{OK: true}

	case "gates":
		var b strings.Builder
		if err := d.GatesReport(&b); err != nil {
			return fail(err)
		}
		return Response{OK: true, Text: b.String()}

	case "reserve-release":
		if err := d.ReleaseReserve(); err != nil {
			return fail(err)
		}
		return Response{OK: true}

	case "reserve-status":
		present, size, granted := d.ReserveStatus()
		return Response{OK: true, Status: map[string]any{"present": present, "size": size, "release_granted": granted}}

	case "emergency-publish":
		if req.Publish == nil {
			return fail(errors.New("publish payload missing"))
		}
		res, err := d.EmergencyPublish(*req.Publish)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Publish: res}

	case "housekeep":
		deleted, err := d.Housekeep()
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Status: map[string]any{"deleted": len(deleted)}}

	case "reindex-semantic":
		n, err := d.ReindexSemantic()
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Status: map[string]any{"embedded": n}}

	case "sync-now":
		if err := d.writable(); err != nil {
			return fail(err)
		}
		d.mu.Lock()
		peers := append([]string(nil), d.loaded.Device.SyncPeers...)
		d.mu.Unlock()
		if req.Peer != "" {
			peers = []string{req.Peer}
		}
		if len(peers) == 0 {
			return fail(errors.New("no sync peers configured (set sync_peers in the device config, or pass an address)"))
		}
		// G7.1: run the sweep in the BACKGROUND and ack immediately. A large
		// catch-up (thousands of events) can exceed the IPC deadline even though
		// convergence succeeds — which surfaced as a spurious i/o timeout. Report
		// progress via the daemon log + `cairn sync status`. One sweep at a time.
		if !d.beginManualSync() {
			return Response{OK: true, Status: map[string]any{
				"status": "a manual sync is already in progress — watch `cairn sync status` or the daemon log"}}
		}
		go func() {
			defer d.endManualSync()
			total := 0
			for _, addr := range peers {
				n, err := d.SyncWith(addr)
				if err != nil {
					fmt.Fprintf(d.warn, "sync now: peer %s failed: %v\n", addr, err)
					continue
				}
				total += n
			}
			fmt.Fprintf(d.warn, "sync now: complete — %d event(s) ingested across %d peer(s)\n", total, len(peers))
		}()
		return Response{OK: true, Status: map[string]any{
			"status": fmt.Sprintf("sync started in background across %d peer(s) — watch `cairn sync status` or the daemon log for convergence", len(peers)),
			"peers":  len(peers)}}

	case "sync-status":
		fr, err := d.Frontiers()
		if err != nil {
			return fail(err)
		}
		frs := make([]map[string]any, 0, len(fr))
		for _, f := range fr {
			frs = append(frs, map[string]any{
				"origin": f.DeviceID + "/" + strconv.Itoa(f.Generation), "next_seq": f.NextSeq,
			})
		}
		d.mu.Lock()
		peers := append([]string(nil), d.loaded.Device.SyncPeers...)
		d.mu.Unlock()
		st := map[string]any{"frontiers": frs, "peers": peers, "bootstrap": d.bootstrapMode,
			"listener": d.SyncListenState()}
		if dur, derr := d.DurabilityStatus(); derr == nil && len(dur) > 0 {
			st["blobs"] = dur
		}
		return Response{OK: true, Status: st}

	case "peek":
		info, err := d.proj.MessageInfo(req.MessageID)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Message: info}

	case "fetch":
		res, err := d.Fetch(req.MessageID, req.AgentView)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Fetched: res}

	case "export":
		path, err := d.Export(req.MessageID)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Path: path}

	case "export-ingest":
		res, err := d.IngestExport(req.Path)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Ingest: res}

	case "resolve":
		res, err := d.Resolve(req.MessageID)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Ingest: res}

	case "status":
		d.mu.Lock()
		next := int64(0)
		if d.lg != nil {
			next = d.lg.NextSeq()
		}
		d.mu.Unlock()
		return Response{OK: true, Status: map[string]any{
			"cairn_id":  d.loaded.Portable.CairnID,
			"device_id": d.loaded.Device.DeviceID,
			"next_seq":  next,
		}}

	default:
		return fail(fmt.Errorf("unknown op %q", req.Op))
	}
}

// Call sends one request to a running daemon and decodes the response.
func Call(deviceDir string, req Request) (*Response, error) {
	sockBytes, err := os.ReadFile(socketPathFile(deviceDir))
	if err != nil {
		return nil, errors.New("daemon not running — start with `cairn daemon` or install with `cairn daemon --install`")
	}
	conn, err := net.DialTimeout("unix", string(sockBytes), 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("daemon not running (%v) — start with `cairn daemon` or install with `cairn daemon --install`", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	blob, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(blob, '\n')); err != nil {
		return nil, err
	}
	respLine, err := readLine(bufio.NewReaderSize(conn, 64<<10), config.IPCMaxRequestBytes)
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return &resp, errors.New(resp.Error)
	}
	return &resp, nil
}

// StageAttachment streams one attachment file to the daemon over IPC (G6): the
// JSON header line, then the raw bytes — so the bytes never inflate a JSON
// request past IPCMaxRequestBytes. It stat-checks the size client-side FIRST
// and fails cleanly (no transmission) if it exceeds the cap, instead of the old
// `broken pipe`. Returns the staged object's hash for a subsequent publish.
func StageAttachment(deviceDir, session, path string) (*StagedAttachment, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(config.DeriveMaxBytes) {
		return nil, fmt.Errorf("attachment %s is %d bytes (cap %d = 16 MiB) — not sent", filepath.Base(path), info.Size(), config.DeriveMaxBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sockBytes, err := os.ReadFile(socketPathFile(deviceDir))
	if err != nil {
		return nil, errors.New("daemon not running — start with `cairn daemon` or install with `cairn daemon --install`")
	}
	conn, err := net.DialTimeout("unix", string(sockBytes), 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("daemon not running (%v) — start with `cairn daemon` or install with `cairn daemon --install`", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	header, err := json.Marshal(Request{
		Op:      "stage-attachment",
		Session: session,
		AttachMeta: &AttachMeta{
			Filename: filepath.Base(path),
			ByteLen:  len(data),
		},
	})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(header, '\n')); err != nil {
		return nil, err
	}
	if _, err := conn.Write(data); err != nil {
		return nil, err
	}
	respLine, err := readLine(bufio.NewReaderSize(conn, 64<<10), config.IPCMaxRequestBytes)
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, errors.New(resp.Error)
	}
	return resp.Staged, nil
}
