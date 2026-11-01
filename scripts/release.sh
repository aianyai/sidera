#!/usr/bin/env bash
# Builds the release archives of one version, from the working tree.
#
#   scripts/release.sh <version> <outdir>
#
# <version> is the release version without a leading "v" (0.8.0). <outdir> receives
# one archive per platform, sidera_<version>_<os>_<arch>.tar.gz (.zip for Windows),
# and checksums.txt, the SHA-256 of every archive. Nothing else is left in it.
#
# An archive holds the binaries at its top level, the adapters' packages in
# adapters/<name>/ (ready for `sidera adapter install`), LICENSE and README.txt.
# The workflow .github/workflows/release.yml runs this script, and so can anyone
# who wants to check what a release would contain.
set -euo pipefail

module=github.com/aianyai/sidera
platforms="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64"
# The adapters every archive carries; the Claude adapter is added for the platforms
# its manifest lists, which are the darwin ones (cmd/sidera-adapter-claude).
adapters="codex console"

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <version> <outdir>" >&2
  exit 2
fi
version=$1
outdir=$2
if ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "version must look like 0.8.0, without a leading v: $version" >&2
  exit 2
fi

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)
if [ -n "$(ls -A "$outdir")" ]; then
  echo "$outdir is not empty" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Every program reports the release version, set by the linker, and the commit the
# tree is at. The commit is stated here because Go records the wrong one for a build
# made in a linked git worktree.
commit=$(git rev-parse HEAD)
modified=false
[ -n "$(git status --porcelain)" ] && modified=true
ldflags="-s -w -X $module/internal/release.stamped=$version"
ldflags="$ldflags -X $module/internal/release.stampedCommit=$commit -X $module/internal/release.stampedModified=$modified"

# build <goos> <goarch> <program> <output>
build() {
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$ldflags" -o "$4" "./cmd/$3"
}

# sha256 prints the SHA-256 of the files named, one "<hash>  <name>" line each.
sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

# The archives carry no user or group of the machine that built them. GNU tar and
# bsdtar (macOS) spell the option differently.
if tar --version | grep -q GNU; then
  tar_owner=(--owner=0 --group=0 --numeric-owner)
else
  tar_owner=(--uid 0 --gid 0 --uname root --gname root)
fi

# The adapters' `package` subcommand has to run here, on the machine that builds,
# whatever platform the package is for, so one native copy of each is built.
hostos=$(go env GOHOSTOS)
hostarch=$(go env GOHOSTARCH)
hostexe=""
[ "$hostos" = windows ] && hostexe=.exe
mkdir -p "$work/host"
for name in claude $adapters; do
  build "$hostos" "$hostarch" "sidera-adapter-$name" "$work/host/sidera-adapter-$name$hostexe"
done
# A release stamped at the wrong path would silently report the development
# version, so the first program built is asked what it reports.
build "$hostos" "$hostarch" sidera "$work/host/sidera$hostexe"
reported=$("$work/host/sidera$hostexe" version)
case $reported in
  "sidera $version ("*) ;;
  *) echo "sidera reports \"$reported\", not version $version" >&2; exit 1 ;;
esac

for platform in $platforms; do
  goos=${platform%/*}
  goarch=${platform#*/}
  exe=""
  [ "$goos" = windows ] && exe=.exe
  name=sidera_${version}_${goos}_${goarch}
  stage=$work/$name
  mkdir -p "$stage/adapters"

  build "$goos" "$goarch" sidera "$stage/sidera$exe"
  files=("sidera$exe")
  platform_adapters=$adapters
  [ "$goos" = darwin ] && platform_adapters="claude $adapters"
  for adapter in $platform_adapters; do
    binary=$stage/sidera-adapter-$adapter$exe
    build "$goos" "$goarch" "sidera-adapter-$adapter" "$binary"
    "$work/host/sidera-adapter-$adapter$hostexe" package \
      --goos "$goos" --goarch "$goarch" --binary "$binary" --out "$stage/adapters/$adapter"
    files+=("sidera-adapter-$adapter$exe")
  done

  cp LICENSE "$stage/LICENSE"
  cat >"$stage/README.txt" <<README
Sidera $version ($goos/$goarch)

The program sidera, the adapter programs, and the adapter packages in adapters/
that 'sidera adapter install' takes.

Install guide:
https://github.com/aianyai/sidera/blob/v$version/docs/guide/install.md
README

  # The archive has no directory around its files, and no extended attributes of
  # the build machine (macOS tar would add them).
  files+=(adapters LICENSE README.txt)
  if [ "$goos" = windows ]; then
    (cd "$stage" && zip -qr "$outdir/$name.zip" "${files[@]}")
  else
    COPYFILE_DISABLE=1 tar "${tar_owner[@]}" -czf "$outdir/$name.tar.gz" -C "$stage" "${files[@]}"
  fi
done

(cd "$outdir" && sha256 sidera_"$version"_* >checksums.txt)
echo "release $version written to $outdir"
