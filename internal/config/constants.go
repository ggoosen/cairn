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

	// P2 full additive profiles (spec §9.1):
	//   search = 0.75·R + 0.08·S + 0.04·F + 0.10·I + 0.03·N
	//   digest = 0.45·R + 0.15·S + 0.20·F + 0.15·I + 0.05·N
	// S=salience (§9.2), I=operator intent, N=novelty. No linear P term — in P2
	// declared priority is a CAP on contribution, not an additive feature.
	SearchP2WeightR = 0.75
	SearchP2WeightS = 0.08
	SearchP2WeightF = 0.04
	SearchP2WeightI = 0.10
	SearchP2WeightN = 0.03

	DigestP2WeightR = 0.45
	DigestP2WeightS = 0.15
	DigestP2WeightF = 0.20
	DigestP2WeightI = 0.15
	DigestP2WeightN = 0.05

	// NoveltyExposureHalf: novelty = 2^(−impressions/half) — a rarely-shown
	// item reads as novel; heavy prior exposure decays it toward 0. Backs the
	// §9.2 "10% exploration quota for new items" intent locally.
	NoveltyExposureHalf = 8.0

	// DeclaredPriorityMax: declared_priority ∈ [0,3]; P = declared/3.
	DeclaredPriorityMax = 3
)

// ---------------------------------------------------------------------------
// P2 salience inputs (spec §9.2) — LOCAL, telemetry-derived. Raw impressions
// never leave the node; only bounded salience feeds ranking.
// ---------------------------------------------------------------------------

const (
	// Demand posterior: fetch_rate = (fetches+α)/(impressions+α+β), α=1, β=4
	// (Beta prior mean 0.20). Below SalienceMinImpressions qualified
	// impressions no NEGATIVE judgment is made — demand floors at the prior.
	SalienceDemandAlpha    = 1.0
	SalienceDemandBeta     = 4.0
	SaliencePriorMean      = 0.20 // = α/(α+β)
	SalienceMinImpressions = 5

	// ExplorationQuotaFraction (spec §9.2, "10% exploration quota for new items"):
	// under the P2 profile, this fraction of a search's K result slots is RESERVED
	// for new items — those below SalienceMinImpressions qualified impressions —
	// so cold-start content gets a fair shot at visibility (and accrues the
	// impressions that let demand be judged) instead of being buried by items that
	// have already accumulated salience. Slots go to new items only when new items
	// exist that scoring would otherwise have cut; unused exploration slots
	// backfill with the next-best by merit, so the quota never wastes budget.
	ExplorationQuotaFraction = 0.10

	// Reference-graph + operator-signal saturation half-constants: the input
	// count at which the saturating term 1−2^(−count/k) reaches 0.5.
	SalienceRefHalf    = 3.0 // reply/citation in-degree
	SalienceSignalHalf = 5.0 // summed operator-signal weight

	// Bounded blend of the three inputs into S ∈ [0,1] (weights sum to 1).
	SalienceWeightDemand = 0.50
	SalienceWeightRef    = 0.30
	SalienceWeightSignal = 0.20

	// Operator-signal anti-gaming (spec §9.2, FIX-H4). Signals are the cheapest
	// channel to forge, so their contribution is deduped, decayed, trust-weighted,
	// and per-principal capped BEFORE it saturates into S — additive with slow
	// decay, never a multiplier lock.
	//
	// SignalDecayHalfLifeHours: a signal's weight halves every N hours of age, so
	// a stale "important" flag fades rather than pinning an item forever.
	SignalDecayHalfLifeHours = float64(30 * 24) // 30 days
	// SignalMaxWeight caps one signal's declared weight (an emitter cannot buy
	// unbounded influence with a single high-weight signal).
	SignalMaxWeight = 3.0
	// SignalAgentTrust discounts a non-operator (agent) principal's signals; the
	// operator's signals carry full trust (1.0). Revisable from dogfood.
	SignalOperatorPrincipal = "operator"
	SignalOperatorTrust     = 1.0
	SignalAgentTrust        = 0.3
	// SignalPrincipalDailyCap bounds ONE principal's total effective signal
	// contribution within a UTC day (summed across every item it signals), so a
	// flood across many items cannot dominate either.
	SignalPrincipalDailyCap = 5.0
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
	// EmbeddingModelID pins the model. Vectors from different models are
	// never compared; migration = invalidate + reindex --semantic.
	//
	// EmbeddingModelHash is a VESTIGE of the ONNX path that never shipped:
	// it was to hold the BLAKE3 of a vendored ONNX artifact. The sanctioned
	// fallback (a sentence-transformers subprocess in an operator-provisioned
	// venv) shipped instead, and artifact pinning with it — but per-venv,
	// as <venv>/model.hash written at provision time and re-verified before
	// each worker start (internal/embed/modelpin.go, P4-G2). Nothing reads
	// this constant. Kept, empty, only so the name is not silently reused.
	EmbeddingModelID   = "all-MiniLM-L6-v2"
	EmbeddingModelHash = ""

	EmbeddingDim = 384

	// BruteForceMaxCandidates: below this, plain cosine scan is the
	// acceptable fallback when sqlite-vec fails to load (rulings §7).
	BruteForceMaxCandidates = 5000
)

// Embed-worker watchdogs. The subprocess protocol has no deadline of its
// own, so a wedged worker would block its caller (and, holding the worker
// mutex, every later embed) forever. On timeout the worker is killed and
// the caller degrades to lexical_only.
const (
	// EmbedHandshakeTimeout covers the first request only: it absorbs the
	// model download/deserialization that sentence-transformers does lazily.
	EmbedHandshakeTimeout = 120 * time.Second
	// EmbedRequestTimeout bounds every post-handshake batch (the model is
	// resident by then; seconds, not minutes).
	EmbedRequestTimeout = 30 * time.Second
)

// ---------------------------------------------------------------------------
// Projection / FTS (rulings §6)
// ---------------------------------------------------------------------------

const (
	ProjectionSchemaVersion = 7 // v7: fts_revisions_trigram companion index (CAPTURE C2); v6: parked_events.retryable (R49/FIX-J1); v5: attachment durability class (N7); v4: derivatives+summaries (N4); v3: subscriptions (N3); v2: parked_events

	// FTSTokenize: unicode61 with tokenchars `_ - # @` (rulings §6).
	FTSTokenize = "unicode61 tokenchars '_-#@'"

	// FTSTrigramTokenize is the CAPTURE C2 companion index's tokenizer. It
	// takes no options: trigram tokenization is fixed-width and already
	// case-folding, so there is nothing to tune here.
	FTSTrigramTokenize = "trigram"

	// FTSTrigramMinTerm is the trigram width, and therefore the shortest
	// query term the companion index can answer at all: a shorter term
	// produces ZERO tokens, so it matches nothing rather than everything.
	// Such terms are dropped from the trigram query (the word index still
	// answers them); a query left with no usable term skips the companion
	// index entirely instead of running a match that cannot hit.
	FTSTrigramMinTerm = 3
)

// ParkedRetryableGrace (RULINGS.md R49.3) is how long a RETRYABLE parked event
// (a projection failure on a missing intra-mesh reference that may still arrive
// — e.g. a topic.link.add replicated ahead of its topic.create) is treated as
// informational rather than a zero-loss failure by doctor/gates. Within the
// grace window a transient cross-node ordering gap during active sync is not a
// red gate; a dependency that never arrives eventually crosses it and becomes a
// failure. Terminal (non-retryable) parked events are ALWAYS a failure.
// Measured from parked_at.
const ParkedRetryableGrace = 24 * time.Hour

// Reindex progress + stall watchdog (FIX-J3, R45: a reindex must never hang
// silently). ReindexProgressInterval throttles the heartbeat log; if no event
// is applied for ReindexStallTimeout the CLI aborts the reindex with a clear
// error (the side-build .rebuild is discardable). The stall timeout is
// deliberately generous — a legitimately slow filesystem (WSL2 drvfs on the
// observed rig) is slow, not wedged — but bounded, so an actual hang surfaces.
const (
	ReindexProgressInterval = 5 * time.Second
	ReindexStallTimeout     = 120 * time.Second
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

	// GateLatencyMinSamples (FIX-J2, R45 spirit) is the minimum number of
	// ack→lexical-visible samples before the P95 gate may return a verdict
	// (PASS or FAIL). The gate reads P95 by rank offset, so with a handful of
	// samples "P95" is just the slowest send — one hiccup fails the gate. Codex
	// Brief A run 2 FAILed on 4 sends (243 ms) on a degraded WSL node that had
	// dropped off the tailnet; that is a small-sample artifact, not a
	// regression. Below this floor the gate reports INCONCLUSIVE (never FAIL):
	// a P95 gate must not fail loudly on a sample too small to be meaningful.
	// 30 aligns with the 30-handoff evaluation cadence and gives the 95th
	// percentile at least one observation above it.
	GateLatencyMinSamples = 30

	// Product-gate thresholds (spec §11, DEPLOY-E3). Success@5: the found
	// message ranked ≤5 in its interaction; workaround rate: fraction of
	// outcomes resolved by going around cairn. Below GateOutcomeMinSamples
	// recorded outcomes the verdicts are INCONCLUSIVE, never FAIL — same
	// small-sample honesty as the latency gate (FIX-J2).
	GateSuccessAt5MinPct     = 70
	GateWorkaroundRateMaxPct = 25
	GateOutcomeMinSamples    = 30
)

// ---------------------------------------------------------------------------
// Filesystem layout and permissions
// ---------------------------------------------------------------------------

const (
	// DefaultDirName under $HOME when --dir / $CAIRN_DIR are unset.
	DefaultDirName = "cairn"

	// ObjectHashHexLen is the exact length of a BLAKE3-256 hex object
	// address. Every externally-supplied hash is validated against this
	// before it is turned into an objects/<first2>/<rest> path — the
	// path construction slices hash[:2] and is unsafe on arbitrary input.
	ObjectHashHexLen = 64

	// Portable-dir entries (build/ARCHITECTURE.md on-disk layout).
	PortableConfigName = "cairn.toml"
	EventsDirName      = "events"
	ObjectsDirName     = "objects"
	ExportsDirName     = "exports"
	ViewsDirName       = "views"
	MapFileName        = "map.md"        // P2-5 local navigation view (spec §7.3)
	MapTopThreads      = 20              // P2-5 top-N threads shown in the map rollup
	CompactionFileName = "compaction.md" // P2-6 current-state compaction view (spec §12)
	DerivedDirName     = ".cairn"        // rebuildable; excluded from backup by default

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

	// SearchSnippetChars (RETR-D1): Unicode scalars of body excerpted into
	// each search result so agents can triage without a fetch per hit. The
	// snippet is quoted (QuotePrefix) and counts against budget_chars like
	// everything else in the payload.
	SearchSnippetChars = 200

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

	// P2-7 heavy derivatives (spec §8.3): OCR / captioning / transcription.
	// OPT-IN (CAIRN_HEAVY_DERIVATIVES=1) — they may shell to external tools and
	// cost real CPU/time; a longer timeout bounds runaway subprocesses. Same
	// input-size cap; the derivative text is still tied to the source hash and
	// untrusted. External tools run with NO network passed through (best-effort).
	HeavyDeriveTimeout    = 60 * time.Second
	HeavyExtractorVersion = "1"

	// HeavyExtractorWaitDelay (P2H7 / shakedown FIND-3): grace after ctx
	// cancellation before the subprocess's I/O pipes are force-closed. Without a
	// WaitDelay, if a FUTURE extractor spawned a child that inherited stdout,
	// cmd.Output() could block past the kill deadline waiting on that pipe.
	// tesseract spawns no such child, so this is defensive today; it becomes
	// load-bearing the moment a heavier/child-spawning extractor is added.
	HeavyExtractorWaitDelay = 5 * time.Second

	// FIX-H5 (R48): decompression-bomb / pixel guards that run BEFORE any heavy
	// extractor. Mesh attachments are untrusted, and tesseract decodes the FULL
	// image — an adversarial image (tiny file, enormous declared dimensions)
	// would OOM the enricher. These bounds are checked from the image HEADER
	// (image.DecodeConfig — no pixel allocation) and reject before decode.
	HeavyMaxImageDimension     = 20000      // per-side pixel cap (declared dims)
	HeavyMaxImagePixels        = 40_000_000 // total pixel ceiling (~40 MP)
	HeavyMaxDecompressionRatio = 200        // decoded-RGBA-bytes / compressed-bytes

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
	// PairingInviteTTL (P3-2): a one-time pairing invitation expires this long
	// after it is minted. The expiry anchor is the root-signed cert's issued_at,
	// so it cannot be forged; the inviting node refuses an expired invite. Short
	// by design — the invitation carries a device credential and is a bearer
	// secret. Spec §336: "one-time expiring high-entropy invitation".
	PairingInviteTTL   = 15 * time.Minute
	PairingHelloDomain = "cairn-pair-hello-v1" // domain-separates the pairing challenge signature

	// Node roles (P3-3, spec §7). Full nodes hold the canonical + eager text
	// window and count toward durability; thin nodes (mobile/metered) hold only a
	// recent window + selected objects, are NOT counted toward durability, are
	// never advertised as a normal node, and answer universal search partially.
	RoleFull = "full"
	RoleThin = "thin"

	// Transports (P3-4, spec §12). TransportTCPTailnet is the P1 default: a TCP
	// socket bound to the tailnet CGNAT address. TransportIroh is the P3 target
	// (iroh 1.x endpoint auth + application-layer membership); its live wire is
	// hardware-gated (no mature Go binding; needs real relays/NAT traversal), so
	// selecting it currently refuses with an instructive error (the P2-7 pattern).
	TransportTCPTailnet = "tcp-tailnet"
	TransportIroh       = "iroh"
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
	// EphemeralLiveDeliveryWindow — R50: an ephemeral body is OFFERED on a
	// push only while the publish event is at most this old (the live-gossip
	// window: push-on-append fires within SyncPushDebounce; the margin covers
	// slow dials). Outside the window the body is never offered — a peer that
	// rejoins later gets the event (chain data) but not the content.
	EphemeralLiveDeliveryWindow = 60 * time.Second
	// EphemeralLiveAcceptWindow — R50 defense in depth on the RECEIVER: an
	// ephemeral body accompanying an ingested event is stored only when the
	// event's wall time is within this window of local now (|skew| tolerant,
	// wider than the send window so conforming live gossip never loses to
	// clock skew). A nonconforming sender's late backfill is refused here.
	EphemeralLiveAcceptWindow = 5 * time.Minute
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

	// SavedSearchesFileName: P2-4 named, re-runnable queries. Device-local
	// operator convenience (like sessions/profiles) — not replicated, not an
	// event; survives reindex.
	SavedSearchesFileName = "saved-searches.json"

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
	LadderDebtLexicalOnly     = 20000 // rung 4

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
