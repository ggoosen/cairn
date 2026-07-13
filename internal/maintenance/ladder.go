// Package maintenance implements the P2 async maintenance worker's decision
// core: the degradation ladder (spec §8.2). Under enrichment debt or disk
// pressure the worker sheds work in a FIXED order — send() never blocks and a
// message is never lost; only derived enrichment lags. This package is the
// pure, deterministic policy; the daemon supplies the live debt signals and
// acts on the returned Level.
package maintenance

import "github.com/ggoosen/cairn/internal/config"

// Level is the current degradation rung (spec §8.2). Higher = more shed. Each
// rung is cumulative: at LexicalOnly, auto-links + summaries + embeddings are
// all delayed as well.
type Level int

const (
	Healthy            Level = iota // 0: all enrichment runs
	DelayAutoLinks                  // 1: delay auto-link suggestions
	DelaySummaries                  // 2: + delay receiver summaries
	DelayEmbeddings                 // 3: + delay embeddings
	LexicalOnly                     // 4: + serve lexical-only search (flagged)
	DelayBlobRepl                   // 5: + delay normal blob replication
	RejectLowPrioBlobs              // 6: + reject oversized/low-priority blobs
	RejectSmallText                 // 7: + reject small text (disk/quota only)
)

func (l Level) String() string {
	switch l {
	case Healthy:
		return "healthy"
	case DelayAutoLinks:
		return "delay-auto-links"
	case DelaySummaries:
		return "delay-summaries"
	case DelayEmbeddings:
		return "delay-embeddings"
	case LexicalOnly:
		return "lexical-only"
	case DelayBlobRepl:
		return "delay-blob-replication"
	case RejectLowPrioBlobs:
		return "reject-low-priority-blobs"
	case RejectSmallText:
		return "reject-small-text"
	default:
		return "unknown"
	}
}

// Debt is the live load signal the daemon samples each maintenance pass.
type Debt struct {
	PendingEmbeddings int  // revisions indexed lexically but not yet embedded
	DiskPressure      bool // free space below the healthy margin
	DiskCritical      bool // free space critically low (reject low-prio blobs)
	QuotaExhausted    bool // no room even for small text (except reserved hi-pri)
}

// Thresholds parameterizes Assess (defaults from config; overridable for tests
// or future config-revision).
type Thresholds struct {
	DelayLinks, DelaySummaries, DelayEmbeddings, LexicalOnly int
}

// DefaultThresholds reads the config constants (the single source of truth).
func DefaultThresholds() Thresholds {
	return Thresholds{
		DelayLinks:      config.LadderDebtDelayLinks,
		DelaySummaries:  config.LadderDebtDelaySummaries,
		DelayEmbeddings: config.LadderDebtDelayEmbeddings,
		LexicalOnly:     config.LadderDebtLexicalOnly,
	}
}

// Assess maps live debt to a degradation Level (spec §8.2). The enrichment
// backlog drives rungs 1–4; disk/quota pressure drives rungs 5–7. The result is
// the MOST degraded rung any signal demands (they are one ordered ladder). A
// reserved slice of capacity for small high-priority text always exists, so
// RejectSmallText never blocks high-priority sends — that guarantee lives in
// the caller (send path), not here; this only reports the level.
func Assess(d Debt, t Thresholds) Level {
	lvl := Healthy

	// enrichment-backlog axis (rungs 1–4)
	switch {
	case d.PendingEmbeddings >= t.LexicalOnly:
		lvl = LexicalOnly
	case d.PendingEmbeddings >= t.DelayEmbeddings:
		lvl = DelayEmbeddings
	case d.PendingEmbeddings >= t.DelaySummaries:
		lvl = DelaySummaries
	case d.PendingEmbeddings >= t.DelayLinks:
		lvl = DelayAutoLinks
	}

	// disk/quota axis (rungs 5–7) — takes precedence when more severe
	switch {
	case d.QuotaExhausted && RejectSmallText > lvl:
		lvl = RejectSmallText
	case d.DiskCritical && RejectLowPrioBlobs > lvl:
		lvl = RejectLowPrioBlobs
	case d.DiskPressure && DelayBlobRepl > lvl:
		lvl = DelayBlobRepl
	}
	return lvl
}

// Gate helpers — a rung is shed when the level is at or above it.
func (l Level) SkipAutoLinks() bool     { return l >= DelayAutoLinks }
func (l Level) SkipSummaries() bool     { return l >= DelaySummaries }
func (l Level) SkipEmbeddings() bool    { return l >= DelayEmbeddings }
func (l Level) LexicalOnlyForced() bool { return l >= LexicalOnly }
func (l Level) DelayBlobRepl() bool     { return l >= DelayBlobRepl }
func (l Level) RejectLowPrioBlob() bool { return l >= RejectLowPrioBlobs }
func (l Level) RejectSmallText() bool   { return l >= RejectSmallText }
