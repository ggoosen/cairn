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
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/projection"
	"github.com/ggoosen/cairn/internal/telemetry"
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
	// D3 (spec §7.2): optional positive resource selectors for the handle
	// being minted. Operator-tier only, like every other session-* parameter.
	SessionSelectors *Selectors `json:"session_selectors,omitempty"`

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

	// P2-4 saved searches
	SavedName string `json:"saved_name,omitempty"`

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

	// R25 session (local) tier — view.json only, never events (subscribe-local)
	LocalSub *LocalSubRequest `json:"local_sub,omitempty"`

	// reads
	Query        string `json:"query,omitempty"`
	K            int    `json:"k,omitempty"`
	BudgetChars  int    `json:"budget_chars,omitempty"`
	BudgetTokens int    `json:"budget_tokens,omitempty"` // D4: exactly one of the two
	// BudgetCeilingChars (D4 × D3) is the session's max_budget_chars cap as a
	// SECOND hard limit. `json:"-"`: the capability gate sets it, never a client
	// — a client-settable ceiling would be a client-settable capability.
	BudgetCeilingChars int    `json:"-"`
	IncludeRetracted   bool   `json:"include_retracted,omitempty"`
	AgentView          string `json:"agent_view,omitempty"` // fetch/digest view
	InteractionID      string `json:"interaction_id,omitempty"`
	ThreadID           string `json:"thread_id,omitempty"` // RETR-D4 thread expansion
}

// Response is the IPC reply.
type Response struct {
	OK           bool                         `json:"ok"`
	Error        string                       `json:"error,omitempty"`
	Publish      *PublishResult               `json:"publish,omitempty"`
	EventID      string                       `json:"event_id,omitempty"`
	Results      []projection.SearchResult    `json:"results,omitempty"`
	Search       *SearchOutput                `json:"search,omitempty"`
	Digest       *DigestOutput                `json:"digest,omitempty"`
	Text         string                       `json:"text,omitempty"`
	Message      *projection.MessageInfo      `json:"message,omitempty"`
	Fetched      *FetchResult                 `json:"fetched,omitempty"`
	Ingest       *IngestResult                `json:"ingest,omitempty"`
	TopicID      string                       `json:"topic_id,omitempty"`
	Path         string                       `json:"path,omitempty"`
	MessageID2   string                       `json:"message_id,omitempty"`
	Status       map[string]any               `json:"status,omitempty"`
	Subs         []projection.SubscriptionRow `json:"subscriptions,omitempty"`
	Sub          *SubscribeResult             `json:"subscription,omitempty"`
	LocalSub     *LocalSubscription           `json:"local_subscription,omitempty"` // R25 session tier
	Onboarding   *OnboardingResult            `json:"onboarding,omitempty"`         // R56 onboarding record
	Derivs       []projection.DerivativeRow   `json:"derivatives,omitempty"`
	Summary      *projection.SummaryRow       `json:"summary,omitempty"`
	Staged       *StagedAttachment            `json:"staged,omitempty"`       // G6: stage-attachment result
	Saved        []SavedSearch                `json:"saved,omitempty"`        // P2-4 saved-search list
	Peers        []string                     `json:"peers,omitempty"`        // SYNC-C1 peer management
	Thread       *ThreadOutput                `json:"thread,omitempty"`       // RETR-D4 thread expansion
	Topics       []projection.TopicInfo       `json:"topics,omitempty"`       // RETR-D5 topic list
	Interactions []telemetry.InteractionRow   `json:"interactions,omitempty"` // P4-G6 query log

	// D3 capability selectors. Refused is a TYPED refusal ("you may not ask"),
	// which an agent must be able to tell from an empty result ("nothing
	// matched"); Capability reports what the selectors did to a request that
	// was allowed — a clamp or a scope applied silently is a lie by omission.
	Refused    *Refusal          `json:"refused,omitempty"`
	Capability *CapabilityNotice `json:"capability,omitempty"`
}

// StagedAttachment is the stage-attachment reply: the stored object's hash and
// metadata, referenced by a subsequent publish (G6).
type StagedAttachment struct {
	ObjectHash string `json:"object_hash"`
	ByteLen    int    `json:"byte_len"`
	Mime       string `json:"mime,omitempty"`
	Filename   string `json:"filename,omitempty"`
}

// SocketDir returns the per-user 0700 directory holding cairn sockets.
// FIX-A7: the socket used to sit directly in os.TempDir() with default
// permissions — on multi-user Linux any local user could connect and,
// with Session:"" granting the operator tier, act as the operator. A
// 0700 parent directory is the enforceable guarantee (chmod on the
// socket itself is not honored everywhere). XDG_RUNTIME_DIR is preferred
// when set (Linux: /run/user/<uid>, already private and short); the
// TempDir fallback keeps the path under the ~104-byte unix-socket cap on
// macOS, where TempDir is per-user private anyway.
func SocketDir() string {
	if rd := os.Getenv("XDG_RUNTIME_DIR"); rd != "" {
		return filepath.Join(rd, "cairn")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("cairn-%d", os.Getuid()))
}

// SocketPath returns the daemon's unix socket location: short, deterministic
// per cairn (unix socket path length limits rule out Application Support).
// The FULL cairn id is required: a UUIDv7 prefix is a millisecond timestamp,
// and two meshes created in the same millisecond would collide — one
// daemon's startup would silently remove the other's live socket (the root
// cause of the TestF3 flake recorded in PROGRESS.md).
func SocketPath(cairnID string) string {
	return filepath.Join(SocketDir(), cairnID+".sock")
}

// prepareSocketDir creates the socket dir and refuses to serve into one
// that is a symlink, owned by another user, or group/other-accessible —
// MkdirAll follows a pre-planted symlink silently, which would let another
// local user relocate (and access) the socket.
func prepareSocketDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating socket dir: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("socket dir %s is a symlink — refusing to serve into it", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("socket dir %s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("socket dir %s is owned by uid %d, not %d — refusing to serve into it", dir, st.Uid, os.Getuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("socket dir %s is group/other-accessible and chmod failed: %w", dir, err)
		}
	}
	return nil
}

// socketPathFile records the socket location in device-local state.
func socketPathFile(deviceDir string) string {
	return filepath.Join(deviceDir, "daemon.sock.path")
}

// Serve listens on the unix socket until ctx is done.
func (d *Daemon) Serve(ctx context.Context) error {
	if err := prepareSocketDir(SocketDir()); err != nil {
		return err
	}
	sock := SocketPath(d.loaded.Portable.CairnID)
	os.Remove(sock) // stale socket from a dead daemon; the flock is the true owner
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	os.Chmod(sock, 0o600) // belt and braces; the 0700 dir is the guarantee
	// Publish the socket path ATOMICALLY. os.WriteFile creates-and-truncates,
	// so a client that stats the file and reads it while the daemon is still
	// starting gets an EMPTY path and fails with the misleading "daemon not
	// running (dial unix: missing address)". Rename is atomic on POSIX, so a
	// reader sees either the previous path or the new one, never a partial.
	// (fsx.WriteFileAtomic can't be reused: it is refuse-if-exists by design,
	// for immutable objects — this pointer file is rewritten every start.)
	pathFile := socketPathFile(d.sockDir)
	tmpPathFile := pathFile + ".tmp"
	if err := os.WriteFile(tmpPathFile, []byte(sock), 0o600); err != nil {
		l.Close()
		return err
	}
	if err := os.Rename(tmpPathFile, pathFile); err != nil {
		os.Remove(tmpPathFile)
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
	// A panic in one request must never take down the single-writer daemon
	// for the whole mesh: recover, log the stack, answer with a typed error.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(d.warn, "PANIC recovered in IPC handler: %v\n%s", r, debug.Stack())
			_ = writeResponse(conn, Response{Error: "internal error (panic recovered; see daemon log)"})
		}
	}()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := readLine(r, config.IPCMaxRequestBytes)
	if err != nil {
		_ = writeResponse(conn, Response{Error: "bad request: " + err.Error()})
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeResponse(conn, Response{Error: "bad request json: " + err.Error()})
		return
	}
	// G6: stage-attachment streams the raw attachment bytes on THIS connection,
	// immediately after the JSON header line — never inlined in the request.
	var werr error
	if req.Op == "stage-attachment" {
		werr = writeResponse(conn, d.stageAttachment(req, r))
	} else {
		werr = writeResponse(conn, d.dispatch(req))
	}
	if werr != nil {
		fmt.Fprintf(d.warn, "IPC response write failed (client gone?): op=%s: %v\n", req.Op, werr)
	}
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

// writeResponse is best-effort: the client may already have hung up, so the
// write error is returned for optional logging, never acted on — the
// request's own durability is settled before any response is written.
func writeResponse(w io.Writer, resp Response) error {
	blob, err := json.Marshal(resp)
	if err != nil {
		blob = []byte(`{"ok":false,"error":"response marshal failure"}`)
	}
	_, werr := w.Write(append(blob, '\n'))
	return werr
}

// DispatchHookForTest, when non-nil, runs before every dispatch. Tests use
// it to inject a panic and exercise handleConn's recover path. Never set in
// production.
var DispatchHookForTest func(req Request)

// reqContext is what the capability gate resolved for one request: who is
// acting, and (D3) what their selectors confine them to. Handlers read it;
// they never read the session table themselves.
type reqContext struct {
	principal string
	confine   *Confinement      // nil = unconfined
	notice    *CapabilityNotice // nil = the selectors changed nothing worth saying
}

func (d *Daemon) dispatch(req Request) Response {
	if DispatchHookForTest != nil {
		DispatchHookForTest(req)
	}
	ctx, refusal := d.capabilityGate(&req)
	if refusal != nil {
		return *refusal
	}
	resp := d.dispatchOp(req, ctx)
	// D3: a confined session sees its confinement on every answer, so a scoped
	// result is never mistaken for the whole mesh.
	if resp.Capability == nil && ctx.notice != nil &&
		(ctx.notice.BudgetClamped || ctx.notice.BudgetCeilingChars > 0 || len(ctx.notice.TopicGrant) > 0) {
		resp.Capability = ctx.notice
	}
	return resp
}

// capabilityGate runs the N2 action-tier check (rulings §7.2, R21/R23) and the
// D3 selector check, in that order, BEFORE any op logic — so every refusal is
// structurally pre-ack. A non-nil Response is a refusal.
func (d *Daemon) capabilityGate(req *Request) (reqContext, *Response) {
	ctx := reqContext{principal: "operator"} // tier-1: local CLI without a handle
	if req.Session == "" {
		return ctx, nil
	}
	sess, prof, err := d.sessions.resolve(req.Session, d.now())
	if err != nil {
		return ctx, &Response{Error: "capability: " + err.Error()}
	}
	ctx.principal = sess.Principal()
	if strings.HasPrefix(req.Op, "session-") {
		// R23: handles are non-delegable — a session can neither mint
		// nor revoke handles; that stays with the operator tier.
		return ctx, &Response{Error: "capability: session handles are non-delegable (session ops require the operator tier)"}
	}
	if capNeeded := capabilityFor(req.Op); !prof.Allows(capNeeded) {
		return ctx, &Response{Error: fmt.Sprintf(
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
	// D3 resource selectors (spec §7.2). The SINGLE enforcement point, one step
	// after the action tier: refuse, clamp, and resolve the topic scope here so
	// no handler ever has to consult the session.
	confine, cerr := d.resolveConfinement(sess)
	if cerr != nil {
		return ctx, &Response{Error: cerr.Error()}
	}
	refusal, notice, aerr := d.applyConfinement(req, confine)
	if aerr != nil {
		return ctx, &Response{Error: aerr.Error()}
	}
	ctx.confine, ctx.notice = confine, notice
	return ctx, refusal
}

func (d *Daemon) dispatchOp(req Request, ctx reqContext) Response {
	fail := func(err error) Response { return Response{Error: err.Error()} }
	principal := ctx.principal

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
		var sel Selectors
		if req.SessionSelectors != nil {
			sel = *req.SessionSelectors
		}
		created, err := d.sessions.create(name, req.SessionProfile, principal, req.SessionPID, sel, d.now())
		if err != nil {
			return fail(err)
		}
		st := map[string]any{
			"session": created.Token, "principal": created.Principal(),
			"profile": created.Profile, "expires_at": created.ExpiresAt,
		}
		if len(sel.Topics) > 0 {
			st["topic_grant"] = sel.Topics
		}
		if sel.MaxBudgetChars > 0 {
			st["max_budget_chars"] = sel.MaxBudgetChars
		}
		return Response{OK: true, Status: st}

	case "session-revoke":
		if err := d.sessions.revoke(req.TargetSession); err != nil {
			return fail(err)
		}
		return Response{OK: true}

	case "session-list":
		return Response{OK: true, Status: map[string]any{"sessions": d.sessions.list(d.now())}}

	case "session-prune":
		// D9: force the sweep and compaction now, and say what went — the
		// operator's way to clear a backlog minted by an older daemon.
		removed, remaining, err := d.sessions.prune(d.now())
		if err != nil {
			return fail(err)
		}
		total := 0
		byReason := map[string]any{}
		for reason, n := range removed {
			total += n
			byReason[reason] = n
		}
		return Response{OK: true, Status: map[string]any{
			"removed": total, "by_reason": byReason, "remaining": remaining}}

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

	// R25 session tier: subscribe-local writes THIS view's view.json only. It
	// appends NO event and NEVER reaches SubscribeDurable — capRead, own view.
	case "subscribe-local":
		if req.LocalSub == nil {
			return fail(errors.New("local_sub payload missing"))
		}
		res, err := d.SubscribeLocal(req.LocalSub.View, req.LocalSub.InterestQuery, req.LocalSub.Topics)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, LocalSub: res}

	case "subscription-local-get":
		res, err := d.LocalSubscriptionFor(req.AgentView)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, LocalSub: res}

	// R56: locate + verify the onboarding record for a view. Read-only; the
	// daemon never applies (that stays client-side, bounded and observable).
	case "onboarding-get":
		res, err := d.OnboardingRecord(req.AgentView)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Onboarding: res}

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
		// R53: validate the untrusted name at the write boundary, before ack.
		if err := validateTopicName(req.TopicName); err != nil {
			return fail(err)
		}
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
			Query: req.Query, K: req.K,
			BudgetChars: req.BudgetChars, BudgetTokens: req.BudgetTokens,
			IncludeRetracted: req.IncludeRetracted,
		}
		if req.Search2 != nil {
			sopts = *req.Search2
		}
		sopts.Principal = principal                       // dispatch-resolved; client value ignored
		sopts.Confine = ctx.confine.Scope()               // D3; nil when unconfined
		sopts.BudgetCeilingChars = req.BudgetCeilingChars // D4 × D3; gate-set only
		out, err := d.Search(sopts)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Search: out}

	case "digest":
		out, err := d.Digest(DigestOptions{AgentView: req.AgentView,
			BudgetChars: req.BudgetChars, BudgetTokens: req.BudgetTokens,
			BudgetCeilingChars: req.BudgetCeilingChars,
			Principal:          principal, Confine: ctx.confine.Scope()})
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

	case "peer-add":
		peers, err := d.PeerAdd(req.Peer)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Peers: peers}

	case "peer-remove":
		peers, err := d.PeerRemove(req.Peer)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Peers: peers}

	case "peer-list":
		return Response{OK: true, Peers: d.PeerList()}

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
			"listener": d.SyncListenState(), "transport": d.transportName, "role": d.myRole()}
		if dur, derr := d.DurabilityStatus(); derr == nil && len(dur) > 0 {
			st["blobs"] = dur
		}
		// D2: a frontier regression is a mesh-integrity fact, so it rides the
		// same status payload `cairn net` already reads.
		if alarms := d.livenessAlarmStatus(); len(alarms) > 0 {
			st["liveness_alarms"] = alarms
		}
		return Response{OK: true, Status: st}

	case "peek":
		info, err := d.proj.MessageInfo(req.MessageID)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Message: info}

	case "thread":
		// RETR-D4 + D3: a thread crosses topics BY CONSTRUCTION, so it is the
		// surface most likely to leak past a topic selector. Out-of-scope
		// messages are dropped inside Thread and counted; a thread with NOTHING
		// in scope is a typed refusal, never an empty rendering.
		out, err := d.Thread(ThreadOptions{
			ThreadID:    req.ThreadID,
			BudgetChars: req.BudgetChars, BudgetTokens: req.BudgetTokens,
			BudgetCeilingChars: req.BudgetCeilingChars,
			Principal:          principal, Confine: ctx.confine.Scope()})
		if err != nil {
			return fail(err)
		}
		if ctx.confine.ConfinesTopics() && out.Included == 0 && out.Withheld > 0 {
			return ctx.confine.refuse("thread",
				fmt.Sprintf("thread %s lies entirely outside this session's grant (%d message(s) withheld)", req.ThreadID, out.Withheld))
		}
		resp := Response{OK: true, Thread: out}
		if out.Withheld > 0 && ctx.notice != nil {
			n := *ctx.notice
			n.Withheld = out.Withheld
			n.Note = fmt.Sprintf("%d message(s) of this thread lie outside the grant and were withheld", out.Withheld)
			resp.Capability = &n
		}
		return resp

	case "topic-list":
		topics, err := d.proj.TopicList()
		if err != nil {
			return fail(err)
		}
		if ctx.confine.ConfinesTopics() {
			kept := topics[:0]
			for _, t := range topics {
				if ctx.confine.MatchesTopic(t.Name) {
					kept = append(kept, t)
				}
			}
			topics = kept
		}
		return Response{OK: true, Topics: topics}

	case "interaction-list":
		if d.tel == nil {
			return fail(errors.New("telemetry unavailable"))
		}
		rows, err := d.tel.Interactions(req.K)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Interactions: rows}

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

	case "export-corpus":
		// D5: bulk export of every live message as Markdown, the form
		// `cairn ingest scan` consumes. Operator tier (see capabilityFor's
		// admin default and opConfinement's refusal): it renders the WHOLE
		// mesh to disk.
		root, man, err := d.ExportCorpus()
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Path: root, Status: map[string]any{
			"root": root, "messages": man.Messages, "cairn_id": man.CairnID,
			"skipped": len(man.Skipped), "repo_label": CorpusRepoLabel(man.CairnID),
		}}

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
		peers := append([]string(nil), d.loaded.Device.SyncPeers...)
		d.mu.Unlock()
		st := map[string]any{
			"cairn_id":    d.loaded.Portable.CairnID,
			"device_id":   d.loaded.Device.DeviceID,
			"next_seq":    next,
			"degradation": d.DegradationLevel().String(), // P2-1 (spec §8.2)
			"version":     d.version,                     // FIX-H7: RUNNING daemon's build version
			"pid":         os.Getpid(),                   // DEPLOY-E4: lets `cairn daemon --stop` signal a hand-run daemon
		}
		// P2H3: one-shot operator health view — membership, peers, sync listener,
		// and projection health (parked events) so `cairn status` answers "is this
		// daemon healthy?" without a doctor run. Best-effort: a query failure omits
		// the field rather than failing the whole status.
		st["members"] = d.memberCount()
		st["peers"] = len(peers)
		st["sync_listener"] = d.SyncListenState()
		// DEPLOY-E2: report the LIVE ranking profile and embedder so an
		// operator reading rank-stats knows which arithmetic they see —
		// the P2 opt-in used to be invisible (env-only, unreported).
		d.mu.Lock()
		rp := "p0"
		if d.rankP2 {
			rp = "p2"
		}
		hd := d.heavyDerive
		d.mu.Unlock()
		st["rank_profile"] = rp
		st["heavy_derivatives"] = hd
		if e := d.emb(); e != nil {
			st["embedder"] = e.ModelID()
		} else {
			st["embedder"] = "none (lexical_only)"
		}
		// D10: lexical-only has three unrelated causes with three different
		// remedies (provision the venv / wait out the backlog / fix a broken
		// embedder) and used to look identical from here.
		rs := d.RetrievalStatus()
		st["retrieval"] = rs.Mode
		if rs.Cause != "" {
			st["lexical_only_cause"] = rs.Cause
			st["lexical_only_detail"] = rs.Detail
		}
		if pending, err := d.proj.CountPendingEmbeddings(); err == nil {
			st["pending_embeddings"] = pending
		}
		if parked, err := d.proj.ParkedEvents(); err == nil {
			terminal, retryable := 0, 0
			for _, pe := range parked {
				if pe.Retryable {
					retryable++
				} else {
					terminal++
				}
			}
			st["parked_terminal"] = terminal
			st["parked_retryable"] = retryable
		}
		return Response{OK: true, Status: st}

	case "map":
		res, err := d.GenerateMap(req.AgentView)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Status: map[string]any{
			"path": res.Path, "total_messages": res.TotalMessages,
			"total_topics": res.TotalTopics, "pinned_objects": res.PinnedObjects}}

	case "compact":
		res, err := d.GenerateCompaction(req.AgentView)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Status: map[string]any{
			"path": res.Path, "events": res.Events,
			"live_entities": res.LiveEntities, "ratio": res.Ratio}}

	case "rank-stats":
		stats, err := d.RankStats()
		if err != nil {
			return fail(err)
		}
		st := map[string]any{"terms": stats}
		if req.Query == "calibrate" { // --calibrate flag reuses Query
			rec, n, cerr := d.Calibrate(d.rankProfileSearch(), 0.05, 4)
			st["episodes"] = n
			if cerr != nil {
				st["calibration_note"] = cerr.Error()
			}
			if rec != nil {
				st["calibration"] = map[string]any{
					"baseline":         rec.Baseline,
					"baseline_holdout": rec.BaselineHoldout,
					"best":             rec.Best,
					"best_train":       rec.BestTrain,
					"best_holdout":     rec.BestHoldout,
					"improves":         rec.Improves,
					"grid_size":        rec.GridSize,
					"note":             "RECOMMENDATION ONLY — weights are not changed; edit internal/config/constants.go if you adopt this after review",
				}
			}
		}
		return Response{OK: true, Status: st}

	case "saved-add":
		if err := d.SavedAdd(req.SavedName, req.Query); err != nil {
			return fail(err)
		}
		return Response{OK: true}

	case "saved-list":
		list, err := d.SavedList()
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Saved: list}

	case "saved-remove":
		if err := d.SavedRemove(req.SavedName); err != nil {
			return fail(err)
		}
		return Response{OK: true}

	case "saved-run":
		out, err := d.SavedRun(req.SavedName, SearchOptions{
			K: req.K, BudgetChars: req.BudgetChars, BudgetTokens: req.BudgetTokens,
			BudgetCeilingChars: req.BudgetCeilingChars,
			IncludeRetracted:   req.IncludeRetracted,
			TaskID:             req.Actor, Principal: principal, Confine: ctx.confine.Scope(),
		})
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Search: out}

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
