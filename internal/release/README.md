# release

The release version, set once at build time and read by every shipped program:
`sidera` and each adapter report the same version. The adapter protocol has its
own version (v1) and does not follow this one.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `Version()` | The version of the running program: the release version, or `DevVersion` |
| `Read()` | `Info`: the version, the abbreviated commit and whether the checkout was modified (from the module's build information), and the Go version |
| `Info.Describe(program)` | The one line `version` prints, `sidera 0.10.0 (commit 369c94e9f1a2, go1.26.10)` |
| `DevVersion` | `0.0.0-dev`, what a build without the release version reports |

## Rules

- The release version arrives only through the linker:
  `-ldflags "-X github.com/aianyai/sidera/internal/release.stamped=<version>"`,
  with no leading `v`. `scripts/release.sh` stamps every program it builds.
- The commit comes from the module's build information, which Go fills in only
  for a build made in a repository's own checkout (a build in a linked git
  worktree records the commit of the main checkout instead). A build that knows
  better states it, `-X ...release.stampedCommit=<commit>` and
  `-X ...release.stampedModified=true` for a tree with uncommitted changes, and
  the stated commit wins; `scripts/release.sh` does so.
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
