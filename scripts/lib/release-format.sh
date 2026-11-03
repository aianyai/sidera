# shellcheck shell=bash
# What a release is, for the scripts that make one. Sourced, not run:
#
#   . "$root/scripts/lib/release-format.sh"
#
# The program that updates itself reads a release with sdk/go/semver and with the
# verifier in cmd/sidera/internal/update/verify, and refuses what they refuse. A release
# built, signed and published under a version or a checksums.txt they refuse could never
# be installed by anyone, so the scripts accept exactly what they accept, and these
# functions are the one place the scripts say so. cmd/sidera/internal/update/verify tests
# every function here against the Go side, row by row.

# The release_* patterns are POSIX extended regular expressions (bash's [[ =~ ]]).
# A number is a version's major, minor or patch: no leading zero, at most nine digits.
release_number='(0|[1-9][0-9]{0,8})'
# A pre-release identifier is a number without a leading zero, or a non-empty run of
# letters, digits and hyphens with at least one letter or hyphen. Numbers in it have no
# length limit of their own.
release_identifier='(0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)'
# release_core is MAJOR.MINOR.PATCH.
release_core="$release_number"'\.'"$release_number"'\.'"$release_number"
# The longest version text, as sdk/go/semver.MaxLen.
release_version_max_length=64

# is_release_version <text>: a semantic version 2.0.0 without a leading "v" and without
# build metadata, at most release_version_max_length characters.
is_release_version() {
  # The ranges of the patterns are ASCII; some locales order letters otherwise.
  local LC_ALL=C
  [ "${#1}" -le "$release_version_max_length" ] &&
    [[ $1 =~ ^$release_core(-$release_identifier(\.$release_identifier)*)?$ ]]
}

# is_release_floor <text>: a release version that has no pre-release part, which is what
# the floor of a release is.
is_release_floor() {
  local LC_ALL=C
  [[ $1 =~ ^$release_core$ ]]
}

# check_checksums <file> <version>: the checksums.txt that scripts/release.sh writes for
# the release version <version>, which must be valid. Each line is
# "<64 lowercase hex digits>  <archive>" with two spaces and a newline alone at its end,
# and the archive is one the release is built for: sidera_<version>_<os>_<arch>.tar.gz,
# .zip for Windows. No line may be blank, a comment or have anything more, and an
# archive occurs once. A release need not list every platform. On a refusal it says why
# on standard error and returns 1.
check_checksums() {
  # The ranges of the patterns are ASCII; some locales order letters otherwise.
  local LC_ALL=C
  local file=$1 version=$2 line name goos goarch ext
  local archives=() known found seen="" number=0
  local line_pattern='^[0-9a-f]{64}  [^ ]+$'
  if [ ! -s "$file" ]; then
    echo "$file is empty" >&2
    return 1
  fi
  if grep -q $'\r' "$file"; then
    echo "$file has a carriage return; lines end with a newline only" >&2
    return 1
  fi
  # The substitution drops the newlines at the end, so it is empty when the last byte is one.
  if [ -n "$(tail -c 1 "$file")" ]; then
    echo "$file: the last line does not end with a newline" >&2
    return 1
  fi
  for goos in darwin linux windows; do
    ext=tar.gz
    [ "$goos" = windows ] && ext=zip
    for goarch in amd64 arm64; do
      archives+=("sidera_${version}_${goos}_${goarch}.$ext")
    done
  done
  while IFS= read -r line; do
    number=$((number + 1))
    if ! [[ $line =~ $line_pattern ]]; then
      echo "$file line $number is not <64 lowercase hex digits>  <name>: $line" >&2
      return 1
    fi
    name=${line#*  }
    found=false
    for known in "${archives[@]}"; do
      [ "$name" = "$known" ] && found=true
    done
    if ! $found; then
      echo "$file line $number is not an archive of version $version: $name" >&2
      return 1
    fi
    case $seen in
      *"|$name|"*) echo "$file line $number lists $name twice" >&2; return 1 ;;
    esac
    seen="$seen|$name|"
  done <"$file"
}
