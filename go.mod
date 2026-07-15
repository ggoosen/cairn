module github.com/ggoosen/cairn

// The `go` directive is major.minor only and states the true minimum the
// dependency set requires (golang.org/x/net v0.57.0 declares go 1.25.0 — the
// binding floor; our own code compiles at 1.23). `toolchain` pins the build
// compiler for reproducibility. See RULINGS.md R52.
go 1.25.0

toolchain go1.26.3

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/google/uuid v1.6.0
	github.com/gowebpki/jcs v1.0.1
	github.com/ledongthuc/pdf v0.0.0-20250511090121-5959a4027728
	github.com/mattn/go-sqlite3 v1.14.47
	github.com/spf13/cobra v1.10.2
	golang.org/x/net v0.57.0
	gopkg.in/yaml.v3 v3.0.0-20200313102051-9f266ea9e77c
	lukechampine.com/blake3 v1.4.1
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)
