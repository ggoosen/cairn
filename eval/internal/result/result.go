// Package result records what a harness run did, in a structured, versioned
// form that survives the run.
//
// TWO RULES SHAPE THIS PACKAGE.
//
// First, it records OBSERVATIONS, never verdicts. There is no metric field
// anywhere below: no nDCG, no MRR, no Success@k, no pass/fail. Per-item
// outcomes (what was asked, what came back, what was known to be relevant)
// are data; turning them into a score is E4's job, and E4 does not start on
// a claim until that claim's kill criterion carries an operator signoff in
// eval/claims.yaml. Keeping the recorder verdict-free means the apparatus
// cannot accidentally publish a result before its criteria were fixed.
//
// Second, a run must be re-inspectable years later by someone who does not
// trust us (EVAL-PLAN §5-E8). So: an explicit schema version, the exact
// binary and corpus checksums, the seed, raw backend output, and a Kind that
// says plainly whether the run was a plumbing check or a real measurement.
package result

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ggoosen/cairn/eval/internal/tunables"
)

// Kind separates apparatus checks from evidence. A plumbing run proves the
// harness works; it says nothing about Cairn and must never be quoted as if
// it did. The field exists so that a stray result file cannot be mistaken
// for evaluation output.
type Kind string

const (
	KindPlumbing    Kind = "plumbing-verification"
	KindMeasurement Kind = "measurement"
)

// Run is one recorded harness execution.
type Run struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	Kind          Kind   `json:"kind"`
	Label         string `json:"label,omitempty"`

	StartedAt  string `json:"started_at"`  // RFC 3339 UTC
	FinishedAt string `json:"finished_at"` // RFC 3339 UTC

	Seed int64 `json:"seed"`

	Backend Backend `json:"backend"`
	Corpus  Corpus  `json:"corpus"`
	Env     Env     `json:"env"`

	Outcomes []Outcome `json:"outcomes"`

	// Notes carry anything a later reader needs in order not to
	// misinterpret the run: known deviations, skipped conditions, manual
	// interventions.
	Notes []string `json:"notes,omitempty"`
}

// Backend identifies the memory condition and the modelling choices it
// embodies. Notes matters: a baseline is a MODEL of what people do, and the
// model is an assumption that belongs beside the numbers it produced.
type Backend struct {
	ID    string `json:"id"`
	Notes string `json:"notes,omitempty"`
	// CairnVersion and CairnBuildTags are set for the Cairn backend only.
	CairnVersion   string `json:"cairn_version,omitempty"`
	CairnBuildTags string `json:"cairn_build_tags,omitempty"`
	// RetrievalMode records whether Cairn ran full hybrid retrieval or
	// degraded to lexical_only. Without it, an embedder-less run and a
	// semantic run are indistinguishable in the record — and they answer
	// different claims.
	RetrievalMode string `json:"retrieval_mode,omitempty"`
}

// Corpus identifies the evaluated material by content, not by name. A result
// whose corpus cannot be pinned to a checksum is not reproducible.
type Corpus struct {
	ID         string `json:"id"`
	Version    string `json:"version,omitempty"`
	Checksum   string `json:"checksum,omitempty"`
	ItemCount  int    `json:"item_count"`
	QueryCount int    `json:"query_count"`
	// LabelSource states WHERE the relevance judgments came from. E3 exists
	// because author-made judgments are not evidence; this field is how a
	// reader checks that in one glance.
	LabelSource string `json:"label_source,omitempty"`
}

// Env captures the machine, because latency claims are machine-specific and
// "proven on one machine" should be visible in the artifact.
type Env struct {
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	NumCPU    int    `json:"num_cpu"`
	GoVersion string `json:"go_version"`
	Harness   string `json:"harness_commit,omitempty"`
}

// Outcome is one retrieval, recorded without judgment.
type Outcome struct {
	QueryID string `json:"query_id"`
	Query   string `json:"query,omitempty"`
	Surface string `json:"surface"`

	// Returned is the ordered list of corpus item ids the backend returned.
	// Empty strings mark hits the backend could not map back to the corpus.
	Returned []string `json:"returned"`
	// ReturnedNative keeps the backend's own identifiers alongside, so a
	// result can be traced into the system that produced it.
	ReturnedNative []string `json:"returned_native,omitempty"`
	// Relevant is the ground truth as the CORPUS declares it, copied in so
	// the record is self-contained. Whether a returned item is relevant is
	// therefore checkable from this file alone — but the checking is not
	// done here.
	Relevant []string `json:"relevant,omitempty"`

	PayloadChars  int    `json:"payload_chars"`
	BudgetChars   int    `json:"budget_chars,omitempty"`
	ElapsedMS     int64  `json:"elapsed_ms"`
	Partial       bool   `json:"partial,omitempty"`
	PartialReason string `json:"partial_reason,omitempty"`
	InteractionID string `json:"interaction_id,omitempty"`
	Error         string `json:"error,omitempty"`

	// Raw is the backend's unparsed output. Verbose on purpose: E8 publishes
	// these files and a third party re-reading them will want the fields we
	// did not think to parse.
	Raw string `json:"raw,omitempty"`
}

// NewRun starts a record. RunID is derived from the run's inputs rather than
// randomly, so re-running the same configuration produces a traceable, not
// merely unique, identifier.
func NewRun(kind Kind, seed int64, b Backend, c Corpus) *Run {
	started := time.Now().UTC()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%s",
		kind, seed, b.ID, c.ID, c.Checksum, started.Format(time.RFC3339Nano))))
	return &Run{
		SchemaVersion: tunables.ResultSchemaVersion,
		RunID:         hex.EncodeToString(sum[:8]),
		Kind:          kind,
		StartedAt:     started.Format(time.RFC3339),
		Seed:          seed,
		Backend:       b,
		Corpus:        c,
		Env: Env{
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
			NumCPU: runtime.NumCPU(), GoVersion: runtime.Version(),
		},
	}
}

// Add appends one outcome.
func (r *Run) Add(o Outcome) { r.Outcomes = append(r.Outcomes, o) }

// Note appends a free-text caveat.
func (r *Run) Note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// Finish stamps the end time.
func (r *Run) Finish() { r.FinishedAt = time.Now().UTC().Format(time.RFC3339) }

// WriteFile writes the run as indented JSON, atomically (temp file, rename)
// so a killed process cannot leave a half-written record that later reads as
// a complete one.
func (r *Run) WriteFile(path string) error {
	if r.FinishedAt == "" {
		r.Finish()
	}
	blob, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadFile loads a recorded run and refuses a schema it does not understand,
// rather than silently misreading fields that moved.
func ReadFile(path string) (*Run, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(blob, &r); err != nil {
		return nil, err
	}
	if r.SchemaVersion != tunables.ResultSchemaVersion {
		return nil, fmt.Errorf("result %s has schema version %d, this harness reads %d",
			path, r.SchemaVersion, tunables.ResultSchemaVersion)
	}
	return &r, nil
}
