// Package config holds Cairn's configuration files (portable + device-local)
// and, in this file, EVERY tunable constant in the system.
//
// Rule (CLAUDE.md): no magic numbers in code. Ranking weights, half-lives,
// seal thresholds, limits, formats — they all live here, commented with the
// ruling/spec section that fixes their value. Change them here or not at all.
package config

import "time"

// ---------------------------------------------------------------------------
// Protocol identity (rulings §1, project rename)
// ---------------------------------------------------------------------------

const (
	// EventDomainSeparator prefixes signing_bytes before BLAKE3 hashing:
	// event_id = BLAKE3(EventDomainSeparator || signing_bytes). Domain-
	// separated per rulings §1 (renamed from agent-mesh-event-v1).
	EventDomainSeparator = "cairn-event-v1"

	// EventSchemaVersion is the envelope schema_version (monotonic integer).
	EventSchemaVersion = 1

	// PayloadSchemaID names the normative payload schema set
	// (build/schemas/p0-events.schema.json).
	PayloadSchemaID = "cairn/p0-events/v1"

	// WallTimeFormat: RFC 3339 UTC with FIXED microsecond precision
	// (rulings §1: timestamps are strings; no floats anywhere).
	WallTimeFormat = "2006-01-02T15:04:05.000000Z"
)

// ---------------------------------------------------------------------------
// Event log: framing and segments (rulings §3)
// ---------------------------------------------------------------------------

const (
	// FrameMagic + FrameVersion open every frame: fixed magic, then
	// u64 little-endian length, record_bytes, CRC32C (Castagnoli).
	FrameMagic   = "CRNF"
	FrameVersion = byte(1)

	// Segments seal at 64 MiB or 10,000 events, whichever first (rulings §3.7).
	SegmentSealBytes  = 64 << 20
	SegmentSealEvents = 10_000

	// FirstGeneration / FirstSequence: origin sequencing starts at 1;
	// ordering is (origin, generation, sequence) — never wall time.
	FirstGeneration = 1
	FirstSequence   = 1
)

// ---------------------------------------------------------------------------
// Bodies, text classes, object store (spec §5.4, rulings §5)
// ---------------------------------------------------------------------------

const (
	// InlineBodyLimit: bodies ≤ 64 KiB may ALSO be inlined in the event
	// (body_bytes); the content-addressed object remains authoritative.
	InlineBodyLimit = 64 << 10

	// InlineBodyCeiling: hard config ceiling for the inline limit (spec §5.4).
	InlineBodyCeiling = 256 << 10

	// AutoDowngradeBytes: bodies > 1 MiB declared canonical/eager are
	// auto-downgraded to ephemeral; only the operator CLI override forces
	// canonical/eager (rulings §5).
	AutoDowngradeBytes = 1 << 20

	// EphemeralTTL: default retention for ephemeral-text objects; expiry
	// yields a typed content_expired on fetch (spec §5.4, rulings §5).
	EphemeralTTL = 7 * 24 * time.Hour

	// Daily canonical-byte and per-message ceilings (rulings §5:
	// "configurable"). These are the defaults; overridable in portable config.
	DefaultDailyCanonicalBytes      = 64 << 20
	DefaultPerMessageCanonicalBytes = 8 << 20
)

// ---------------------------------------------------------------------------
// Emergency reserve (rulings §11)
// ---------------------------------------------------------------------------

const (
	// EmergencyReserveBytes: preallocated per cairn; ordinary sends can
	// never consume it; released only by explicit interactive operator command.
	EmergencyReserveBytes = 64 << 20

	// EmergencyReleaseMaxBytes: one release admits one small text event.
	EmergencyReleaseMaxBytes = 64 << 10
)

// ---------------------------------------------------------------------------
// Retrieval and ranking, P0 profiles (spec §9.1, rulings §7)
// ---------------------------------------------------------------------------
// NOTE: floats are forbidden in event payloads, not in local ranking math.
// why_ranked stores component values as decimal strings.

const (
	// RRF fusion over FTS top-N and vector top-N candidate lists.
	FusionCandidatesFTS    = 100
	FusionCandidatesVector = 100
	RRFK                   = 60

	// P0 search profile: 0.90·R + 0.07·F + 0.03·P, freshness half-life 90 d.
	SearchWeightR = 0.90
	SearchWeightF = 0.07
	SearchWeightP = 0.03

	// P0 digest profile: 0.60·R + 0.30·F + 0.10·P, freshness half-life 72 h.
	DigestWeightR = 0.60
	DigestWeightF = 0.30
	DigestWeightP = 0.10

	// PenaltyCap: each duplicate/thread-saturation penalty is capped at 0.15
	// (spec §9.1). P0 profiles apply no penalties; the cap is pinned here for
	// the P2 profiles so the constant has one home.
	PenaltyCap = 0.15

	// DeclaredPriorityMax: declared_priority ∈ [0,3]; P = declared/3.
	DeclaredPriorityMax = 3
)

const (
	SearchFreshnessHalfLife = 90 * 24 * time.Hour
	DigestFreshnessHalfLife = 72 * time.Hour

	// PriorityDecayHalfLife: effective_P = (declared/3)·2^(−age/60h);
	// suspended by an active pin or signal.emit(priority_confirm) (rulings §7).
	PriorityDecayHalfLife = 60 * time.Hour
)

// ---------------------------------------------------------------------------
// Embeddings and vector search (rulings §7)
// ---------------------------------------------------------------------------

const (
	// EmbeddingModelID pins the model; EmbeddingModelHash pins the exact
	// ONNX artifact (BLAKE3 hex). Vectors from different models are never
	// compared; migration = invalidate + reindex --semantic.
	EmbeddingModelID   = "all-MiniLM-L6-v2"
	EmbeddingModelHash = "" // pinned in M6 when the ONNX artifact is vendored (deliberately empty until then)

	EmbeddingDim = 384

	// BruteForceMaxCandidates: below this, plain cosine scan is the
	// acceptable fallback when sqlite-vec fails to load (rulings §7).
	BruteForceMaxCandidates = 5000
)

// ---------------------------------------------------------------------------
// Projection / FTS (rulings §6)
// ---------------------------------------------------------------------------

const (
	ProjectionSchemaVersion = 1

	// FTSTokenize: unicode61 with tokenchars `_ - # @` (rulings §6).
	FTSTokenize = "unicode61 tokenchars '_-#@'"
)

// ---------------------------------------------------------------------------
// Views (spec §7.3, rulings §8)
// ---------------------------------------------------------------------------

const (
	// QuotePrefix prefixes EVERY line of cairn content quoted into digests —
	// per-line prefixing cannot be escaped by in-line content.
	QuotePrefix = "> [CAIRN] "
)

// ---------------------------------------------------------------------------
// Engineering gates (spec §11, rulings §10) — release blockers, used by
// `cairn gates` (M7) and CI assertions.
// ---------------------------------------------------------------------------

const (
	GateLexicalVisibilityP95 = 200 * time.Millisecond // send-ack → lexical-digest-visible
	GateSuccessAt5Min        = 0.70                   // golden-corpus CI proxy (validates config, not thesis)
	GateLexicalOnlyTop10Min  = 0.60                   // lexical_only known-relevant in top-10
	GateWorkaroundRateMax    = 0.25                   // human-measured (DOGFOOD protocol)
)

// ---------------------------------------------------------------------------
// Filesystem layout and permissions
// ---------------------------------------------------------------------------

const (
	// DefaultDirName under $HOME when --dir / $CAIRN_DIR are unset.
	DefaultDirName = "cairn"

	// Portable-dir entries (build/ARCHITECTURE.md on-disk layout).
	PortableConfigName = "cairn.toml"
	EventsDirName      = "events"
	ObjectsDirName     = "objects"
	ExportsDirName     = "exports"
	ViewsDirName       = "views"
	DerivedDirName     = ".cairn" // rebuildable; excluded from backup by default

	// Device-local entries. Private keys NEVER live under the portable dir.
	DeviceConfigName = "config-device.toml"
	DeviceKeyName    = "device.key"
	DeviceCertName   = "device.cert"
	RootKeyName      = "root.key"
	SeqStateName     = "seq_state.json" // cache only; the verified log is authoritative

	KeyFilePerm  = 0o600
	FilePerm     = 0o644
	DirPerm      = 0o700
	PublicDirNew = 0o755
)

// ConfigVersion for both TOML files (monotonic; strict-checked on load).
const (
	PortableConfigVersion = 1
	DeviceConfigVersion   = 1
)
