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

	Publish *PublishRequest `json:"publish,omitempty"`

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
	OK      bool                      `json:"ok"`
	Error   string                    `json:"error,omitempty"`
	Publish *PublishResult            `json:"publish,omitempty"`
	EventID string                    `json:"event_id,omitempty"`
	Results []projection.SearchResult `json:"results,omitempty"`
	Search  *SearchOutput             `json:"search,omitempty"`
	Digest  *DigestOutput             `json:"digest,omitempty"`
	Text    string                    `json:"text,omitempty"`
	Message *projection.MessageInfo   `json:"message,omitempty"`
	Fetched *FetchResult              `json:"fetched,omitempty"`
	Ingest  *IngestResult             `json:"ingest,omitempty"`
	Path    string                    `json:"path,omitempty"`
	Status  map[string]any            `json:"status,omitempty"`
}

// SocketPath returns the daemon's unix socket location: short, deterministic
// per cairn (unix socket path length limits rule out Application Support).
func SocketPath(cairnID string) string {
	return filepath.Join(os.TempDir(), "cairn-"+cairnID[:13]+".sock")
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
	if err := os.WriteFile(socketPathFile(d.loaded.DeviceDir), []byte(sock), 0o600); err != nil {
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
	writeResponse(conn, d.dispatch(req))
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
	pubReq := PublishRequest{Actor: req.Actor}

	switch req.Op {
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
		payload := map[string]any{"link_id": req.LinkID, "message_id": req.MessageID, "topic_id": req.TopicID}
		if req.Protected {
			payload["protected"] = true
		}
		id, err := d.SimpleEvent("topic.link.add", "link", req.LinkID, payload, pubReq)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "unlink":
		id, err := d.SimpleEvent("topic.link.remove", "link", req.LinkID,
			map[string]any{"removed_link_ids": []string{req.LinkID}}, pubReq)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "pin":
		actor := req.Actor
		if actor == "" {
			actor = "operator"
		}
		id, err := d.SimpleEvent("blob.pin", "pin", req.PinID,
			map[string]any{"pin_id": req.PinID, "principal_id": actor, "object_hash": req.ObjectRef, "durability": req.Durability}, pubReq)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "unpin":
		id, err := d.SimpleEvent("blob.unpin", "pin", req.PinID,
			map[string]any{"pin_ids": []string{req.PinID}}, pubReq)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "signal":
		payload := map[string]any{"message_id": req.MessageID, "kind": req.Kind}
		if req.Weight > 0 {
			payload["weight"] = req.Weight
		}
		id, err := d.SimpleEvent("signal.emit", "message", req.MessageID, payload, pubReq)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, EventID: id}

	case "search":
		out, err := d.Search(SearchOptions{
			Query: req.Query, K: req.K, BudgetChars: req.BudgetChars,
			IncludeRetracted: req.IncludeRetracted,
		})
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Search: out}

	case "digest":
		out, err := d.Digest(DigestOptions{AgentView: req.AgentView, BudgetChars: req.BudgetChars})
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

	case "reindex-semantic":
		n, err := d.ReindexSemantic()
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Status: map[string]any{"embedded": n}}

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
		return nil, errors.New("daemon is not running (no socket registration); start it with `cairn daemon`")
	}
	conn, err := net.DialTimeout("unix", string(sockBytes), 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("daemon is not reachable (%v); start it with `cairn daemon`", err)
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
