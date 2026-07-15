package daemon

// N4 — deterministic derivatives (spec §8.3) and the receiver summary
// topical-consistency check (spec §8.4). Both run on the background
// enricher thread: agents never wait (spec §8.1); the work queues are
// reconstructed from projection state, never separately persisted.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/derive"
	"github.com/ggoosen/cairn/internal/embed"
)

// sniffMime maps the sniffed content class to a MIME string (content is
// authoritative; sender-supplied MIME is a hint only — spec §8.3).
func sniffMime(data []byte) string {
	switch derive.Sniff(data) {
	case "pdf":
		return "application/pdf"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "html":
		return "text/html"
	case "text":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// DeriveOnce processes up to limit attachments lacking derivatives:
// sandboxed extraction → text object → derivative.publish (or .fail) event.
// Every outcome is recorded, so the queue drains monotonically.
// extractDerivative runs the deterministic (N4) extractor first; if the content
// has no deterministic extractor (image/audio) and heavy derivatives are opted
// in (P2-7), it runs the sandboxed heavy pipeline (OCR/metadata/…). The derived
// text is still content-addressed and tied to the source hash by the caller.
func (d *Daemon) extractDerivative(data []byte) (*derive.Result, error) {
	res, err := derive.Extract(context.Background(), data)
	if err == nil {
		return res, nil
	}
	d.mu.Lock()
	heavy := d.heavyDerive
	d.mu.Unlock()
	if heavy && errors.Is(err, derive.ErrUnsupported) {
		if hres, herr := derive.ExtractHeavy(context.Background(), data); herr == nil {
			return hres, nil
		} else if !errors.Is(herr, derive.ErrUnsupported) {
			return nil, herr
		}
	}
	return nil, err
}

// SetHeavyDerivativesForTest toggles the P2-7 opt-in heavy-derivative pipeline.
func (d *Daemon) SetHeavyDerivativesForTest(on bool) {
	d.mu.Lock()
	d.heavyDerive = on
	d.mu.Unlock()
}

func (d *Daemon) DeriveOnce(limit int) (int, error) {
	if d.readOnly {
		return 0, nil
	}
	blobs, err := d.proj.AttachmentsNeedingDerivative(limit)
	if err != nil || len(blobs) == 0 {
		return 0, err
	}
	done := 0
	for _, b := range blobs {
		// R50: an ephemeral attachment blob that is absent locally was never
		// delivered here (ephemeral is origin+live-recipients only) — its
		// absence is by design, not an extraction failure. Skip quietly; no
		// derivative.fail event, and NEVER a remote fetch.
		if b.Durability == "ephemeral" && !d.store.Exists(b.ObjectHash) {
			continue
		}
		derivID := d.newUUID()
		now := d.now().UTC().Format(config.WallTimeFormat)
		data, gerr := d.store.Get(b.ObjectHash)

		var payload map[string]any
		eventType := "derivative.publish"
		if gerr != nil {
			eventType = "derivative.fail"
			payload = map[string]any{
				"derivative_id": derivID, "blob_hash": b.ObjectHash,
				"derivative_type": "unknown", "extractor": "cairn-derive",
				"extractor_version": derive.ExtractorVersion,
				"error":             fmt.Sprintf("blob unavailable: %v", gerr),
				"generated_at":      now,
			}
		} else if res, xerr := d.extractDerivative(data); xerr != nil {
			eventType = "derivative.fail"
			payload = map[string]any{
				"derivative_id": derivID, "blob_hash": b.ObjectHash,
				"derivative_type": derive.Sniff(data), "extractor": "cairn-derive",
				"extractor_version": derive.ExtractorVersion,
				"error":             xerr.Error(),
				"generated_at":      now,
			}
		} else {
			textHash, perr := d.store.Put([]byte(res.Text))
			if perr != nil {
				return done, perr
			}
			payload = map[string]any{
				"derivative_id": derivID, "blob_hash": b.ObjectHash,
				"derivative_type": res.DerivativeType, "text_hash": textHash,
				"extractor": res.Extractor, "extractor_version": res.Version,
				"generated_at": now,
			}
		}
		if _, err := d.SimpleEvent(eventType, "derivative", derivID, payload, PublishRequest{Actor: "daemon"}); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// DerivativeInvalidate marks one derivative invalid (operator op; e.g. a
// bad extraction or an extractor bump). Pre-ack validated.
func (d *Daemon) DerivativeInvalidate(id, reason, actor string) (string, error) {
	if _, err := d.proj.DerivativeByID(id); err != nil {
		return "", fmt.Errorf("rejected before ack: %w", err)
	}
	payload := map[string]any{"derivative_id": id}
	if reason != "" {
		payload["reason"] = reason
	}
	return d.SimpleEvent("derivative.invalidate", "derivative", id, payload, PublishRequest{Actor: actor})
}

// extractiveLead is the receiver's LOCAL summary (spec §8.4: extractive,
// never abstractive in P1): the leading sentences up to the cap.
func extractiveLead(body string, maxChars int) string {
	body = strings.TrimSpace(body)
	if utf8.RuneCountInString(body) <= maxChars {
		return body
	}
	runes := []rune(body)
	cut := maxChars
	for i := maxChars; i > maxChars/2; i-- {
		if runes[i-1] == '.' || runes[i-1] == '\n' {
			cut = i
			break
		}
	}
	return strings.TrimSpace(string(runes[:cut]))
}

// SummaryCheckOnce verifies up to limit pending sender summaries: embed the
// sender's claim vs the body; below the agreement floor the receiver marks
// DISAGREEMENT and prefers its own extractive summary (with provenance).
// No embedder → checks stay pending (degradation ladder §8.2 delays
// receiver summaries, never drops them).
func (d *Daemon) SummaryCheckOnce(limit int) (int, error) {
	if d.readOnly || d.embedder == nil {
		return 0, nil
	}
	ids, err := d.proj.SummariesNeedingCheck(limit)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	done := 0
	for _, id := range ids {
		row, err := d.proj.SummaryForMessage(id)
		if err != nil || row == nil {
			return done, err
		}
		info, err := d.proj.MessageInfo(id)
		if err != nil {
			return done, err
		}
		now := d.now().UTC().Format(config.WallTimeFormat)
		body, gerr := d.store.Get(info.BodyHash)
		if gerr != nil {
			// body expired before the check ran: record the miss, don't loop
			if err := d.proj.RecordSummaryCheck(id, 0, false, "", "body-unavailable", now); err != nil {
				return done, err
			}
			done++
			continue
		}
		vecs, err := d.embedder.Embed([]string{row.SenderSummary, string(body)})
		if err != nil {
			return done, nil // embedder outage: stay pending, retry next tick
		}
		cos := embed.Cosine(vecs[0], vecs[1])
		disagree := cos < config.SummaryAgreeCosineMin
		local, method := "", ""
		if disagree {
			local = extractiveLead(string(body), config.SummaryExtractLen)
			method = "extractive-lead-v1/" + d.embedder.ModelID()
		}
		if err := d.proj.RecordSummaryCheck(id, int(cos*10000), disagree, local, method, now); err != nil {
			return done, err
		}
		if disagree {
			fmt.Fprintf(d.warn, "SUMMARY DISPUTED: message %s sender summary diverges from body (cosine %.2f < %.2f); local extractive summary recorded\n",
				id, cos, config.SummaryAgreeCosineMin)
		}
		done++
	}
	return done, nil
}
