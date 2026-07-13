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

	// MaxRecordBytes bounds a single record_bytes for frame-reading sanity:
	// a torn/corrupt length field must produce a frame error, never an
	// attempted multi-GiB allocation. Generous headroom above the largest
	// legitimate event (inline bodies cap at 64 KiB + envelope + payload).
	MaxRecordBytes = 16 << 20

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

	// EphemeralTTLDefault: default retention for ephemeral-text objects;
	// expiry yields a typed content_expired on fetch (spec §5.4, rulings §5).
	// Tunable via portable config ephemeral_ttl_hours (RULINGS.md R10).
	EphemeralTTLDefault = 7 * 24 * time.Hour

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
	ProjectionSchemaVersion = 5 // v5: attachment durability class (N7); v4: derivatives+summaries (N4); v3: subscriptions (N3); v2: parked_events

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

// ---------------------------------------------------------------------------
// Daemon (rulings §6, §8)
// ---------------------------------------------------------------------------

const (
	// DaemonLockName: exclusive flock in device-local state — OS file lock +
	// IPC ownership, never PID files alone (rulings §6).
	DaemonLockName = "daemon.lock"

	// OutboxPollInterval: how often the watcher scans views/*/outbox for
	// renamed-in bundles and .md drops.
	OutboxPollInterval = 250 * time.Millisecond

	// HousekeepIntervalDefault: ephemeral-TTL housekeeping cadence (basic
	// in-process loop; the P2 maintenance subsystem is out of P0 scope).
	// Tunable via portable config housekeep_minutes (RULINGS.md R10).
	HousekeepIntervalDefault = 1 * time.Hour

	// IPCMaxRequestBytes bounds one unix-socket JSON request (bodies are
	// bounded well below this by MaxRecordBytes).
	IPCMaxRequestBytes = 32 << 20

	// MCP retrieval budget defaults (RULINGS.md R19): budgets over the
	// COMPLETE retrieval payload, accounted identically to the CLI. Tunable
	// per call via tool arguments; these are the no-argument defaults.
	MCPDigestBudgetDefault = 1500
	MCPSearchBudgetDefault = 2000

	// MCPMaxLineBytes bounds one stdio JSON-RPC message (N1). Matches the
	// IPC bound: the MCP layer never carries more than one IPC payload.
	MCPMaxLineBytes = 32 << 20

	// Durable semantic subscriptions (N3, RULINGS.md R24): calibration is
	// RELATIVE — top-N-per-window or percentile over observed similarity,
	// with a margin over the next-best candidate at the inclusion boundary.
	// No static cosine thresholds, ever. All buildpack-judgment values,
	// revisable from dogfood data.
	SubTopNDefault        = 10 // top_n mode: matches per window
	SubWindowHoursDefault = 24
	SubPercentileDefault  = 90   // percentile mode: include above this pool percentile
	SubPushCapDefault     = 20   // deliveries per day per subscription
	SubMarginMin          = 0.15 // required gap over the observed noise floor (q25)

	// Deterministic derivatives (N4, spec §8.3): sandbox limits. All
	// extraction is in-process, pure over the input bytes (no network by
	// construction), panic-contained, and bounded by these caps.
	DeriveMaxBytes     = 16 << 20 // input blob cap (matches the IPC attach path)
	DeriveMaxTextBytes = 1 << 20  // extracted-text cap
	DeriveMaxPages     = 200      // PDF page cap
	DeriveTimeout      = 10 * time.Second

	// Receiver summary topical-consistency check (N4, spec §8.4): the
	// sender summary is an untrusted claim; below this cosine vs the body
	// the receiver marks disagreement and prefers its LOCAL extractive
	// summary. Local-check constant (config-revisable) — subscriptions'
	// no-static-threshold rule (R24) is about match calibration, not this.
	SummaryAgreeCosineMin = 0.5

	// SummaryExtractLen bounds the local extractive summary (lead text).
	SummaryExtractLen = 400

	// N5 transport + membership (R27/R28).
	EnrollRequestTTL = 1 * time.Hour // R28: enrolment requests expire
	SyncHelloTimeout = 10 * time.Second
	// SyncDefaultPort — R44: the port the auto-detected tailnet listener binds
	// (<tailnet-ip>:9700). The listener NEVER binds 0.0.0.0.
	SyncDefaultPort = 9700
	// SyncListenAuto / SyncListenOff are the two sentinel sync_listen values
	// (R44): "auto" (also the empty/unset default) detects a tailnet interface
	// and binds it; "off" disables the listener deliberately. Any other value
	// is a literal tailnet host:port to pin.
	SyncListenAuto = "auto"
	SyncListenOff  = "off"
	// SyncHelloDomain domain-separates handshake signatures from event
	// signatures (never sign raw peer-supplied bytes).
	SyncHelloDomain = "cairn-sync-hello-v1"

	// N6 reconciliation + text replication (R29/R30). All config-revisable.
	// SyncProtocolVersion tags the reconciliation wire protocol (post-handshake).
	SyncProtocolVersion = 1
	// SyncAntiEntropyInterval — R29 anti-entropy sweep cadence (default 5 min).
	// A just-appended event ALSO kicks an immediate sweep (push-on-append).
	SyncAntiEntropyInterval = 5 * time.Minute
	// SyncPushDebounce collapses a burst of appends into one push sweep.
	SyncPushDebounce = 250 * time.Millisecond
	// SyncRangeBatch caps records per range/push message (memory bound).
	SyncRangeBatch = 512
	// SyncBulkCatchupThreshold — R30: a peer more than this many events behind
	// on one origin is caught up by streaming whole sealed segments rather
	// than per-event ranges (logged as a bulk catch-up).
	SyncBulkCatchupThreshold = 10000
	// SyncReconcileTimeout bounds one full reconciliation session.
	SyncReconcileTimeout = 60 * time.Second
	// SyncMaxObjectBytes caps a single replicated body/blob transfer (frame
	// sanity bound; mirrors the 16 MiB record cap).
	SyncMaxObjectBytes = 16 << 20

	// N7 blob durability (spec §6.3; buildpack N7). Class → replica target:
	// ephemeral = origin only (1); normal = at least this many nodes;
	// important / pinned = all non-revoked member nodes (computed at runtime).
	DurabilityDefault   = "normal"
	DurabilityNormalMin = 2                 // "normal ≥ 2 nodes (default)"
	DurabilityRegistry  = "durability.json" // under .cairn/ (derived, cache-class)

	// N8 fork detection (spec §6.4; R33). Equivocation = same origin+gen+seq,
	// different event_id, both validly signed. Detected when logs meet.
	ForksDirName      = "forks"      // under .cairn/: one <device>_<gen>.json per fork
	QuarantineDirName = "quarantine" // under .cairn/: preserved divergent branch frames
	// ForkProbeWindow bounds how far back the divergence probe compares peer
	// vs local event ids when a frontier fork is detected (0 = whole log).
	// P1 logs are small; the full compare is affordable and exact.
	ForkProbeWindow = 0

	// Capability sessions (N2, RULINGS.md R23): opaque daemon-side handles,
	// short-TTL, non-delegable, auto-revoked on exit/idle. TTL and idle
	// window are buildpack-judgment constants, revisable from dogfood data.
	SessionTTLDefault  = 24 * time.Hour
	SessionIdleTimeout = 6 * time.Hour
	SessionTokenBytes  = 32
	SessionsFileName   = "sessions.json" // device-local (cache-class)
	ProfilesFileName   = "profiles.toml" // device-local capability profiles
	SessionEnvVar      = "CAIRN_SESSION" // set by `cairn run` for the child

	// EnrichInterval/EnrichBatch pace the background embedding enricher
	// (rulings §6: async; a just-sent message is briefly lexical_only).
	EnrichInterval = 2 * time.Second
	EnrichBatch    = 64

	// P2-1 degradation ladder (spec §8.2). The maintenance worker sheds
	// enrichment rungs in order as the embedding backlog ("debt") grows —
	// send() NEVER blocks, enrichment merely lags. Thresholds are the pending-
	// embedding counts at which each rung engages (tunable from dogfood data).
	// Rung order: 1 delay auto-links → 2 delay summaries → 3 delay embeddings →
	// 4 serve lexical-only (flagged). Rungs 5–7 (blob/disk/quota) are a separate
	// axis keyed off disk pressure, not the backlog.
	LadderDebtDelayLinks      = 200   // rung 1
	LadderDebtDelaySummaries  = 1000  // rung 2
	LadderDebtDelayEmbeddings = 5000  // rung 3
	LadderDebtLexicalOnly = 20000 // rung 4

	// Disk-pressure margins (rungs 5–7). Free space below each engages the
	// corresponding rung: pressure → delay blob replication; critical → reject
	// oversized/low-priority blobs; quota → reject small low-priority text (the
	// emergency reserve for small HIGH-priority text is honored separately).
	LadderDiskPressureFreeBytes = 2 << 30   // 2 GiB
	LadderDiskCriticalFreeBytes = 512 << 20 // 512 MiB
	LadderDiskQuotaFreeBytes    = 64 << 20  // 64 MiB

	// OutboxReadySuffix marks an atomically-renamed-in bundle directory;
	// receipts and rejections use the fixed names below.
	OutboxReadySuffix  = ".ready"
	OutboxRejectedDir  = "rejected"
	OutboxProcessedDir = "processed"
	OutboxRequestFile  = "request.json"
	OutboxErrorFile    = "error.json"
	ReceiptSuffix      = ".receipt.json"
)
