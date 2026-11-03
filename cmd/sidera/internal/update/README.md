# update

`sidera update`: the converging run that brings the programs, the daemon and the
installed adapters to one release, and the packages that do its parts. The reference
(`docs/references/update/README.md`) has the trust chain, the rules and the reasons;
this is the map.

| Directory | What it does |
| --------- | ------------ |
| [`fetch`](fetch/README.md) | Reaches GitHub: the latest release, one asset of a release. Checks nothing it returns |
| [`verify`](verify/README.md) | Believes a release: signature, trusted comment, version binding, floor, `checksums.txt`, archive digest |
| [`unpack`](unpack/README.md) | Opens a `.tar.gz` or `.zip` into an empty directory, refusing everything but plain files and directories |
| [`swap`](swap/README.md) | Replaces the programs in one directory all or none, and repairs what a cut short run left |
| this package | The run: what is needed, the refusals, the cache and lock, the order of the steps |

## Entry points

- `Run(ctx, cfg, want)` does the update and returns a `Report`, even when it fails, so
  the caller can say what had been done. `Check(ctx, cfg, want)` only reads the release
  and returns a `Verdict`, which says nothing of the daemon or the adapters because a
  check does not look at them. `want` is a release version without `v` (`NormalizeVersion` accepts both
  spellings) or empty for the latest.
- `Config` is everything a run needs. The keys releases are believed by, the
  GitHub client and the daemon are fields, so a test supplies a throwaway key, a local
  TLS server and a fake node; the command passes the built-in key and nothing a user can
  set reaches them.
- `Node` is the daemon as the run needs it (status, restart, adapters, install). The
  command implements it over the management client; this package imports no
  management code.

## Files

- `update.go`: the types (`Config`, `Node`, `Plan`, `Report`) and `NormalizeVersion`.
- `run.go`: `Run`, `Check`, the refusals, the node steps and the channel (how the
  files of a build are obtained; only a release archive is, other builds are refused
  with the way to update them).
- `archive.go`: the update lock, the cache directory, download, digest check, unpack
  and the smoke run of the unpacked program.
- `location.go`: which programs would be replaced and whether their place is
  acceptable (links, owner, writable). `owner_unix.go` and `owner_windows.go` say who
  owns a file and whether others can enter a directory; on Windows there is no owner
  check (the file explains why).
- `signals.go`: keeps an interrupt, a termination or a hangup from ending the
  process while the programs are being replaced.

## Rules the code keeps

- A run reads before it writes: the release, the downgrade and the place of the programs
  are checked before the cache directory and the lock are created, so a refused run leaves
  nothing behind. The cache directory is refused unless it is the caller's and closed to
  others.
- Nothing is changed before the release is verified, the archive's digest matched and
  its program ran as the release asked for; a refusal leaves every file as it was.
- The order of the steps is the order of the dependencies: programs, then the daemon
  (so that it runs the new version), then the adapters (a new package is installed
  only into a daemon that reads it).
- Each step is skipped when its result is already there, so a run that was cut short is
  finished by running again.
- The archive is written to a file, that file is checked against the signed digest and
  that same file is unpacked. The release's directory in the cache is owner-only and is wiped
  before every download and removed when the run ends; the lock file beside it stays.
- Tests: `harness_test.go` serves signed releases from a local TLS server, with a
  throwaway key and small archives, and `update_test.go` runs the whole run against a
  temporary program directory and a fake node. The real binary on the three systems is
  the acceptance's.
