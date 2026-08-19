//go:build cairn_novec

package projection

// D1: built with `cairn_novec`, sqlite-vec is not compiled in at all — no
// `vec0`, no `vec_version()`. The feature probe in vec.go then fails for
// real and the projection falls back to the brute-force cosine scan that
// rulings §7 sanctions. This is how the fallback is TESTED (a genuinely
// extension-less binary), not a supported way to ship.

func registerVecExtension() {}
