#!/usr/bin/env bash
# Signs the checksums of one release with minisign.
#
#   scripts/release-sign.sh <version> <distdir>
#
# <version> is the release version without a leading "v" (0.8.0). <distdir> is the
# output of scripts/release.sh; the script adds checksums.txt.minisig to it, the
# prehashed minisign signature of checksums.txt. Its trusted comment, which the
# signature covers, is
#
#   sidera <version> update-from <floor>
#
# where <floor> is the version in scripts/update-from. A verifier that asks for a
# version can therefore tell that the signature belongs to that version, and which
# installs may update to it. The script refuses a floor above <version> by SemVer
# precedence (a pre-release is below its release, so 0.8.0-rc.1 cannot have the
# floor 0.8.0) and a checksums.txt that does not list exactly the archives of
# <version> that scripts/release.sh writes.
#
# The secret key comes from the environment, never from the command line:
#
#   MINISIGN_SECRET_KEY  the content of the minisign secret key file
#   MINISIGN_PASSWORD    its password. When unset, minisign asks on the terminal.
#
# On a development machine: MINISIGN_SECRET_KEY=$(cat <key file>) and
# MINISIGN_PASSWORD=..., or a throwaway key made with `minisign -G`. A signature
# made with a key other than the release key needs MINISIGN_PUBLIC_KEY_FILE to name
# its public key; by default the script checks against internal/release/minisign.pub,
# the key of the releases, and fails when the signature does not verify with it.
# That check is what catches a wrong secret before anything is published.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <version> <distdir>" >&2
  exit 2
fi
version=$1
dist=$2

root=$(cd "$(dirname "$0")/.." && pwd)
public_key=${MINISIGN_PUBLIC_KEY_FILE:-$root/internal/release/minisign.pub}

[ -f "$dist/checksums.txt" ] || { echo "$dist/checksums.txt does not exist; run scripts/release.sh first" >&2; exit 1; }
# The version is the one scripts/release.sh built, and that script alone defines
# what a version looks like: every line of checksums.txt must name an archive of
# exactly this version, so a string that is not such a version fails here.
while read -r sum name extra; do
  if [[ -n $extra || ! $sum =~ ^[0-9a-f]{64}$ || $name != "sidera_${version}_"* \
    || ! ${name#"sidera_${version}_"} =~ ^(darwin|linux|windows)_(arm64|amd64)\.(tar\.gz|zip)$ ]]; then
    echo "$dist/checksums.txt has a line that is not an archive of version $version: $sum $name $extra" >&2
    exit 1
  fi
done <"$dist/checksums.txt"
[ -s "$dist/checksums.txt" ] || { echo "$dist/checksums.txt is empty" >&2; exit 1; }
[[ $version =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)(-.*)?$ ]] || { echo "cannot read the version: $version" >&2; exit 1; }
version_core=("${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}")
version_pre=${BASH_REMATCH[4]}

# The floor: the one line of scripts/update-from that is not a comment. It is a
# release version, MAJOR.MINOR.PATCH without leading zeros, pre-release part or
# build metadata.
floor=$(grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$root/scripts/update-from" || true)
if ! [[ $floor =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "scripts/update-from must hold exactly one version MAJOR.MINOR.PATCH, found: \"$floor\"" >&2
  exit 1
fi
floor_core=("${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}")
# A floor above the release would forbid every install from updating to it. By
# SemVer precedence a pre-release is below the release of the same numbers, so
# 0.8.0-rc.1 is below the floor 0.8.0 too.
above=false
for i in 0 1 2; do
  if [ "$((10#${floor_core[$i]}))" -lt "$((10#${version_core[$i]}))" ]; then break; fi
  if [ "$((10#${floor_core[$i]}))" -gt "$((10#${version_core[$i]}))" ]; then above=true; break; fi
  if [ "$i" = 2 ] && [ -n "$version_pre" ]; then above=true; fi
done
if $above; then
  echo "update-from $floor is above the version $version" >&2
  exit 1
fi

command -v minisign >/dev/null || { echo "minisign is not installed" >&2; exit 1; }
[ -f "$public_key" ] || { echo "no public key at $public_key" >&2; exit 1; }
[ -n "${MINISIGN_SECRET_KEY:-}" ] || { echo "MINISIGN_SECRET_KEY is not set" >&2; exit 1; }
[ ! -e "$dist/checksums.txt.minisig" ] || { echo "$dist/checksums.txt.minisig exists already" >&2; exit 1; }
dist=$(cd "$dist" && pwd)

# minisign reads the key from a file only. The file lives in a directory of this
# script's own with mode 0700, outside the checkout and the release, and is removed
# when the script ends, however it ends.
work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/release-sign.XXXXXX")
trap 'rm -rf "$work"' EXIT
# The umask is for the key file only: the signature written next to the archives
# is published and keeps the usual mode.
(umask 077 && printf '%s\n' "$MINISIGN_SECRET_KEY" >"$work/minisign.key")

comment="sidera $version update-from $floor"
if [ -n "${MINISIGN_PASSWORD+x}" ]; then
  # With its input not a terminal, minisign reads the password from it, one line.
  printf '%s\n' "$MINISIGN_PASSWORD" | minisign -S -s "$work/minisign.key" \
    -m "$dist/checksums.txt" -x "$dist/checksums.txt.minisig" -t "$comment" >/dev/null
else
  minisign -S -s "$work/minisign.key" \
    -m "$dist/checksums.txt" -x "$dist/checksums.txt.minisig" -t "$comment" >/dev/null
fi

# -Q prints the trusted comment of a signature that verifies and nothing else.
if ! signed=$(minisign -V -Q -p "$public_key" -m "$dist/checksums.txt" -x "$dist/checksums.txt.minisig"); then
  rm -f "$dist/checksums.txt.minisig"
  echo "the signature does not verify with $public_key: wrong secret key?" >&2
  exit 1
fi
if [ "$signed" != "$comment" ]; then
  rm -f "$dist/checksums.txt.minisig"
  echo "signed comment \"$signed\", expected \"$comment\"" >&2
  exit 1
fi
echo "signed $dist/checksums.txt: $signed"
