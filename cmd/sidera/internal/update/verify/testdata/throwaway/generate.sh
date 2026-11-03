#!/usr/bin/env bash
# Regenerates the fixtures of this directory with the real minisign tool:
#
#   TMPDIR=<scratch dir> ./generate.sh
#
# A throwaway key is made for the run, signs every case, and is deleted with the
# directory it lived in; only the public key (throwaway.pub) and the signed files
# are kept. Each case is <case>.txt, a checksums.txt, and <case>.txt.minisig, its
# signature. A case differs from the "good" one in a single respect that its name
# says: the trusted comment (comment-*) or the content of checksums.txt (sums-*).
# The archive hashes are not real: the hash of a name is the SHA-256 of the name,
# so that a test can tell which line an answer came from.
set -euo pipefail

cd "$(dirname "$0")"
command -v minisign >/dev/null || { echo "minisign is not installed" >&2; exit 1; }
rm -f ./*.txt ./*.txt.minisig throwaway.pub

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
minisign -G -W -p throwaway.pub -s "$work/throwaway.key" >/dev/null

# The tool scripts/release.sh uses: sha256sum, or shasum -a 256 where there is none (macOS).
sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

fake_sum() { printf '%s' "$1" | sha256 | cut -c1-64; }

# lines <version>: the six archives scripts/release.sh writes, in its order.
lines() {
  local p n
  for p in darwin_amd64.tar.gz darwin_arm64.tar.gz linux_amd64.tar.gz linux_arm64.tar.gz windows_amd64.zip windows_arm64.zip; do
    n=sidera_$1_$p
    printf '%s  %s\n' "$(fake_sum "$n")" "$n"
  done
}

# sign <case> <trusted comment>: the checksums.txt is on stdin.
sign() {
  cat >"$1.txt"
  minisign -S -s "$work/throwaway.key" -m "$1.txt" -x "$1.txt.minisig" -t "$2" >/dev/null
}

# replace_third <text>: good lines with the third one (linux_amd64) replaced.
replace_third() { lines 0.12.0 | awk -v r="$1" 'NR==3 { print r; next } { print }'; }

version=0.12.0
comment="sidera $version update-from 0.8.0"
name=sidera_${version}_linux_amd64.tar.gz
hash=$(fake_sum "$name")
tab=$(printf '\t')

# Trusted comments.
lines "$version" | sign good "$comment"
lines "$version" | sign comment-double-space "sidera  $version update-from 0.8.0"
lines "$version" | sign comment-leading-space " sidera $version update-from 0.8.0"
lines "$version" | sign comment-trailing-space "sidera $version update-from 0.8.0 "
lines "$version" | sign comment-tab-separated "sidera${tab}$version${tab}update-from${tab}0.8.0"
lines "$version" | sign comment-missing-floor "sidera $version update-from"
lines "$version" | sign comment-missing-keyword-and-floor "sidera $version"
lines "$version" | sign comment-extra-field "sidera $version update-from 0.8.0 extra"
lines "$version" | sign comment-v-version "sidera v$version update-from 0.8.0"
lines "$version" | sign comment-v-floor "sidera $version update-from v0.8.0"
lines "$version" | sign comment-floor-prerelease "sidera $version update-from 0.8.0-rc.1"
lines "$version" | sign comment-floor-leading-zero "sidera $version update-from 0.08.0"
lines "$version" | sign comment-floor-two-parts "sidera $version update-from 0.8"
lines "$version" | sign comment-floor-build "sidera $version update-from 0.8.0+1"
lines "$version" | sign comment-version-build "sidera $version+1 update-from 0.8.0"
lines "$version" | sign comment-wrong-name "siderax $version update-from 0.8.0"
lines "$version" | sign comment-capitalised "Sidera $version update-from 0.8.0"
lines "$version" | sign comment-wrong-keyword "sidera $version update-to 0.8.0"
lines "$version" | sign comment-other-version "sidera 0.12.1 update-from 0.8.0"
lines "$version" | sign comment-floor-above "sidera $version update-from 0.13.0"
lines 0.12.0-rc.1 | sign prerelease "sidera 0.12.0-rc.1 update-from 0.8.0"

# The content of checksums.txt, under the right trusted comment.
{ lines "$version"; printf '%s  %s\n' "$hash" "$name"; } | sign sums-duplicate "$comment"
{ lines "$version"; printf '%s  %s\n' "$(fake_sum other)" "$name"; } | sign sums-duplicate-other-hash "$comment"
lines "$version" | awk 'NR != 4' | sign sums-missing-platform "$comment"
replace_third "$hash $name" | sign sums-one-space "$comment"
replace_third "$hash   $name" | sign sums-three-spaces "$comment"
replace_third "$hash${tab}$name" | sign sums-tab "$comment"
replace_third "$hash *$name" | sign sums-binary-mode "$comment"
replace_third "$(printf '%s' "$hash" | tr a-f A-F)  $name" | sign sums-uppercase-hex "$comment"
replace_third "${hash%?}  $name" | sign sums-short-hash "$comment"
replace_third "${hash}0  $name" | sign sums-long-hash "$comment"
replace_third "g${hash#?}  $name" | sign sums-non-hex "$comment"
replace_third "$hash  sidera_0.11.0_linux_amd64.tar.gz" | sign sums-foreign-version "$comment"
replace_third "$hash  sidera_v${version}_linux_amd64.tar.gz" | sign sums-v-version "$comment"
replace_third "$hash  evil.tar.gz" | sign sums-foreign-name "$comment"
replace_third "$hash  ./$name" | sign sums-dot-path "$comment"
replace_third "$hash  ../$name" | sign sums-parent-path "$comment"
replace_third "$hash  sidera_${version}_freebsd_amd64.tar.gz" | sign sums-unknown-os "$comment"
replace_third "$hash  sidera_${version}_linux_riscv64.tar.gz" | sign sums-unknown-arch "$comment"
replace_third "$hash  sidera_${version}_linux_amd64.zip" | sign sums-wrong-extension "$comment"
replace_third "$hash  sidera_${version}_windows_amd64.tar.gz" | sign sums-windows-tar "$comment"
replace_third "$hash  $name.sig" | sign sums-name-suffix "$comment"
replace_third "$hash  $name extra" | sign sums-extra-column "$comment"
replace_third "$hash  $name " | sign sums-trailing-space "$comment"
replace_third " $hash  $name" | sign sums-leading-space "$comment"
{ echo '# checksums'; lines "$version"; } | sign sums-comment-line "$comment"
lines "$version" | awk 'NR == 3 { print "" } { print }' | sign sums-blank-line "$comment"
{ lines "$version"; echo; } | sign sums-trailing-blank-line "$comment"
lines "$version" | awk '{ printf "%s\r\n", $0 }' | sign sums-crlf "$comment"
printf '%s' "$(lines "$version")" | sign sums-no-final-newline "$comment"
printf '' | sign sums-empty "$comment"
