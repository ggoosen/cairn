// Package tunables holds every tunable constant in the evaluation harness.
//
// The main module keeps its constants in one commented module
// (internal/config/constants.go) so that nothing is a magic number buried in
// a call site; the harness inherits that discipline. If you find yourself
// typing a duration, a limit or a default into harness code, it belongs
// here instead.
//
// Nothing in this file is an evaluation THRESHOLD. Thresholds and kill
// criteria live in eval/claims.yaml, which is the operator's to sign off;
// these are only the mechanical knobs the apparatus needs to run.
package tunables

import "time"

const (
	// DaemonReadyTimeout bounds how long Provision waits for a freshly
	// started daemon to answer `cairn status`. Generous because the first
	// start also opens (and possibly migrates) the SQLite projection, and
	// because a CI runner under load is slower than a dev machine.
	DaemonReadyTimeout = 30 * time.Second

	// DaemonReadyPoll is the interval between readiness probes. Short
	// enough that provisioning cost is dominated by the daemon, not by us.
	DaemonReadyPoll = 50 * time.Millisecond

	// DaemonStopTimeout bounds a graceful SIGTERM shutdown before the
	// driver escalates to SIGKILL. The daemon fsyncs on the way out; give
	// it room, but never hang a test run on it.
	DaemonStopTimeout = 15 * time.Second

	// CommandTimeout bounds a single black-box CLI invocation. A CLI verb
	// that has not answered in this long is a harness-visible failure, not
	// something to wait out.
	CommandTimeout = 60 * time.Second

	// BuildTimeout bounds `go build` of the cairn binary under test. CGO
	// (mattn/go-sqlite3) makes the first build slow and it is not cached
	// between CI runners.
	BuildTimeout = 10 * time.Minute

	// MCPCallTimeout bounds one JSON-RPC request/response exchange with
	// `cairn mcp` over stdio.
	MCPCallTimeout = 60 * time.Second
)

const (
	// DefaultK is the number of results a Retrieve request asks for when
	// the caller does not say. Ten matches the CLI's own default, so the
	// harness measures the surface an agent actually gets.
	DefaultK = 10

	// DefaultBudgetChars is the default budget_chars for a budgeted
	// surface. 1500 matches the digest budget the agent instructions in
	// CLAUDE.md tell sessions to use, for the same reason.
	DefaultBudgetChars = 1500

	// AdversarialBudgetChars is the budget E6 asks its surfaces for. It is
	// deliberately large: a tight budget would drop planted payloads before
	// they reached the agent, and a surface that never carried an injection
	// would report clean containment it had not actually demonstrated. E6 is
	// measuring what happens when content DOES arrive.
	AdversarialBudgetChars = 40000

	// GrepContextChars bounds how much text the file-backed baselines (B1,
	// B2) return around a match. It models "the agent reads the matching
	// chunk", not "the agent reads the whole file"; it is a MODELLING
	// choice, recorded in the backend's own documentation, and it is
	// deliberately not tuned against any corpus.
	GrepContextChars = 600
)

const (
	// ResultSchemaVersion versions the on-disk run record. Bump it on any
	// change to the recorded shape so that old result files stay
	// interpretable — E8 publishes these, and a result nobody can parse
	// later is not a replication artifact.
	ResultSchemaVersion = 1

	// CorpusSchemaVersion versions the corpus manifest format (E3).
	CorpusSchemaVersion = 1

	// MaxCorpusLineBytes bounds one JSONL record. Mined issue bodies can be
	// long; a stack trace pasted into a bug report is routinely tens of KB.
	MaxCorpusLineBytes = 4 << 20

	// MinedBodyChars caps how much of a source body is included when a query
	// is built from title AND body. Whole bug reports as queries would be an
	// unusual thing for a person to ask.
	MinedBodyChars = 500
)

// Split assignment (BUILD-PLAN §3.7: no tuning on the evaluation set).
const (
	// SplitSalt fixes the dev/holdout partition. It is a CONSTANT and must
	// never be changed to "rebalance" a corpus: a split that can be re-rolled
	// is a split that can be re-rolled until the holdout flatters the system.
	// Changing it invalidates every result measured against the old split,
	// and any change belongs in the same commit as that admission.
	SplitSalt = "cairn-eval-split-v1:"

	// HoldoutFractionPerMyriad is the share of queries held out, in parts per
	// ten thousand (3000 = 30%). Integer parts-per-myriad rather than a float
	// so the partition is exactly reproducible on any machine.
	HoldoutFractionPerMyriad = 3000
)
