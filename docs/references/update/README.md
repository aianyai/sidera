# update

`sidera update` brings one installation to a release: the programs next to the running
one, the daemon and the installed adapters. This topic is the rules of that run and the
reasons for them. The code is in
[`cmd/sidera/internal/update`](../../../cmd/sidera/internal/update/README.md) and its four
packages (`fetch`, `verify`, `unpack`, `swap`), the command in
[`internal/cli`](../../../cmd/sidera/internal/cli/README.md); the user's view is the
[operations guide](../../guide/operations.md#upgrade). The signing side (the key, the
signature file, the trusted comment) is written in [ci](../ci/README.md).

`update` is the only thing in Sidera that contacts GitHub or any release server, and only
while it runs. The daemon never does; it reaches its peers, which may be across the internet,
and nothing else.

## What a run is

```
sidera update [--check] [--version <version>] [--yes] [--home <directory>]
```

Without `--version` it takes the latest release; `--version` accepts `0.12.0` and `v0.12.0`
and may name a pre-release. It asks `Update? [y/N]` before changing anything, unless
`--yes`; with no terminal and no `--yes` it refuses before any request (`--check` needs
neither). One run manages one data directory: `--home`, then `SIDERA_HOME`, then the default,
as for every command. The programs do not belong to a data directory, so another node on the
same machine needs `sidera update --home <directory>` too, which then only restarts its
daemon and installs its adapters.

`--check` downloads `checksums.txt` and its signature (about a kilobyte), verifies them and
writes nothing. It always exits 0 once it has read a release it can believe; `--json` gives
`update_available` (the target is newer than the running program), `admitted` (the signed
floor allows it) and `refusal`, the first reason a run would stop, if any. A release that
cannot be believed, or a network that fails, is an error like any other. A check looks at
the release and the running program only: it does not read the daemon or the adapters, so it
does not say whether an earlier run left them behind, and its report has no `program`,
`daemon` or `adapters` member. `update` without `--check` finishes those.

### The run converges

Every run looks at three things and does only the step that is missing, so a run that was
cut short by a crash, a power cut or Ctrl-C is finished by running again:

1. **The programs.** `sidera`, and `sidera-adapter-console` when it is next to it, are
   replaced when the running program's version is not the target.
2. **The daemon.** A daemon that runs and is older than the target is restarted with
   `daemon restart --expect-version <target>`, which refuses, leaving the daemon up, unless
   the program it would start reports the target. The run then waits for the new daemon
   (the same code as `sidera daemon restart`). With no daemon running, nothing is
   restarted and the run says so.
3. **The adapters.** Each installed adapter that is older than the target is installed from
   the release's package with `adapters.install`, in place: nothing is disabled, the
   instances restart and keep their sessions. An installed adapter the release has no
   package of is left as it is and reported. So is one that is not the release's to replace,
   found before anything is downloaded: its version is newer than the target (update never
   goes back), or is not a release version (a third-party or self-built adapter). Neither
   keeps a node from being current. A third-party adapter with a release-shaped version older
   than the target cannot be told from the release's by its version, so such a run downloads
   the archive and then reports it as not in the release.

The order is the order of the dependencies. The adapters come last because a package is
installed only into a daemon that reads it: an old daemon decodes a new manifest strictly
and may refuse it. With no daemon running the adapters cannot be listed or installed, so the
run replaces the programs and tells the owner to start the daemon and run `update` again;
that second run replaces nothing and installs the adapters. A run with nothing to do (all
three at the target, the adapters having been looked at) says `already_current` and downloads
nothing. With no daemon running it never says so, because the adapters were not looked at.

The version of the running program decides step 1, and the replacement puts the running
program last (see Replacing the programs): a replacement cut between two programs leaves the
running one old, so the next run replaces the set again.

The run goes on in the old program's process after the programs are replaced; it does not
hand over to the new one. A new release's extra steps are therefore found by the next run,
not the current one.

### Refusals

Every refusal is made before anything is changed, with the way forward in the message.

| Refusal | Why |
| ------- | --- |
| The build is not a release archive's (`distribution` is not `archive`, or the checkout was modified) | Only the programs of an archive are replaced. A development build is rebuilt from source; any other build is updated the way it was installed |
| The target is older than the running program | A newer version may have written what an older one cannot read; going back is a manual decision (see the operations guide) |
| The running version is below the release's signed floor | The release declares the oldest version that may update to it; update in steps |
| The program or its directory is a symbolic link | The replacement would change another place than the one shown (for example a package manager's link) |
| The program or its directory is not owned by the user running `update` | A run under `sudo` would put root's files into a user's directory. Message: do not run update with sudo. Not checked on Windows: a file created by an elevated process is owned by the Administrators group, not by the user, so an owner comparison would refuse an installation made from an elevated shell in every later session, and the trap it guards, files the user then cannot use, does not exist there (what `update` creates takes the access list of its directory, so the user keeps full control) |
| The daemon runs a version newer than the target | A newer version may have written what an older one cannot read, and `update` does not restart a daemon into an older release; found before anything is changed. A daemon whose version is not a release version is refused too |
| The cache directory belongs to another user, or others can enter it (Unix) | What is downloaded there is checked once and used later; a directory someone else can write, such as one in a cache directory shared between users, proves nothing |
| The directory cannot be written | Found by creating and removing a file before the download |
| The release cannot be believed | See the trust chain |
| The unpacked program does not report the distribution `archive` (a release built before the stamp existed, 0.11.0 and earlier, does not) | Found by running it, after the download and the checks and before anything changes. Installing a program without the stamp would leave an installation that can no longer update itself, so such a release is never a target |
| Another `update` is running | See Cache and lock |

The same version is not a downgrade: a run at the target version replaces no program and
converges the daemon and the adapters.

## Channels

How the files of a release reach the machine depends on how Sidera was installed. The run
has that as one step selected by the build's `distribution` (`sidera version --json`). The
only channel built is the release archive, stamped by `scripts/release.sh`; a build
without a stamp, such as `go build` or `go install`, is refused with its own message. The
part that makes the node follow (restart the daemon, install the adapters) does not depend
on the channel, so a channel that does not replace the programs itself (a package manager)
plugs in at the first step, by supplying the files, and leaves the others as they are.

## The trust chain

Nothing the network delivers is believed because of where it came from. The chain, from the
program's own bytes:

1. **The key** is built into the program (`internal/release/minisign.pub`, parsed by
   `verify.EmbeddedKeys`). That function is the only place that says which keys count; no
   flag or environment variable reaches it. A test supplies a throwaway key through the
   run's configuration.
2. **The signature file** `checksums.txt.minisig` is a minisign signature of `checksums.txt`
   (`internal/minisign`: only the prehashed `ED` form; the legacy form is refused). Its
   trusted comment is signed too.
3. **The trusted comment** is exactly `sidera <version> update-from <floor>`. The version
   must be the version asked for, as text, so the signature of another release, however
   valid, cannot stand in for the one requested (a replay or a forced downgrade). The floor
   is a plain `MAJOR.MINOR.PATCH`.
4. **`checksums.txt`** is read as `scripts/release.sh` writes it or refused whole: each line
   `<64 lowercase hex>  <sidera_VERSION_OS_ARCH.tar.gz|zip>`, no line skipped, no name twice.
5. **The archive's SHA-256** must equal the line for this platform. The archive is written
   to a file, that file is checked and that same file is unpacked, so what is believed is what
   is used.
6. **The unpacked program is run** as `version --json`; it must report the target version and
   the distribution `archive`.

Build metadata in any version (`1.2.3+x`) is refused where a version is read, because the
semantic-version parser would drop it silently. `ErrNotTrusted` (anything that stops the bytes
from being believed) and `ErrNotAllowed` (a genuine release this installation is too old for)
are different refusals; there is no way around the first. Details and tests:
[`verify`](../../../cmd/sidera/internal/update/verify/README.md).

## Reaching GitHub

[`fetch`](../../../cmd/sidera/internal/update/fetch/README.md) is the only code that does.
Finding the latest release reads the 302 of `<repository>/releases/latest` without following
it; only `<repository>/releases/tag/v<version>` with a release version is accepted. A
download follows at most 5 redirects, each over HTTPS, to any host: GitHub serves assets
from a storage host that has changed before, and the signature, not the host, makes a file
authentic. The proxy is read from `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY`. Every request
has a deadline that covers the body (one minute; fifteen for the archive) and a size limit
(`checksums.txt` 64 KiB, the signature 4 KiB, the archive 256 MiB). Requests carry no
credential and a `User-Agent` of `sidera/<version> (<os>/<arch>)`: the version and platform
are in the archive's name anyway. Nothing about the daemon, the node or the machine is sent.

## Opening the archive

[`unpack`](../../../cmd/sidera/internal/update/unpack/README.md) treats an archive as hostile
even after its digest matched. It extracts only regular files and directories into an empty
directory, and refuses an absolute or non-plain path (`..`, `.`, empty component, backslash,
colon, NUL), any link, device or other member, a name that occurs twice, a file over 128 MiB,
files over 512 MiB together, more than 1024 members, and a member whose real size differs
from the declared one. Modes are not copied: an executable becomes `0755`, a file `0644`.

## Replacing the programs

[`swap`](../../../cmd/sidera/internal/update/swap/README.md) replaces the set all or none:

- Every new program is written as `<target>.new` beside its target (under a temporary name,
  synced, renamed), then, for each target in order, the current file is renamed
  `<target>.old` and `<target>.new` is renamed to the target. Windows allows renaming a
  running program but not overwriting it, so the same order is used on every system and the
  platform that cannot do otherwise is the one tested.
- The first failing step undoes every rename made, newest first. If the undo fails too, the
  error names where each previous program is and how to restore it (absolute paths).
- After success the `.old` files stay, the way back, until the next update that replaces the
  programs (`Clean` removes them). New programs are mode `0700`.
- **Crash states.** Each rename is atomic, the sequence is not:

  | Crash | Targets | Leftovers | Repair |
  | ----- | ------- | --------- | ------ |
  | During `Prepare` | all old | `.new` files, perhaps a `.tmp-*` | `Clean` removes the `.new` files; a `.tmp-*` by hand |
  | Before the first rename of `Commit` | all old | `.new` for each | `Clean` |
  | Between the two renames of one target | that target missing | `<target>.old` holds the previous program, `<target>.new` the new | `Clean` renames the `.old` back, removes the `.new` |
  | Between the renames of two targets | the first new, the second old | `.old` of the first, `.new` of the second | `Clean` removes both; the next update installs the missing version |

  A power cut is the same, with the added chance that renames not yet durable are lost.
  The window between the two renames of one target is microseconds on Unix and is accepted:
  the visible result is `sidera: command not found`, repaired by renaming `sidera.old` back
  (`sidera.exe.old` on Windows).
  On Windows each rename is retried for up to about two seconds, so the window is wider.
- `Clean` runs before every replacement; it puts a missing program back before it removes
  anything and never removes the only copy. On Windows a `.old` that a process still runs
  from (a `sidera mcp` helper of an agent session) cannot be removed but can be renamed;
  `Clean` renames it to `<target>.old-<n>` so the name `.old` is free, and lists it; it is
  removed by a later `Clean`.
- **What the caller must guarantee.** One writer (the update lock). No interrupt during
  `Commit`: the run blocks SIGINT, SIGTERM and SIGHUP (an upgrade over SSH gets a hangup
  when the connection drops), and Ctrl-C of the console on Windows, for its duration, and
  reports a signal that arrived once the replacement is complete or undone.
  `Clean` before `Prepare`, with the same targets.

The running program is replaced last, console program first; when `sidera-adapter-console`
is not next to the program it is not created and the report says so. Only a program in the
directory of the running one is replaced; the adapters' own programs come with their
packages.

## Cache and lock

Releases are downloaded into `<user cache directory>/sidera-update/<version>/`
(`os.UserCacheDir`: `~/Library/Caches` on macOS, `$XDG_CACHE_HOME` or `~/.cache` on Linux,
`%LocalAppData%` on Windows). The directory name is not under the data directory on any
system: on Windows `UserCacheDir` is `%LocalAppData%`, which holds the default data directory
`%LocalAppData%\Sidera`, and the daemon refuses a data directory that others can reach. The
version directory is created owner-only (`fsx.MakeRoot`), because the archive is checked once
and unpacked after: if someone else could replace it in between, the check would prove
nothing. It is wiped before every download, trusting nothing an earlier run left, and removed
when the run ends, whether it succeeded or not (the lock file beside it stays); the adapters are installed from it while it
exists (the daemon copies the package itself).

A run holds `sidera-update/update.lock` (an `fsx` lock, released by the system if the process
dies) from before it writes anything to the end. The file stays after the run and the next run
locks it again. A second run fails at once, naming the lock.
Everything before the lock only reads: the release is found, the downgrade and the place of
the programs are checked. A run that is refused there, such as one under `sudo` (which on
macOS keeps the user's home), creates neither the cache directory nor the lock, so it leaves
no file of root's where the next run of the user would meet it. Once created, the cache
directory is refused unless it is owned by the caller and, on Unix, closed to the group and
to others.

## What the management interface must keep

The run that replaced the programs goes on as the old program, talking to a daemon that has
just become the new version; a new `update` talks to the daemon of an older release. So the
four methods `update` calls must read the same across versions:

| Method | What update relies on |
| ------ | --------------------- |
| `status` | `node.version`; `daemon.state` and `daemon.start_id` (for the wait) |
| `daemon.restart` | param `expect_version`; result `restarting`, `program`, `version`, `start_id` |
| `adapters.list` | `adapters[].id` and `adapters[].version` |
| `adapters.install` | param `source` (an absolute path); result `adapter.id`, `adapter.version`, `replaced`, `already_current` |

A later version may add members; it does not rename, remove or change the meaning of these.
The command decodes the answers loosely (a member it does not know is ignored), so adding one
breaks nothing. A change that cannot keep this is a change to the update procedure, made
before it ships.

## Where

`cmd/sidera/internal/update` (the run, `Run` and `Check`); its subpackages; the command and
the management client in `cmd/sidera/internal/cli/update.go`. Tests: the run against a local
TLS server with a throwaway key and fake archives, a temporary program directory and a fake
daemon (`update_test.go`); each subpackage by its own; the command's arguments, questions,
output and mapping of the management methods (`cli/update_test.go`). Replacing the running
daemon's programs, exec and the platform behaviour of a real release are checked on real
machines before a release, not by the unit tests.
