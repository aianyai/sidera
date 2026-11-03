# verify

The chain that lets `sidera update` believe a release: the public key built into the
program, the minisign signature of `checksums.txt`, the trusted comment that signature
covers, the lines of `checksums.txt`, and the SHA-256 of one archive. It decides whether a
release is genuine and whether this installation may take it. It does no network or file
I/O: callers hand it bytes, and what it has not verified it does not return.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `EmbeddedKeys()` | The keys releases are trusted by: `release.SigningKey()` parsed by `internal/minisign`. The only place that says which keys count |
| `Verify(keys, checksums, minisig, want)` | Checks signature, trusted comment, version binding and the lines of `checksums.txt`; returns a `Release` |
| `Release.Version()`, `Release.Floor()` | The version the signature is for and the oldest version that may update to it. Read only: the fields are not exported, so no caller can lower the floor |
| `Release.Admits(installed)` | Whether an installation at `installed` may take the release (the floor) |
| `Release.Archive(goos, goarch)` | The archive name for the platform and its expected SHA-256 |
| `VerifyDigest(r, sum)` | Streams `r` through SHA-256 and compares |
| `ErrNotTrusted`, `ErrNotAllowed`, `ErrNoArchive`, `ErrInvalidVersion` | What `errors.Is` finds in a refusal |

`keys` is a parameter of `Verify` so that a test can sign with a throwaway key (real release
archives are too large for testdata, so a caller's tests inject one too); the production
caller passes `EmbeddedKeys()` and nothing else. No flag or environment variable reaches it,
and an empty list trusts nothing.

## What the caller must do

- **Unpack exactly the bytes that were hashed.** Write the download to a file in a private
  directory while hashing it, run `VerifyDigest` on that file, and unpack that same file.
  Fetching the archive a second time by its path, or reading it from a directory another
  process can write to, would unpack bytes nobody verified.
- **Refuse a downgrade or the same version.** `Admits` compares the installation with the
  floor only; whether the release is newer than what is installed is the caller's, by
  comparing `Release.Version()` with the installed version.
- **Tell a bad version from a bad release.** `ErrInvalidVersion` is about text the caller
  passed in; the version asked for may come from the network, so the caller classifies the
  error by where the text came from.

## Rules

- **Not trustworthy and not allowed are different refusals.** `ErrNotTrusted` is anything
  that stops the bytes from being believed: a signature that does not verify or is by another
  key, a signature for another version, a trusted comment or `checksums.txt` that is not in
  the form the pipeline writes, an archive whose digest differs. `ErrNotAllowed` is a genuine
  release that this installation is too old to take. A caller must not offer a way around the
  first; the second is fixed by updating in steps.
- **The trusted comment is exactly `sidera <version> update-from <floor>`.** Four fields
  separated by single spaces; the version is a release version without `v`, the floor a plain
  `MAJOR.MINOR.PATCH` (no pre-release, no leading zeros). The grammar is written by
  `scripts/release-sign.sh`, which takes it from `scripts/lib/release-format.sh`; shell
  cannot share this package's code, so `releasescripts_test.go` runs that file's functions
  against this package and requires the same answers (see Tests). The signed files of the real 0.11.0
  release in `testdata/release-0.11.0` are a static snapshot: they show that this package
  reads what the pipeline produced at 0.11.0, and they do not change when the pipeline does,
  so a later change of either side is not caught by them.
- **Version binding.** The comment's version must be the version asked for, as text. The
  signature of an older or newer release is valid for its own checksums and is refused for any
  other, so a replayed old release cannot stand in for the one requested. A pre-release is a
  different version from its release.
- **Build metadata is refused** wherever a version is read (asked for, installed, in the
  comment, as the floor), before `semver.Parse`, which would drop it: a version with a
  suffix that is ignored is not the version that was signed.
- **The floor is by SemVer precedence, pre-releases included.** `0.8.0-rc.1` is below the
  floor `0.8.0`, as are development builds (`0.0.0-dev`). `Admits` compares the installed
  version with the floor only. Whether the release is newer than what is installed
  (downgrade, same version) is not decided here: the caller knows both versions and owns that
  rule, and it is a statement about the request, not about the release.
- **`checksums.txt` is read as `scripts/release.sh` writes it, or refused whole.** Every line
  is `<64 lowercase hex digits>  <name>` (two spaces) ending in a newline alone; there is no
  blank line, comment, carriage return, extra column or path, and the name is
  `sidera_<version>_<os>_<arch>.<tar.gz|zip>` of exactly the verified version for a platform
  the releases are built for (`.zip` for Windows). A name may occur once. A line is never
  skipped, so the rule applied to a line is the signer's (`check_checksums` in
  `scripts/lib/release-format.sh`). The release need not list every
  platform; `Archive` reports a missing one as `ErrNoArchive`.
- **The signature covers `checksums.txt`, not the archives.** An archive is believed only
  when `VerifyDigest` of its bytes equals the sum `Archive` returned. The digest is computed
  by streaming; a read error is returned as it is and is not a mismatch.
- **The zero `Release` is safe.** It was never verified: `Archive` reports `ErrNoArchive` and
  `Admits` fails closed with `ErrNotTrusted`, not as a version problem.

## Tests

`testdata/release-0.11.0/` holds the `checksums.txt` and `checksums.txt.minisig` of the real
0.11.0 release; they verify with the committed key. `testdata/throwaway/` holds public key
`throwaway.pub` and about fifty cases signed by it with the real `minisign` tool for what the
real release cannot show: every deviation of the trusted comment and of a `checksums.txt`
line that must be refused, one at a time. `generate.sh` there makes them (the secret key
lives only for the run). The table in `verify_test.go` names the part of the message each
refusal must mention; the tampering of the signature itself is `internal/minisign`'s tests,
and here one case shows that such a failure reaches the caller as `ErrNotTrusted`. The floor
rule is tried in `admits_test.go` on Releases made inside the package.

`releasescripts_test.go` is what keeps the release scripts and this package one grammar. It
runs `is_release_version`, `is_release_floor` and `check_checksums` of
`scripts/lib/release-format.sh` with `bash` over a table of versions (leading zeros, empty
identifiers, nine-digit and 64-character limits, build metadata, non-ASCII), the floors of
the comment fixtures and every `sums-*` fixture, and requires that the shell accepts
exactly what `Verify` accepts. A new deviation the verifier refuses belongs in the table or
in `generate.sh`. The test is skipped where `bash` is not installed.
