#!/usr/bin/env bash
# Writes THIRD_PARTY_NOTICES: the licence of every module the shipped programs link.
#
#   scripts/third-party-notices.sh <outfile> "<os/arch>..." "<package>..."
#
# The modules are the union, over every platform given, of the modules that Go
# links into the packages given (the programs of a release), built as
# scripts/release.sh builds them. Each module is listed with its path, its version,
# the name of its licence and the text of every licence and notice file at the top
# of its directory in the module cache, verbatim. The Go standard library, which is
# linked into every program, is listed the same way.
#
# The script fails, and lists every offender, when a module has no licence file or a
# licence file that is not one of the licences below. Those are the ones that allow
# the module to be bundled into a binary under another licence; any other licence
# is a decision for the maintainers, not for this script.
set -euo pipefail

allowed="MIT BSD-2-Clause BSD-3-Clause Apache-2.0 ISC MPL-2.0"

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <outfile> \"<os/arch>...\" \"<package>...\"" >&2
  exit 2
fi
out=$1
platforms=$2
packages=$3

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Modules of this repository (the main modules of go.work) are not third party.
# CGO_ENABLED=0 is how the release builds, and it changes which packages link.
: >"$work/linked"
for platform in $platforms; do
  # $packages is a list of words on purpose.
  # shellcheck disable=SC2086
  CGO_ENABLED=0 GOOS=${platform%/*} GOARCH=${platform#*/} go list -deps \
    -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{"\n"}}{{end}}{{end}}' \
    $packages >>"$work/linked"
done
sed '/^$/d' "$work/linked" | LC_ALL=C sort -u >"$work/modules"

# label <file> prints the SPDX identifier of the licence in the file, nothing when
# the text is none of the ones this script knows. A file that only says a module is
# licensed under either of two licences (the multiformats modules do, and ship the
# two texts beside it, or not at all) is labelled with that choice.
label() {
  if grep -q -E 'Apache-2.0 OR MIT|Permissive License Stack' "$1"; then
    echo "Apache-2.0 OR MIT"
  elif grep -q -E 'Mozilla Public License,? [Vv]ersion 2\.0' "$1"; then
    echo "MPL-2.0"
  elif grep -q 'Apache License' "$1" && grep -q 'Version 2\.0' "$1"; then
    echo "Apache-2.0"
  elif grep -q 'Permission is hereby granted, free of charge' "$1"; then
    echo "MIT"
  elif grep -q -E 'Permission to use, copy, modify, and(/or)? distribute this software' "$1"; then
    echo "ISC"
  elif grep -q 'Redistribution and use in source and binary forms' "$1"; then
    if grep -q -i 'neither the name' "$1"; then echo "BSD-3-Clause"; else echo "BSD-2-Clause"; fi
  fi
}

failed=0
problem() {
  echo "$1" >&2
  failed=1
}

# notice <name> <version> <dir> adds one module to the index and the body.
notice() {
  local name=$1 version=$2 dir=$3 file base lic names="" choice="" mpl=false
  local texts="$work/texts"
  : >"$texts"
  # LC_ALL=C sort puts LICENSE before LICENSE-BSD, whatever the machine's locale.
  local licenses notices
  licenses=$(find "$dir" -maxdepth 1 -type f \( -iname 'LICEN[SC]E*' -o -iname 'COPYING*' \) | LC_ALL=C sort)
  notices=$(find "$dir" -maxdepth 1 -type f \( -iname 'NOTICE*' -o -iname 'PATENTS*' \) | LC_ALL=C sort)
  if [ -z "$licenses" ]; then
    problem "$name $version: no licence file in $dir"
    return
  fi
  while IFS= read -r file; do
    base=${file##*/}
    lic=$(label "$file")
    if [ -z "$lic" ]; then
      problem "$name $version: $base is not a licence this script recognises"
      continue
    fi
    for part in ${lic// OR / }; do
      case " $allowed " in
        *" $part "*) ;;
        *) problem "$name $version: $base is $part, which is not one of: $allowed" ;;
      esac
    done
    [ "$lic" = MPL-2.0 ] && mpl=true
    # A choice between two licences names the module; the texts beside it are not
    # further licences.
    case $lic in
      *" OR "*) choice=$lic ;;
      *) case " $names " in *" $lic "*) ;; *) names="${names:+$names AND }$lic" ;; esac ;;
    esac
    {
      printf -- '--- %s ---\n' "$base"
      cat "$file"
      printf '\n'
    } >>"$texts"
  done <<EOF
$licenses
EOF
  while IFS= read -r file; do
    [ -n "$file" ] || continue
    {
      printf -- '--- %s ---\n' "${file##*/}"
      cat "$file"
      printf '\n'
    } >>"$texts"
  done <<EOF
$notices
EOF

  names=${choice:-$names}
  printf '%s %s: %s\n' "$name" "$version" "$names" >>"$work/index"
  {
    printf '================================================================================\n'
    printf '%s %s\nLicense: %s\n\n' "$name" "$version" "$names"
    if $mpl; then
      # The MPL asks that the source of its files stay available to whoever gets the
      # binary. The repository of a Go module is its path without the major version.
      case $name in
        github.com/*/*)
          printf 'The source code of this module, version %s, is available at https://%s\n\n' \
            "$version" "$(echo "$name" | cut -d/ -f1-3)"
          ;;
        *) problem "$name $version: MPL-2.0 needs the repository URL of the module, which this script derives for github.com only" ;;
      esac
    fi
    cat "$texts"
  } >>"$work/body"
}

: >"$work/index"
: >"$work/body"
notice "Go standard library" "$(go env GOVERSION)" "$(go env GOROOT)"
while IFS='|' read -r name version dir; do
  if [ -z "$dir" ]; then
    problem "$name $version: the module is not in the module cache"
    continue
  fi
  notice "$name" "$version" "$dir"
done <"$work/modules"

if [ "$failed" -ne 0 ]; then
  echo "third-party notices not written" >&2
  exit 1
fi

{
  printf 'Third-party software in Sidera\n\n'
  printf 'Sidera is licensed under the terms in LICENSE. Its programs link the Go standard\n'
  printf 'library and the modules below, each under its own licence, which is reproduced\n'
  printf 'here with any notice file the module ships.\n\n'
  cat "$work/index"
  printf '\n'
  cat "$work/body"
} >"$out"
