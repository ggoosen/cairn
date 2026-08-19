#!/usr/bin/env sh
# D6 — render packaging/homebrew/cairn.rb.tmpl into a real formula.
#
#   packaging/mkformula.sh --tag v0.1.0 --repo ggoosen/cairn \
#       --checksums dist/checksums.txt [--signed] [-o dist/cairn.rb]
#
# The checksums file is `shasum -a 256` output over the artifacts as uploaded.
# Every placeholder must resolve: a formula that ships with an unsubstituted
# @@SHA@@ would either refuse to install or, worse, be "fixed" later by hand
# against bytes nobody verified. So a missing checksum is a hard error here,
# not a warning at the far end.
set -eu

TAG=""; REPO=""; SUMS=""; OUT="-"; SIGNED="no"
while [ $# -gt 0 ]; do
  case "$1" in
    --tag) TAG="$2"; shift 2 ;;
    --repo) REPO="$2"; shift 2 ;;
    --checksums) SUMS="$2"; shift 2 ;;
    --signed) SIGNED="yes"; shift ;;
    -o) OUT="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$TAG" ] || { echo "--tag is required" >&2; exit 2; }
[ -n "$REPO" ] || { echo "--repo is required" >&2; exit 2; }
[ -n "$SUMS" ] && [ -f "$SUMS" ] || { echo "--checksums <file> is required and must exist" >&2; exit 2; }

TMPL="$(dirname "$0")/homebrew/cairn.rb.tmpl"
[ -f "$TMPL" ] || { echo "template not found: $TMPL" >&2; exit 2; }

# Homebrew versions have no leading "v"; the tag usually does.
VERSION="${TAG#v}"

# Pull one checksum by artifact filename. Matches the basename, so it works
# whether the checksums file names bare files or paths.
sum_for() {
  want="$1"
  got="$(awk -v f="$want" '{ n=$NF; sub(/.*\//, "", n); if (n == f) { print $1; exit } }' "$SUMS")"
  [ -n "$got" ] || { echo "no checksum for $want in $SUMS" >&2; exit 1; }
  printf '%s' "$got"
}

SHA_DARWIN_ARM64="$(sum_for "cairn_${VERSION}_darwin_arm64.tar.gz")"
SHA_LINUX_AMD64="$(sum_for "cairn_${VERSION}_linux_amd64.tar.gz")"
SHA_LINUX_ARM64="$(sum_for "cairn_${VERSION}_linux_arm64.tar.gz")"

# The honest note. Cairn has no Apple Developer ID today, so the macOS artifact
# is ad-hoc signed by the Go linker (enough to RUN on Apple Silicon) but not
# Developer ID signed and not notarized. Homebrew does not quarantine formula
# downloads, so `brew install` works; a browser download of the same tarball
# would be quarantined and Gatekeeper would block it. Say so, either way,
# rather than letting a user discover it.
if [ "$SIGNED" = "yes" ]; then
  NOTE="      The macOS binary is Developer ID signed and notarized by Apple.
      Verify it yourself:  codesign -dv --verbose=4 \$(brew --prefix)/bin/cairn"
else
  NOTE="      NOT CODE SIGNED. The macOS binary carries only the ad-hoc signature
      the Go linker applies — enough to run on Apple Silicon, but it is not
      Developer ID signed and not notarized by Apple, because this project has
      no Apple Developer ID. \`brew install\` is unaffected (Homebrew does not
      quarantine formula downloads). Downloading the same tarball with a
      browser WOULD be quarantined and Gatekeeper would refuse it; clear that
      with:  xattr -d com.apple.quarantine ./cairn

      Verify what you got instead of trusting the signature: the release page
      publishes checksums.txt, and \`brew\` already checked the sha256 above."
fi

render() {
  awk -v repo="$REPO" -v tag="$TAG" -v version="$VERSION" \
      -v d_arm="$SHA_DARWIN_ARM64" -v l_amd="$SHA_LINUX_AMD64" -v l_arm="$SHA_LINUX_ARM64" \
      -v note="$NOTE" '
    {
      gsub(/@@REPO@@/, repo)
      gsub(/@@TAG@@/, tag)
      gsub(/@@VERSION@@/, version)
      gsub(/@@SHA_DARWIN_ARM64@@/, d_arm)
      gsub(/@@SHA_LINUX_AMD64@@/, l_amd)
      gsub(/@@SHA_LINUX_ARM64@@/, l_arm)
      if ($0 ~ /@@SIGNING_NOTE@@/) { print note; next }
      print
    }' "$TMPL"
}

RENDERED="$(render)"
case "$RENDERED" in
  *@@*) echo "unsubstituted placeholder left in the rendered formula" >&2; exit 1 ;;
esac

if [ "$OUT" = "-" ]; then
  printf '%s\n' "$RENDERED"
else
  printf '%s\n' "$RENDERED" > "$OUT"
  echo "wrote $OUT"
fi
