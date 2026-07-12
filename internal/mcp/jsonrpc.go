package mcp

// The stdio transport: newline-delimited JSON-RPC 2.0 per the MCP spec.
// Only this file knows about framing; the tool layer (mcp.go) is
// transport-agnostic for the later HTTP mount.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ggoosen/cairn/internal/config"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// protocolVersion is what we advertise when the client's requested version
// is absent; otherwise we echo the client's (we depend on nothing newer
// than the 2024-11-05 baseline: tools only).
const protocolVersion = "2025-06-18"

// textBlock is one MCP tool-result content block.
type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []textBlock `json:"content"`
	IsError bool        `json:"isError,omitempty"`
}

// HandleMessage processes one JSON-RPC message. A nil return means no
// response is due (notification or unparseable id-less garbage).
func (s *Server) HandleMessage(line []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return nil // cannot even address a reply
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return nil // notification (e.g. notifications/initialized)
	}
	reply := func(result any) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	}
	fail := func(code int, msg string) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: msg}}
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := p.ProtocolVersion
		if v == "" {
			v = protocolVersion
		}
		return reply(map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "cairn", "version": s.version},
		})

	case "ping":
		return reply(map[string]any{})

	case "tools/list":
		return reply(map[string]any{"tools": s.Tools()})

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return fail(codeInvalidParams, "tools/call requires params.name")
		}
		result, err := s.CallTool(p.Name, p.Arguments)
		if err != nil {
			// tool-level failures surface as isError content (MCP semantics);
			// only marshalling of our own reply is a protocol error
			return reply(toolResult{IsError: true, Content: []textBlock{{Type: "text", Text: err.Error()}}})
		}
		blob, err := json.Marshal(result)
		if err != nil {
			return fail(codeInternal, fmt.Sprintf("result marshal: %v", err))
		}
		return reply(toolResult{Content: []textBlock{{Type: "text", Text: string(blob)}}})

	default:
		return fail(codeMethodNotFound, fmt.Sprintf("method %q not found", req.Method))
	}
}

// ServeStdio reads newline-delimited JSON-RPC messages from r and writes
// responses to w until EOF. Diagnostics belong on stderr (the caller's
// concern) — w carries protocol messages ONLY.
func (s *Server) ServeStdio(r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readMessageLine(br, config.MCPMaxLineBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if resp := s.HandleMessage(line); resp != nil {
			blob, err := json.Marshal(resp)
			if err != nil {
				return err
			}
			if _, err := w.Write(append(blob, '\n')); err != nil {
				return err
			}
		}
	}
}

func readMessageLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				return buf, nil
			}
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > max {
			return nil, errors.New("message too large")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}
