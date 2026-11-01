#!/usr/bin/env bash
# Prints the notes of one release: the body of its section in CHANGELOG.md, without
# the heading.
#
#   scripts/release-notes.sh <version>
#
# <version> is the release version without a leading "v" (0.8.0). The section is the
# one whose heading starts with "## [0.8.0]". A missing or empty section is an error,
# so that a release cannot be published without notes.
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <version>" >&2
  exit 2
fi
version=$1

root=$(cd "$(dirname "$0")/.." && pwd)
notes=$(awk -v heading="## [$version]" '
  /^## / { if (found) exit; if (index($0, heading) == 1) { found = 1; next } }
  /^\[[^]]+\]: / { if (found) exit }
  found { print }
' "$root/CHANGELOG.md")

if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "CHANGELOG.md has no notes for version $version" >&2
  exit 1
fi
printf '%s\n' "$notes"
