# release

The release version, set once at build time and read by every shipped program:
`sidera` and each adapter report the same version. The adapter protocol has its
own version (v1) and does not follow this one.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `Version()` | The version of the running program: the release version, or `DevVersion` |
| `Read()` | `Info`: the version, the distribution, the abbreviated commit and whether the checkout was modified (from the module's build information), and the Go version |
| `DistributionArchive` | `archive`, the distribution of the programs in the release archives |
| `SigningKey()` | The content of `minisign.pub`, the public key the releases are signed with, as a copy the caller may change |
| `ParseVersion(text)` | Reads a release version into a `semver.Version`, or `ErrInvalidVersion`: the one Go home of what a release version is |
| `Info.Describe(program)` | The one line `version` prints, `sidera 0.18.0 (commit 369c94e9f1a2, go1.26.10)` |
| `DevVersion` | `0.0.0-dev`, what a build without the release version reports |

## Rules

- The release version arrives only through the linker:
  `-ldflags "-X github.com/aianyai/sidera/internal/release.stamped=<version>"`,
  with no leading `v`. `scripts/release.sh` stamps every program it builds.
- The distribution is stamped the same way, `-X ...release.stampedDistribution=archive`,
  by `scripts/release.sh` for the programs in its archives, which are the ones
  `sidera update` replaces. It is empty for every other build (a plain `go build`, a
  package manager's build): `Info.Distribution` is then left out of `version --json`,
  and a program that does not say it comes from an archive is never replaced by
  an update. `scripts/release.sh` checks that the built `sidera` reports it, as it
  checks the version.
- The commit comes from the module's build information, which Go fills in only
  for a build made in a repository's own checkout (a build in a linked git
  worktree records the commit of the main checkout instead). A build that knows
  better states it, `-X ...release.stampedCommit=<commit>` and
  `-X ...release.stampedModified=true` for a tree with uncommitted changes, and
  the stated commit wins; `scripts/release.sh` does so.
- A release version is a SemVer 2.0.0 version as `sdk/go/semver` reads it, without
  a leading `v` and without build metadata. `ParseVersion` is the only Go code that
  says so: `sidera update` (`fetch`, `verify`) calls it, and
  `scripts/lib/release-format.sh` writes the same rule in shell for the release
  scripts, held to it by a test in `cmd/sidera/internal/update/verify`. Build
  metadata is refused before `semver.Parse`, which would drop it. Adapter manifest
  versions are not release versions and allow build metadata; they are read with
  `semver` directly. `sdk/go/semver` is standard library only, so this adds nothing to
  the adapters beyond what the SDK already brings.
- A build without it reports `DevVersion`, which is valid SemVer (adapters write
  their version into a manifest and answer it in `initialize`) and sorts before
  every release, so a development build cannot be taken for one. It is
  `0.0.0-dev`, not the next release's number, so that nothing has to be bumped
  after a release.
- This package is the one exception to "no global state" of the contract
  packages: the linker needs a package variable to write. It is never assigned in
  code.
- Adapters under `cmd/` otherwise import only `sdk/go`; they import this package
  for the version alone, because a version that every program of a release
  reports has to have one home, and the SDK is a published module that must not
  carry the repository's release number. An adapter's manifest and its answer to
  `initialize` both read `Version()`, so a released package carries the release
  version.
- `minisign.pub` is the public key the releases are signed with, a minisign
  `.pub` file, kept here as the one copy: `scripts/release-sign.sh` verifies
  its signature against it, the install guides quote it, and `SigningKey()`
  returns it, embedded with `go:embed` (an embed cannot reach outside the package
  directory, so it is made here). The bytes are not parsed in this package: the
  adapters import it, and the minisign parser and its `golang.org/x/crypto`
  dependency must not reach them. The signature and how it is made are in
  `docs/references/ci/README.md`, "Signature".
