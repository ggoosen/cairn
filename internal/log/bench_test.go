package log_test

// CI-B4: benchmarks for the two paths the P95 gate depends on. Run with
// `go test -tags sqlite_fts5 -bench . ./internal/log/`.

import (
	"testing"

	"github.com/ggoosen/cairn/internal/fsx"
	"github.com/ggoosen/cairn/internal/identity"
	cairnlog "github.com/ggoosen/cairn/internal/log"
)

func BenchmarkAppend(b *testing.B) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	c, lg := newLogWithGenesis(b, m, "/p", 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env, rec := c.next(b)
		if err := lg.Append(rec, env); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecovery(b *testing.B) {
	m := fsx.NewMemFS()
	m.MkdirAll("/p", 0o700)
	c, lg := newLogWithGenesis(b, m, "/p", 1000)
	if err := lg.Close(); err != nil {
		b.Fatal(err)
	}
	verify := identity.NewChainVerifier().Verify
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clone := m.Clone()
		lg2, _, err := cairnlog.Open(clone, "/p", origin(c), verify, nil)
		if err != nil {
			b.Fatal(err)
		}
		lg2.Close()
	}
}
