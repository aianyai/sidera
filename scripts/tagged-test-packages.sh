#!/usr/bin/env bash
# Prints the packages whose tests a set of build tags changes.
#
#   scripts/tagged-test-packages.sh <tags>
#
# <tags> is the comma-separated list `go test -tags` takes. The output is the import
# path of every package of ./... and ./sdk/go/... that has tests and whose test binary
# differs when built with the tags from the one built without them, one per line,
# sorted. A package outside the list builds the same test from the same files and
# dependencies either way, so running it again with the tags would repeat the verdict
# of the run without them.
#
# A package is on the list when its test binary links a package, test variant
# included, that the tags change: one whose own files differ (GoFiles, CgoFiles,
# TestGoFiles, XTestGoFiles, EmbedFiles) or that exists with only one of the two sets
# of tags. Whether the binary links it is read from the dependencies `go list` reports
# for the generated test main package.
#
# The script fails, and says so on standard error, when `go list` fails or when the
# list would be empty: a job that runs the packages named here would otherwise pass
# having tested nothing.
set -euo pipefail

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $0 <tags>" >&2
  exit 2
fi
tags=$1

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# golist <outfile> <go list arguments>: go list over both modules, failing loudly.
golist() {
  out=$1
  shift
  if ! go list -e -test "$@" ./... ./sdk/go/... >"$out"; then
    echo "go list failed: go list -e -test $*" >&2
    exit 1
  fi
}

# One line per package, with every field that decides what its test binary is. "|"
# separates the fields because an ID such as "p [p.test]" itself holds spaces.
files='{{.ImportPath}}|{{.GoFiles}}|{{.CgoFiles}}|{{.TestGoFiles}}|{{.XTestGoFiles}}|{{.EmbedFiles}}'
golist "$work/plain" -deps -f "$files"
golist "$work/tagged" -deps -f "$files" -tags "$tags"

# The IDs whose line is in one listing only or differs, minus the generated test
# mains (IDs ending in ".test"): their generated file is no source of difference.
LC_ALL=C sort "$work/plain" >"$work/plain.sorted"
LC_ALL=C sort "$work/tagged" >"$work/tagged.sorted"
LC_ALL=C comm -3 "$work/plain.sorted" "$work/tagged.sorted" |
  sed 's/^[[:space:]]*//' | cut -d'|' -f1 | grep -v '\.test$' | LC_ALL=C sort -u >"$work/changed" || true

# "|" separates the dependencies for the same reason.
golist "$work/deps" -f '{{.ImportPath}}|{{join .Deps "|"}}' -tags "$tags"
awk -F'|' '
  NR == FNR { changed[$0] = 1; next }
  $1 ~ /\.test$/ {
    for (i = 2; i <= NF; i++) {
      if ($i in changed) { print substr($1, 1, length($1) - 5); break }
    }
  }
' "$work/changed" "$work/deps" | LC_ALL=C sort -u >"$work/packages"

if [ ! -s "$work/packages" ]; then
  echo "no package has a test binary that differs with -tags $tags" >&2
  exit 1
fi
cat "$work/packages"
