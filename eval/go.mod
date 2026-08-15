// The evaluation harness is a SEPARATE Go module on purpose (EVAL-PLAN §3):
// Go's internal/ visibility rule then makes black-box access
// compiler-enforced rather than conventional — nothing here can import
// github.com/ggoosen/cairn/internal/..., so the harness can only reach Cairn
// the way a real agent does (the CLI and the MCP stdio surface).
//
// It also keeps the harness's future dependencies (LLM clients, statistics)
// out of the daemon's deliberately small, offline dependency tree.
//
// DEPENDENCIES: this module has none, and that is a property worth keeping.
// The T0 tier must stay offline, deterministic and free (EVAL-PLAN §4); a
// stdlib-only harness cannot quietly stop being any of those. LLM clients
// arrive with E5, not before.
module github.com/ggoosen/cairn/eval

go 1.25.0

toolchain go1.26.3
