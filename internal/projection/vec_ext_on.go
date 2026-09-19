//go:build !cairn_novec

package projection

// D1: the pinned vector extension (CLAUDE.md library table). The cgo binding
// compiles sqlite-vec from source INTO this binary and registers it as an
// SQLite auto-extension, so every connection this process opens — including
// the projection's — gets `vec0` and `vec_version()`. Nothing is loaded from
// disk at runtime, which is precisely why the ONNX path failed and this one
// does not.
//
// The `cairn_novec` build tag compiles it out entirely. That is not a
// deployment option; it exists so the extension-ABSENT path required by
// rulings §7 can be exercised for real — a binary in which vec0 genuinely
// does not exist — rather than only simulated. See vec_ext_off.go.

import sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"

func registerVecExtension() { sqlitevec.Auto() }
