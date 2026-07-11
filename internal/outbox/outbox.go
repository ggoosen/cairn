// Package outbox implements the filesystem ingestion contract (rulings §8):
// the canonical atomic directory bundle (<request_id>.ready/request.json)
// and the bare-.md convenience shorthand. request_id is the idempotency key
// FOREVER — reprocessing returns the original event IDs. Receipts are
// written ONLY after event durability; invalid bundles go to rejected/ with
// a structured error and never ack.
package outbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/daemon"
	"github.com/ggoosen/cairn/internal/fsx"
)

// request.json for a canonical bundle. op: publish | reply.
type bundleRequest struct {
	RequestID        string   `json:"request_id"`
	Op               string   `json:"op"`
	Body             string   `json:"body,omitempty"`
	BodyFile         string   `json:"body_file,omitempty"` // name inside the bundle
	TextClass        string   `json:"text_class,omitempty"`
	DeclaredPriority int      `json:"declared_priority,omitempty"`
	MessageID        string   `json:"message_id,omitempty"`
	ThreadID         string   `json:"thread_id,omitempty"`
	ReplyToMessageID string   `json:"reply_to_message_id,omitempty"`
	Recipients       []string `json:"recipients,omitempty"`
	TopicIDs         []string `json:"topic_ids,omitempty"`
	TaskID           string   `json:"task_id,omitempty"`
	AgentInstanceID  string   `json:"agent_instance_id,omitempty"`
}

// front-matter whitelist for the .md shorthand (rulings §8 — STRICT).
type mdFrontMatter struct {
	Action           string   `yaml:"action"`
	MessageID        string   `yaml:"message_id"`
	BaseRevisionID   string   `yaml:"base_revision_id"`
	ReplyToMessageID string   `yaml:"reply_to_message_id"`
	TextClass        string   `yaml:"text_class"`
	DeclaredPriority int      `yaml:"declared_priority"`
	TopicIDs         []string `yaml:"topic_ids"`
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{3,4}-[0-9a-f]{3,4}-[0-9a-f]{12}$`)

// Watcher processes one agent view's outbox directory.
type Watcher struct {
	fs    fsx.FS
	d     *daemon.Daemon
	dir   string // portable dir
	agent string // view name = actor principal
}

// NewWatcher creates a watcher for one agent view. The agent name is a
// daemon-generated safe id (rulings §8); path metacharacters are refused.
func NewWatcher(fsys fsx.FS, d *daemon.Daemon, portableDir, agent string) (*Watcher, error) {
	if agent == "" || strings.ContainsAny(agent, "/\\") || strings.Contains(agent, "..") {
		return nil, fmt.Errorf("invalid agent view name %q", agent)
	}
	return &Watcher{fs: fsys, d: d, dir: portableDir, agent: agent}, nil
}

func (w *Watcher) outboxDir() string {
	return filepath.Join(w.dir, config.ViewsDirName, w.agent, "outbox")
}

// ProcessOnce scans the outbox for ready bundles and .md drops, processing
// each to completion (receipt or rejected/). Idempotent and crash-restart
// safe: a bundle whose events landed but whose receipt didn't gets the
// IDENTICAL receipt regenerated on the next pass.
func (w *Watcher) ProcessOnce() error {
	dir := w.outboxDir()
	entries, err := w.fs.ReadDir(dir)
	if err != nil {
		return nil // no outbox for this view yet
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && strings.HasSuffix(name, config.OutboxReadySuffix):
			if err := w.processBundle(filepath.Join(dir, name)); err != nil {
				return err
			}
		case !e.IsDir() && strings.HasSuffix(name, ".md"):
			if err := w.processMarkdown(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// processBundle handles <request_id>.ready/ per the canonical contract.
func (w *Watcher) processBundle(bundleDir string) error {
	requestID := strings.TrimSuffix(filepath.Base(bundleDir), config.OutboxReadySuffix)
	receiptPath := filepath.Join(w.outboxDir(), requestID+config.ReceiptSuffix)

	raw, err := w.fs.ReadFile(filepath.Join(bundleDir, config.OutboxRequestFile))
	if err != nil {
		return w.reject(bundleDir, requestID, fmt.Sprintf("bundle missing %s: %v", config.OutboxRequestFile, err))
	}
	var req bundleRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return w.reject(bundleDir, requestID, "unparseable request.json: "+err.Error())
	}
	if req.RequestID != requestID {
		return w.reject(bundleDir, requestID, fmt.Sprintf("request_id %q does not match bundle name %q", req.RequestID, requestID))
	}
	if !uuidRe.MatchString(requestID) {
		return w.reject(bundleDir, requestID, "request_id must be a UUID")
	}
	if req.Op != "publish" && req.Op != "reply" {
		return w.reject(bundleDir, requestID, fmt.Sprintf("unsupported op %q (P0 outbox: publish, reply)", req.Op))
	}
	if req.Op == "reply" && req.ReplyToMessageID == "" {
		return w.reject(bundleDir, requestID, "reply requires reply_to_message_id")
	}

	body := req.Body
	if req.BodyFile != "" {
		if body != "" {
			return w.reject(bundleDir, requestID, "body and body_file are mutually exclusive")
		}
		if strings.Contains(req.BodyFile, "/") || strings.Contains(req.BodyFile, "..") {
			return w.reject(bundleDir, requestID, "body_file must be a plain name inside the bundle")
		}
		blob, err := w.fs.ReadFile(filepath.Join(bundleDir, req.BodyFile))
		if err != nil {
			return w.reject(bundleDir, requestID, "unreadable body_file: "+err.Error())
		}
		body = string(blob)
	}
	if body == "" {
		return w.reject(bundleDir, requestID, "empty body")
	}

	res, err := w.d.Publish(daemon.PublishRequest{
		Actor:            w.agent,
		TaskID:           req.TaskID,
		AgentInstanceID:  req.AgentInstanceID,
		CorrelationID:    requestID,
		Body:             body,
		TextClass:        req.TextClass,
		DeclaredPriority: req.DeclaredPriority,
		MessageID:        req.MessageID,
		ThreadID:         req.ThreadID,
		ReplyToMessageID: req.ReplyToMessageID,
		Recipients:       req.Recipients,
		TopicIDs:         req.TopicIDs,
		// OperatorOverride deliberately impossible from the outbox (rulings §5)
	})
	if err != nil {
		// validation/terminal failure BEFORE ack → rejected, no receipt
		return w.reject(bundleDir, requestID, "rejected: "+err.Error())
	}

	// === events durable; NOW (and only now) the receipt ===
	if err := w.writeReceipt(receiptPath, requestID, res); err != nil {
		return err
	}
	return w.archiveBundle(bundleDir, requestID)
}

// processMarkdown wraps a bare .md into an internal publish (convenience
// shorthand; daemon-assigned request id recorded in the receipt).
func (w *Watcher) processMarkdown(path string) error {
	name := filepath.Base(path)
	receiptPath := path + config.ReceiptSuffix // <name>.md.receipt.json

	blob, err := w.fs.ReadFile(path)
	if err != nil {
		return err
	}
	fm, body, err := splitFrontMatter(string(blob))
	if err != nil {
		return w.rejectFile(path, name, err.Error())
	}
	if body == "" {
		return w.rejectFile(path, name, "empty body")
	}
	if fm.Action != "" && fm.Action != "publish" && fm.Action != "reply" {
		return w.rejectFile(path, name, fmt.Sprintf("unsupported action %q (shorthand: publish, reply; edits go through exports)", fm.Action))
	}
	if fm.Action == "reply" && fm.ReplyToMessageID == "" {
		return w.rejectFile(path, name, "reply requires reply_to_message_id")
	}

	res, err := w.d.Publish(daemon.PublishRequest{
		Actor:            w.agent,
		Body:             body,
		TextClass:        fm.TextClass,
		DeclaredPriority: fm.DeclaredPriority,
		MessageID:        fm.MessageID,
		ReplyToMessageID: fm.ReplyToMessageID,
		TopicIDs:         fm.TopicIDs,
	})
	if err != nil {
		return w.rejectFile(path, name, "rejected: "+err.Error())
	}
	if err := w.writeReceipt(receiptPath, "", res); err != nil {
		return err
	}
	return w.fs.Remove(path) // consumed
}

// splitFrontMatter parses optional strict YAML front-matter. No front-matter
// ⇒ plain publish of the whole file.
func splitFrontMatter(content string) (mdFrontMatter, string, error) {
	var fm mdFrontMatter
	if !strings.HasPrefix(content, "---\n") {
		return fm, strings.TrimSpace(content), nil
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fm, "", errors.New("unterminated front-matter")
	}
	dec := yaml.NewDecoder(strings.NewReader(rest[:end]))
	dec.KnownFields(true) // strict whitelist (rulings §9)
	if err := dec.Decode(&fm); err != nil && !errors.Is(err, io.EOF) {
		return fm, "", fmt.Errorf("front-matter: %v", err)
	}
	body := rest[end+4:]
	return fm, strings.TrimSpace(body), nil
}

// writeReceipt: receipt bytes are a pure function of (request_id, result) —
// no timestamps — so a crash-and-retry regenerates the IDENTICAL receipt.
func (w *Watcher) writeReceipt(path, requestID string, res *daemon.PublishResult) error {
	receipt := map[string]any{
		"status":      "accepted",
		"event_ids":   res.EventIDs,
		"message_id":  res.MessageID,
		"revision_id": res.RevisionID,
		"body_hash":   res.BodyHash,
		"text_class":  res.TextClass,
	}
	if requestID != "" {
		receipt["request_id"] = requestID
	}
	if res.Downgraded {
		receipt["downgraded"] = true
		receipt["downgrade_reason"] = res.DowngradeReason
	}
	blob, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if existing, err := w.fs.ReadFile(path); err == nil {
		if bytes.Equal(existing, blob) {
			return nil // receipt already durable and identical
		}
		return fmt.Errorf("receipt %s exists with DIFFERENT content — refusing to overwrite", path)
	}
	return fsx.WriteFileAtomic(w.fs, path, blob, config.FilePerm)
}

// reject moves an invalid bundle to rejected/ with a structured error. No ack.
func (w *Watcher) reject(bundleDir, requestID, reason string) error {
	rejectedDir := filepath.Join(w.outboxDir(), config.OutboxRejectedDir, requestID)
	if err := w.fs.MkdirAll(rejectedDir, config.DirPerm); err != nil {
		return err
	}
	blob, _ := json.MarshalIndent(map[string]any{
		"request_id": requestID,
		"status":     "rejected",
		"error":      reason,
	}, "", "  ")
	w.fs.Remove(filepath.Join(rejectedDir, config.OutboxErrorFile))
	if err := fsx.WriteFileAtomic(w.fs, filepath.Join(rejectedDir, config.OutboxErrorFile), blob, config.FilePerm); err != nil {
		return err
	}
	// preserve the bundle's request.json for debugging, then drop the bundle
	if raw, err := w.fs.ReadFile(filepath.Join(bundleDir, config.OutboxRequestFile)); err == nil {
		w.fs.Remove(filepath.Join(rejectedDir, config.OutboxRequestFile))
		fsx.WriteFileAtomic(w.fs, filepath.Join(rejectedDir, config.OutboxRequestFile), raw, config.FilePerm)
	}
	return w.fs.RemoveAll(bundleDir)
}

func (w *Watcher) rejectFile(path, name, reason string) error {
	rejectedDir := filepath.Join(w.outboxDir(), config.OutboxRejectedDir, name)
	if err := w.fs.MkdirAll(rejectedDir, config.DirPerm); err != nil {
		return err
	}
	blob, _ := json.MarshalIndent(map[string]any{
		"file":   name,
		"status": "rejected",
		"error":  reason,
	}, "", "  ")
	w.fs.Remove(filepath.Join(rejectedDir, config.OutboxErrorFile))
	if err := fsx.WriteFileAtomic(w.fs, filepath.Join(rejectedDir, config.OutboxErrorFile), blob, config.FilePerm); err != nil {
		return err
	}
	return w.fs.Rename(path, filepath.Join(rejectedDir, name))
}

// archiveBundle removes a fully-processed bundle: the receipt in the outbox
// root is the durable answer, and duplicates are detected via the projection
// (correlation_id), so re-dropping the same request_id still regenerates the
// identical receipt.
func (w *Watcher) archiveBundle(bundleDir, _ string) error {
	return w.fs.RemoveAll(bundleDir)
}
