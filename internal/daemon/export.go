package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/merge"
	"github.com/ggoosen/cairn/internal/object"
	"github.com/ggoosen/cairn/internal/views"
)

// Export/ingest (rulings §8, BUILD-PLAN M5). Exports live at
// exports/<message_id>.md with READ-ONLY front-matter; conflicts materialize
// under views/operator/conflicts/<message_id>/ (the operator edits exports
// in P0). Never a blocking prompt, never a silent overwrite.

// IngestResult reports what an export ingest (or resolve) did.
type IngestResult struct {
	Outcome        string   `json:"outcome"` // unchanged | revised | merged | conflict
	EventID        string   `json:"event_id,omitempty"`
	NewRevisionIDs []string `json:"new_revision_ids,omitempty"`
	HeadRevisionID string   `json:"head_revision_id"`
	ConflictDir    string   `json:"conflict_dir,omitempty"`
	ExportPath     string   `json:"export_path,omitempty"`
}

// conflictManifest records the merge inputs at materialization time so
// `cairn resolve` replays the SAME base against the then-current head.
type conflictManifest struct {
	MessageID      string `json:"message_id"`
	BaseRevisionID string `json:"base_revision_id"`
	HeadRevisionID string `json:"head_revision_id"` // head at conflict time (informational)
}

func (d *Daemon) exportPath(messageID string) string {
	return filepath.Join(d.dir, config.ExportsDirName, messageID+".md")
}

func (d *Daemon) conflictDir(messageID string) string {
	return filepath.Join(d.dir, config.ViewsDirName, "operator", "conflicts", messageID)
}

// Export renders exports/<message_id>.md for the current head revision.
func (d *Daemon) Export(messageID string) (string, error) {
	info, err := d.proj.MessageInfo(messageID)
	if err != nil {
		return "", err
	}
	if info.Retracted {
		return "", fmt.Errorf("message %s is retracted; it is not exported", messageID)
	}
	body, err := d.store.Get(info.BodyHash)
	if err != nil {
		return "", fmt.Errorf("head body unavailable: %w", err)
	}
	content, err := views.RenderExport(views.ExportFrontMatter{
		MessageID:      info.MessageID,
		RevisionID:     info.HeadRevisionID,
		BaseRevisionID: info.HeadRevisionID,
		BodyHash:       info.BodyHash,
		ExportedAt:     d.now().UTC().Format(config.WallTimeFormat),
	}, body)
	if err != nil {
		return "", err
	}
	path := d.exportPath(messageID)
	if err := d.fs.MkdirAll(filepath.Dir(path), config.DirPerm); err != nil {
		return "", err
	}
	d.fs.Remove(path) // regenerated view
	if err := fsx.WriteFileAtomic(d.fs, path, content, config.FilePerm); err != nil {
		return "", err
	}
	return path, nil
}

// IngestExport processes an edited export file: validate the read-only
// front-matter, normalize, then base==head → normal revision; head moved +
// clean diff3 → one event with TWO revisions (operator branch + merged);
// conflict → conflicts/ materialization. Rejections write
// <export>.reject.json (the error receipt) and return the error.
func (d *Daemon) IngestExport(path string) (*IngestResult, error) {
	content, err := d.fs.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, editedBody, err := views.ParseExport(content)
	if err != nil {
		return nil, d.rejectExport(path, err)
	}
	res, err := d.ingestEdit(fm.MessageID, fm.BaseRevisionID, fm.BodyHash, editedBody, true)
	if err != nil {
		return nil, d.rejectExport(path, err)
	}
	return res, nil
}

// ingestEdit is the shared merge path for export ingest and resolve.
// verifyHash: export ingest validates fm.body_hash against the base
// revision (front-matter immutability); resolve already trusts its manifest.
func (d *Daemon) ingestEdit(messageID, baseRevisionID, claimedBodyHash string, editedBody []byte, verifyHash bool) (*IngestResult, error) {
	if err := d.writable(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lg == nil {
		return nil, errors.New("daemon is closed")
	}

	// read-only front-matter enforcement: every field must match the log
	base, err := d.proj.RevisionInfo(baseRevisionID)
	if err != nil {
		return nil, fmt.Errorf("front-matter mutation rejected: %w", err)
	}
	if base.MessageID != messageID {
		return nil, fmt.Errorf("front-matter mutation rejected: revision %s does not belong to message %s", baseRevisionID, messageID)
	}
	if verifyHash && base.BodyHash != claimedBodyHash {
		return nil, fmt.Errorf("front-matter mutation rejected: body_hash does not match revision %s", baseRevisionID)
	}

	info, err := d.proj.MessageInfo(messageID)
	if err != nil {
		return nil, err
	}
	if info.Retracted {
		return nil, fmt.Errorf("message %s is retracted; restore requires explicit operator action (message.restore is not in P0)", messageID)
	}

	edited, err := merge.NormalizeLF(editedBody)
	if err != nil {
		return nil, err
	}
	headBodyRaw, err := d.store.Get(info.BodyHash)
	if err != nil {
		return nil, fmt.Errorf("current head body unavailable: %w", err)
	}
	headBody, err := merge.NormalizeLF(headBodyRaw)
	if err != nil {
		return nil, err
	}

	// no-op edit: identical to the current head
	if string(edited) == string(headBody) {
		return &IngestResult{Outcome: "unchanged", HeadRevisionID: info.HeadRevisionID}, nil
	}

	if info.HeadRevisionID == baseRevisionID {
		// head unchanged since export → ONE normal revision
		return d.appendRevision(messageID, [][2]any{
			{edited, []string{baseRevisionID}},
		}, false)
	}

	// head moved: three-way merge against the then-current head
	baseBodyRaw, err := d.store.Get(base.BodyHash)
	if err != nil {
		return nil, fmt.Errorf("export base body unavailable: %w", err)
	}
	baseBody, err := merge.NormalizeLF(baseBodyRaw)
	if err != nil {
		return nil, err
	}
	mres, err := merge.Diff3(baseBody, headBody, edited)
	if err != nil {
		return nil, err
	}
	if mres.Clean {
		// ONE event, TWO revisions: operator branch (parent=base), then the
		// machine-merged head (parents=[operator branch, current head])
		return d.appendRevision(messageID, [][2]any{
			{edited, []string{baseRevisionID}},
			{mres.Merged, []string{"", info.HeadRevisionID}}, // "" ← filled with op-branch id
		}, true)
	}

	// conflict: materialize; never a prompt, never a silent overwrite
	dir := d.conflictDir(messageID)
	if err := d.fs.MkdirAll(dir, config.DirPerm); err != nil {
		return nil, err
	}
	writes := map[string][]byte{
		"BASE.md":          baseBody,
		"CURRENT.md":       headBody,
		"OPERATOR_EDIT.md": edited,
		"RESOLVE.md":       mres.Merged, // conflict-marked seed the operator edits
	}
	manifest, _ := json.MarshalIndent(conflictManifest{
		MessageID:      messageID,
		BaseRevisionID: baseRevisionID,
		HeadRevisionID: info.HeadRevisionID,
	}, "", "  ")
	writes["conflict.json"] = manifest
	for name, blob := range writes {
		p := filepath.Join(dir, name)
		d.fs.Remove(p)
		if err := fsx.WriteFileAtomic(d.fs, p, blob, config.FilePerm); err != nil {
			return nil, err
		}
	}
	return &IngestResult{Outcome: "conflict", HeadRevisionID: info.HeadRevisionID, ConflictDir: dir}, nil
}

// appendRevision persists body objects FIRST, then appends ONE
// message.revise_body event carrying 1 (normal) or 2 (merge) revisions —
// crash-atomic: either no revision or the complete merge graph (row 13).
// Caller holds d.mu.
func (d *Daemon) appendRevision(messageID string, bodies [][2]any, machineMerged bool) (*IngestResult, error) {
	// R42/R46 sweep: a revision inherits its message's text_class. An ephemeral
	// message's revision body must NEVER be inlined — same invariant the publish
	// path enforces — or the un-purgeable inline copy is reintroduced on the
	// revise/merge path (the N9 audit's H1 finding).
	info, err := d.proj.MessageInfo(messageID)
	if err != nil {
		return nil, err
	}
	ephemeral := info.TextClass == object.ClassEphemeral
	type rev struct {
		id      string
		body    []byte
		parents []string
		hash    string
		merged  bool
	}
	var revs []rev
	for i, pair := range bodies {
		body := pair[0].([]byte)
		parents := pair[1].([]string)
		hash, err := d.store.Put(body) // object BEFORE event (rulings §3.1)
		if err != nil {
			return nil, err
		}
		revs = append(revs, rev{
			id:      d.newUUID(),
			body:    body,
			parents: parents,
			hash:    hash,
			merged:  machineMerged && i == len(bodies)-1,
		})
	}
	// wire the merged revision's first parent to the operator branch
	if len(revs) == 2 && revs[1].parents[0] == "" {
		revs[1].parents[0] = revs[0].id
	}

	payloadRevs := make([]map[string]any, 0, len(revs))
	for _, r := range revs {
		m := map[string]any{
			"revision_id":         r.id,
			"parent_revision_ids": r.parents,
			"body_hash":           r.hash,
			"body_len":            len(r.body),
		}
		if len(r.body) <= config.InlineBodyLimit && !ephemeral {
			m["body_bytes"] = string(r.body)
		}
		if r.merged {
			m["machine_merged"] = true
		}
		payloadRevs = append(payloadRevs, m)
	}
	payload := map[string]any{"message_id": messageID, "revisions": payloadRevs}
	env, rec, err := d.buildEvent("message.revise_body", "message", messageID, payload, PublishRequest{Actor: "operator"})
	if err != nil {
		return nil, err
	}
	// R42/R46 pre-ack guard (F1: reject before acknowledgement, never after):
	// structural enforcement of the ephemeral-no-inline invariant on the
	// revise/merge path — a defense-in-depth twin of ValidateNoInlineEphemeral.
	if err := ValidateNoInlineEphemeralRevisions(env, info.TextClass); err != nil {
		return nil, err
	}
	if err := d.lg.Append(rec, env); err != nil {
		return nil, err
	}
	d.applyProjection(env, rec)

	res := &IngestResult{
		EventID:        env.EventID,
		HeadRevisionID: revs[len(revs)-1].id,
	}
	for _, r := range revs {
		res.NewRevisionIDs = append(res.NewRevisionIDs, r.id)
	}
	if len(revs) == 2 {
		res.Outcome = "merged"
	} else {
		res.Outcome = "revised"
	}
	return res, nil
}

// Link adds a topic link with pre-ack referential validation (FIX-F1):
// message and topic must resolve (topic by id or name); unknown → reject
// before anything is appended.
func (d *Daemon) Link(messageID, topicRef string, protected bool, actor string) (string, error) {
	if _, err := d.proj.MessageInfo(messageID); err != nil {
		return "", fmt.Errorf("rejected before ack: %w", err)
	}
	d.mu.Lock()
	topicID, _, err := d.resolveTopic(topicRef)
	d.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("rejected before ack: %w (create it with `cairn topic create` or use `cairn send --topic`)", err)
	}
	payload := map[string]any{"link_id": d.newUUID(), "message_id": messageID, "topic_id": topicID}
	if protected {
		payload["protected"] = true
	}
	return d.SimpleEvent("topic.link.add", "link", payload["link_id"].(string), payload, PublishRequest{Actor: actor})
}

// Unlink removes an observed link assertion; the assertion must exist.
func (d *Daemon) Unlink(linkID, actor string) (string, error) {
	active, err := d.proj.LinkActive(linkID)
	if err != nil {
		return "", err
	}
	if !active {
		return "", fmt.Errorf("rejected before ack: no active link %s", linkID)
	}
	return d.SimpleEvent("topic.link.remove", "link", linkID,
		map[string]any{"removed_link_ids": []string{linkID}}, PublishRequest{Actor: actor})
}

// Signal validates the target message before ack.
func (d *Daemon) Signal(messageID, kind string, weight int, actor string) (string, error) {
	if _, err := d.proj.MessageInfo(messageID); err != nil {
		return "", fmt.Errorf("rejected before ack: %w", err)
	}
	payload := map[string]any{"message_id": messageID, "kind": kind}
	if weight > 0 {
		payload["weight"] = weight
	}
	return d.SimpleEvent("signal.emit", "message", messageID, payload, PublishRequest{Actor: actor})
}

// Pin validates the object exists before ack.
func (d *Daemon) Pin(objectHash, durability, actor string) (string, error) {
	if !object.ValidHash(objectHash) {
		return "", fmt.Errorf("rejected before ack: %w: %q", object.ErrBadHash, objectHash)
	}
	if !d.store.Exists(objectHash) {
		return "", fmt.Errorf("rejected before ack: object %s not found in the store", objectHash)
	}
	pinID := d.newUUID()
	return d.SimpleEvent("blob.pin", "pin", pinID,
		map[string]any{"pin_id": pinID, "principal_id": actorOr(actor), "object_hash": objectHash, "durability": durability},
		PublishRequest{Actor: actor})
}

// Unpin validates the pin exists and is active.
func (d *Daemon) Unpin(pinID, actor string) (string, error) {
	active, err := d.proj.PinActiveByID(pinID)
	if err != nil {
		return "", err
	}
	if !active {
		return "", fmt.Errorf("rejected before ack: no active pin %s", pinID)
	}
	return d.SimpleEvent("blob.unpin", "pin", pinID,
		map[string]any{"pin_ids": []string{pinID}}, PublishRequest{Actor: actor})
}

func actorOr(a string) string {
	if a == "" {
		return "operator"
	}
	return a
}

// SimpleEventUnvalidatedForTest bypasses pre-ack referential validation —
// ONLY for injecting historical poison events in the parking regression
// tests (the F1 defense-in-depth path).
func (d *Daemon) SimpleEventUnvalidatedForTest(eventType, objectType, objectID string, payload map[string]any) (string, error) {
	return d.SimpleEvent(eventType, objectType, objectID, payload, PublishRequest{Actor: "test"})
}

// Revise appends one normal revision with the CURRENT head as base (the
// M9 ingest update path — base==head by construction, so no merge arises).
func (d *Daemon) Revise(messageID, body string) (*IngestResult, error) {
	info, err := d.proj.MessageInfo(messageID)
	if err != nil {
		return nil, err
	}
	return d.ingestEdit(messageID, info.HeadRevisionID, "", []byte(body), false)
}

// TopicEnsure returns the topic id for a name, creating the topic if absent
// (idempotent — repeated ingests must not fail on UNIQUE names).
func (d *Daemon) TopicEnsure(name string) (topicID string, created bool, err error) {
	if err := d.writable(); err != nil {
		return "", false, err
	}
	// R53: gate the untrusted name at the write boundary, before ack — even
	// though an existing conforming topic short-circuits below, a non-conforming
	// name must never reach SimpleEvent.
	if err := validateTopicName(name); err != nil {
		return "", false, err
	}
	if id, err := d.proj.TopicIDByName(name); err != nil {
		return "", false, err
	} else if id != "" {
		return id, false, nil
	}
	id := d.newUUID()
	if _, err := d.SimpleEvent("topic.create", "topic", id,
		map[string]any{"topic_id": id, "name": name}, PublishRequest{Actor: "operator"}); err != nil {
		return "", false, err
	}
	return id, true, nil
}

// Resolve applies conflicts/<id>/RESOLVE.md against the then-current head
// (rulings §8). The resolution event carries TWO revisions: the operator
// branch (OPERATOR_EDIT.md, parent = the original export base) and the
// resolved head (RESOLVE.md, parents = [operator branch, current head]) —
// a human merge, so machine_merged stays false. If the head moved again
// since materialization, the resolution is diff3-merged against the new
// head; a fresh conflict refreshes the materialization instead.
func (d *Daemon) Resolve(messageID string) (*IngestResult, error) {
	dir := d.conflictDir(messageID)
	blob, err := d.fs.ReadFile(filepath.Join(dir, "conflict.json"))
	if err != nil {
		return nil, fmt.Errorf("no unresolved conflict for %s: %w", messageID, err)
	}
	var manifest conflictManifest
	if err := json.Unmarshal(blob, &manifest); err != nil {
		return nil, err
	}
	resolveRaw, err := d.fs.ReadFile(filepath.Join(dir, "RESOLVE.md"))
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(resolveRaw), "<<<<<<<") {
		return nil, fmt.Errorf("RESOLVE.md still contains conflict markers — finish editing it first")
	}
	opEditRaw, err := d.fs.ReadFile(filepath.Join(dir, "OPERATOR_EDIT.md"))
	if err != nil {
		return nil, err
	}

	res, err := d.applyResolution(messageID, &manifest, resolveRaw, opEditRaw, dir)
	if err != nil {
		return nil, err
	}
	if res.Outcome == "conflict" {
		return res, nil // refreshed materialization; RESOLVE.md re-seeded
	}
	if err := d.fs.RemoveAll(dir); err != nil {
		return nil, err
	}
	if _, err := d.Export(messageID); err == nil {
		res.ExportPath = d.exportPath(messageID)
	}
	return res, nil
}

func (d *Daemon) applyResolution(messageID string, manifest *conflictManifest, resolveRaw, opEditRaw []byte, dir string) (*IngestResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lg == nil {
		return nil, errors.New("daemon is closed")
	}
	info, err := d.proj.MessageInfo(messageID)
	if err != nil {
		return nil, err
	}
	if info.Retracted {
		return nil, fmt.Errorf("message %s is retracted; restore requires explicit operator action", messageID)
	}
	resolveBody, err := merge.NormalizeLF(resolveRaw)
	if err != nil {
		return nil, err
	}
	opEdit, err := merge.NormalizeLF(opEditRaw)
	if err != nil {
		return nil, err
	}

	mergedBody := resolveBody
	if info.HeadRevisionID != manifest.HeadRevisionID {
		// head moved during resolution: fold the newer head changes in,
		// relative to the head the operator resolved against
		conflictHeadRev, err := d.proj.RevisionInfo(manifest.HeadRevisionID)
		if err != nil {
			return nil, err
		}
		conflictHeadRaw, err := d.store.Get(conflictHeadRev.BodyHash)
		if err != nil {
			return nil, fmt.Errorf("conflict-time head body unavailable: %w", err)
		}
		conflictHead, err := merge.NormalizeLF(conflictHeadRaw)
		if err != nil {
			return nil, err
		}
		newHeadRaw, err := d.store.Get(info.BodyHash)
		if err != nil {
			return nil, fmt.Errorf("current head body unavailable: %w", err)
		}
		newHead, err := merge.NormalizeLF(newHeadRaw)
		if err != nil {
			return nil, err
		}
		mres, err := merge.Diff3(conflictHead, newHead, resolveBody)
		if err != nil {
			return nil, err
		}
		if !mres.Clean {
			// refresh the materialization against the new head
			manifest.HeadRevisionID = info.HeadRevisionID
			mblob, _ := json.MarshalIndent(manifest, "", "  ")
			for name, blob := range map[string][]byte{
				"CURRENT.md":    newHead,
				"RESOLVE.md":    mres.Merged,
				"conflict.json": mblob,
			} {
				p := filepath.Join(dir, name)
				d.fs.Remove(p)
				if err := fsx.WriteFileAtomic(d.fs, p, blob, config.FilePerm); err != nil {
					return nil, err
				}
			}
			return &IngestResult{Outcome: "conflict", HeadRevisionID: info.HeadRevisionID, ConflictDir: dir}, nil
		}
		mergedBody = mres.Merged
	}

	// human resolution: operator branch + resolved head (NOT machine_merged)
	return d.appendRevision(messageID, [][2]any{
		{opEdit, []string{manifest.BaseRevisionID}},
		{mergedBody, []string{"", info.HeadRevisionID}},
	}, false)
}

// rejectExport writes the error receipt beside the export (structured; no
// ack) and returns the error.
func (d *Daemon) rejectExport(path string, cause error) error {
	blob, _ := json.MarshalIndent(map[string]any{
		"status": "rejected",
		"file":   filepath.Base(path),
		"error":  cause.Error(),
	}, "", "  ")
	rejectPath := path + ".reject.json"
	d.fs.Remove(rejectPath)
	_ = fsx.WriteFileAtomic(d.fs, rejectPath, blob, config.FilePerm)
	return cause
}

// UnresolvedConflicts lists conflict directories (doctor conflicts).
func UnresolvedConflicts(fsys fsx.FS, portableDir string) ([]string, error) {
	var out []string
	viewsDir := filepath.Join(portableDir, config.ViewsDirName)
	agents, err := fsys.ReadDir(viewsDir)
	if err != nil {
		return nil, nil // no views yet
	}
	for _, a := range agents {
		if !a.IsDir() {
			continue
		}
		cdir := filepath.Join(viewsDir, a.Name(), "conflicts")
		entries, err := fsys.ReadDir(cdir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, filepath.Join(cdir, e.Name()))
			}
		}
	}
	return out, nil
}
