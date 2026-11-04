#!/usr/bin/env bash
# Builds the release archives of one version, from the working tree.
#
#   scripts/release.sh <version> <outdir>
#
# <version> is the release version without a leading "v" (0.8.0, 0.8.0-rc.1), the
# SemVer 2.0.0 syntax without build metadata that lib/release-format.sh defines and
# `sidera update` reads. <outdir>
# receives one archive per platform, sidera_<version>_<os>_<arch>.tar.gz (.zip for
# Windows), and checksums.txt, the SHA-256 of every archive. Nothing else is left in it.
#
# Before anything is built it checks that both install guides quote the public key the
# release is signed with, internal/release/minisign.pub.
#
# An archive holds, at its top level, sidera, sidera-adapter-console (which is also
# the owner's command-line client), LICENSE, THIRD_PARTY_NOTICES and README.txt. The adapters' programs
# are only inside their packages, adapters/<name>/, ready for `sidera adapter
# install`.
# The workflow .github/workflows/release.yml runs this script, and so can anyone
# who wants to check what a release would contain.
set -euo pipefail

module=github.com/aianyai/sidera
platforms="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"
# The adapters, each shipped as a package for every platform above: an adapter
# whose manifest lists fewer makes its `package` subcommand refuse and the script stop.
adapters="claude codex console hermes"

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <version> <outdir>" >&2
  exit 2
fi
version=$1
outdir=$2

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
# shellcheck source-path=SCRIPTDIR source=lib/release-format.sh
. scripts/lib/release-format.sh

# A version that `sidera update` would refuse could never be installed by anyone.
if ! is_release_version "$version"; then
  echo "version must be a release version without a leading v or build metadata, like 0.8.0 or 0.8.0-rc.1: $version" >&2
  exit 2
fi

# The key the releases are signed with is the second line of the .pub file, and the
# install guides quote it for whoever verifies a download by hand. A key that changed
# in one place only would make every manual verification fail, so a release whose
# guides disagree with the file stops before anything is built.
key=$(sed -n 2p internal/release/minisign.pub)
if [ -z "$key" ]; then
  echo "internal/release/minisign.pub has no key on its second line" >&2
  exit 1
fi
for guide in docs/guide/install.md docs/agent-guide.md; do
  if ! grep -qF -- "$key" "$guide"; then
    echo "$guide does not quote the key of internal/release/minisign.pub" >&2
    exit 1
  fi
done

mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)
if [ -n "$(ls -A "$outdir")" ]; then
  echo "$outdir is not empty" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The licences of every module any program links, on any platform. It comes before
# the builds because a module without an acceptable licence stops the release.
packages="./cmd/sidera"
for adapter in $adapters; do
  packages="$packages ./cmd/sidera-adapter-$adapter"
done
scripts/third-party-notices.sh "$work/THIRD_PARTY_NOTICES" "$platforms" "$packages"

# Every program reports the release version, set by the linker, that it comes from a
# release archive, and the commit the tree is at. The commit is stated here because Go records the wrong one for a build
# made in a linked git worktree.
commit=$(git rev-parse HEAD)
modified=false
[ -n "$(git status --porcelain)" ] && modified=true
ldflags="-s -w -X $module/internal/release.stamped=$version"
ldflags="$ldflags -X $module/internal/release.stampedDistribution=archive"
ldflags="$ldflags -X $module/internal/release.stampedCommit=$commit -X $module/internal/release.stampedModified=$modified"

# build <goos> <goarch> <program> <output>
build() {
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$ldflags" -o "$4" "./cmd/$3"
}

# sha256 prints the SHA-256 of the files named, one "<hash>  <name>" line each.
sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

# The archives carry no user or group of the machine that built them, and no extended
# attributes: bsdtar (macOS) stores attributes such as com.apple.provenance as pax
# headers, and GNU tar prints a warning for each one when it lists or extracts the
# archive. GNU tar stores none unless asked, so its options need no such flag. GNU tar
# and bsdtar spell the owner options differently.
if tar --version | grep -q GNU; then
  tar_options=(--owner=0 --group=0 --numeric-owner)
else
  tar_options=(--uid 0 --gid 0 --uname root --gname root --no-xattrs)
fi

# The adapters' `package` subcommand has to run here, on the machine that builds,
# whatever platform the package is for, so one native copy of each is built.
hostos=$(go env GOHOSTOS)
hostarch=$(go env GOHOSTARCH)
hostexe=""
[ "$hostos" = windows ] && hostexe=.exe
mkdir -p "$work/host"
for name in $adapters; do
  build "$hostos" "$hostarch" "sidera-adapter-$name" "$work/host/sidera-adapter-$name$hostexe"
done
# A release stamped at the wrong path would silently report the development
# version, and one whose distribution was not stamped would not be taken for an
# archive by `sidera update`, so the first program built is asked what it reports.
build "$hostos" "$hostarch" sidera "$work/host/sidera$hostexe"
reported=$("$work/host/sidera$hostexe" version)
case $reported in
  "sidera $version ("*) ;;
  *) echo "sidera reports \"$reported\", not version $version" >&2; exit 1 ;;
esac
reported=$("$work/host/sidera$hostexe" version --json)
case $reported in
  *'"distribution":"archive"'*) ;;
  *) echo "sidera reports $reported, not distribution archive" >&2; exit 1 ;;
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
  # Only the console program is also an archive file of its own; the others are
  # built outside the archive and reach it inside their packages.
  files=("sidera$exe" "sidera-adapter-console$exe")
  mkdir -p "$work/$name-bin"
  for adapter in $adapters; do
    binary=$work/$name-bin/sidera-adapter-$adapter$exe
    [ "$adapter" = console ] && binary=$stage/sidera-adapter-console$exe
    build "$goos" "$goarch" "sidera-adapter-$adapter" "$binary"
    "$work/host/sidera-adapter-$adapter$hostexe" package \
      --goos "$goos" --goarch "$goarch" --binary "$binary" --out "$stage/adapters/$adapter"
  done

  cp LICENSE "$stage/LICENSE"
  cp "$work/THIRD_PARTY_NOTICES" "$stage/THIRD_PARTY_NOTICES"
  cat >"$stage/README.txt" <<README
Sidera $version ($goos/$goarch)

sidera                   the program: the daemon and the command line of agents
sidera-adapter-console   the console adapter, which is also the owner's command-line
                         client
adapters/                the packages of the adapters, ready for 'sidera adapter
                         install' (each holds its own program)
LICENSE                  the licence of Sidera
THIRD_PARTY_NOTICES      the licences of the software the programs are built from

Install guide:
https://github.com/aianyai/sidera/blob/v$version/docs/guide/install.md
README

  # The archive has no directory around its files. COPYFILE_DISABLE keeps macOS tar
  # from adding AppleDouble entries (._name) for the attributes of the build machine.
  files+=(adapters LICENSE THIRD_PARTY_NOTICES README.txt)
  if [ "$goos" = windows ]; then
    (cd "$stage" && zip -qr "$outdir/$name.zip" "${files[@]}")
  else
    COPYFILE_DISABLE=1 tar "${tar_options[@]}" -czf "$outdir/$name.tar.gz" -C "$stage" "${files[@]}"
  fi
done

(cd "$outdir" && sha256 sidera_"$version"_* >checksums.txt)
echo "release $version written to $outdir"
