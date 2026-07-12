//go:build !sqlite_fts5

// FIX-F4 (Codex AUDIT-001): without the sqlite_fts5 build tag the binary
// compiles but cannot start (FTS5 is absent from mattn/go-sqlite3). A
// binary that builds but cannot run is never acceptable — so the build
// FAILS HERE, at compile time, with the fix in the identifier below.
//
//	build with:  make build     (or)  go build -tags sqlite_fts5 ./...
//	test with:   make test      (or)  go test  -tags sqlite_fts5 ./...
package projection

var _ = CAIRN_BUILD_ERROR__missing_required_build_tag__run__make_build__or__go_build_dash_tags_sqlite_fts5
