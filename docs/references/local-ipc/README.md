# Local IPC

How the Agent interface and the management interface are reached on the same
machine, and what each side checks before it trusts the other. Code:
[`internal/node/ipc`](../../../internal/node/ipc/README.md).

## What

Two interfaces, each on its own local endpoint:

| Interface  | Unix                                  | Windows                          |
| ---------- | ------------------------------------- | -------------------------------- |
| Agent      | `<home>/run/agent.sock`               | `\\.\pipe\sidera-<h>-agent`      |
| Management | `<home>/manage/manage.sock`           | `\\.\pipe\sidera-<h>-manage`     |

`<home>` is the data directory, made absolute and clean. The management socket
has its own directory because sandboxes are granted access by directory and
`run/` is granted to agents. The instance lock is `<home>/daemon.lock`, at the
root, because `state/` can be deleted as a whole and a lock inside it would
allow a second daemon while it is gone.

### Unix

- `run/` and `manage/` are owned by the user with mode 0700; sockets are 0600.
  The listener checks the directory and never creates it. After `listen` the
  socket is chmod 0600; between the two calls the 0700 directory already keeps
  everyone else out.
- The daemon deletes a leftover socket only while it holds `daemon.lock`, and
  only if it is a socket owned by the user. The caller proves it holds the lock.
- The listener reads the peer's uid for every accepted connection (macOS
  `LOCAL_PEERCRED`, Linux `SO_PEERCRED`) and closes a connection whose uid is
  not its own, or whose credentials cannot be read.
- The client, before connecting: the directory is a real directory (not a
  link), owned by the user, mode exactly 0700; the socket is a real socket (not
  a link), owned by the user. After connecting it reads the server's
  credentials and requires the user's uid. Nothing is sent when a check fails.
- Linux abstract-namespace sockets are not used: they are not protected by file
  permissions.

#### Socket path length

The absolute path in bytes must not exceed 103 on macOS and 107 on Linux. The
numbers: macOS `sun_path` is 104 bytes and a 104-byte path was measured to fail
with "invalid argument", so 103 is the longest that works; Linux `sun_path` is
108 bytes including the NUL. Some other Unix implementations are as short as 92
bytes; they are not supported. `Locate` is the one check, shared by `init`,
daemon start and client connect, and its error names the path, its length and
the limit. Leave room for a long user name and for a `SIDERA_HOME` override.

Sandboxes that allow writes to the socket directory match the resolved path:
Codex normalizes the allowed path (`/tmp` becomes `/private/tmp`), Claude Code
matches by sub-path. Allow the canonical path.

On Linux the directory must be searchable and writable by the user and the
socket writable by the connecting process (unix(7)); both hold for the owner.

### Windows

*Pending verification by CI on main.*

- Name: `<h>` is the first 16 hex characters of
  `SHA-256(user SID string, 0x00, data directory)`, the directory absolute,
  clean and lower case (NTFS paths and pipe names are case-insensitive). Two
  spellings of one directory through a junction give two names, so the client
  cannot connect; that is visible and easy to fix, and no path resolution is
  done for it.
- Listener: go-winio `ListenPipe` with the security descriptor
  `O:<SID>D:P(A;;GA;;;<SID>)`. The first instance is created with
  `FILE_CREATE`, the NT form of `FILE_FLAG_FIRST_PIPE_INSTANCE`, so a name that
  already exists makes the listener fail. Remote clients are always rejected.
  The listener does not check who connects: the kernel enforces the descriptor
  when a client opens the pipe, which is the same level as a 0700 directory with
  a 0600 socket on Unix.
- Client: after opening the pipe it reads the server process id from its own
  handle, opens the process and its token, and requires the token's `TokenUser`
  to equal its own. Any failing step refuses the connection. This stops another
  local user who created the pipe while the daemon was down from collecting the
  token a client sends.
- A pipe is a kernel object that disappears with its last handle, so there is no
  leftover to delete.

## Why

- **Directory check on the client, not only 0600 on the socket.** The socket's
  mode is not enough for a client: another user who can write in a parent
  directory could replace the socket, and the client would send its token to
  it. A directory that only the user can write, owned by the user, closes that.
- **Credentials as well as permissions.** unix(7) says portable programs should
  not rely on socket permissions for security; unix(4) on macOS says the
  credentials mechanism is reliable and works in both directions. Both sides
  therefore check credentials in addition to the modes.
- **No `umask`.** It is process-wide and would change files that other
  goroutines create; the directory's mode covers the gap before `chmod`.
- **Explicit owner in the pipe descriptor.** A process started elevated may have
  the Administrators group as the default owner in its token, so the owner
  rights shorthand could mean the group. The user's SID, taken from the process
  token, is written out as owner and as the only allowed principal. SYSTEM and
  Administrators are not listed: the daemon serves only its own user.
- **The SID in the pipe name.** The pipe namespace is visible machine-wide. Two
  users who chose the same data directory string would otherwise fight over one
  name. The SID is not secret and adds no security; it removes the accident.
- **No server-side check on Windows.** The descriptor is enforced by the kernel
  at open time. The Unix listener checks credentials only because Linux
  documents socket permissions as unreliable; the pipe descriptor has no such
  reservation.
- **Client check on the server's token.** Nothing prevents another user from
  creating the pipe first while the daemon is down; only the client can notice.
- **AF_UNIX on Windows rejected.** It would be one mechanism on every platform,
  but it supports only stream sockets, no ancillary data and no socketpair,
  needs the old file deleted before binding, its path limit is unconfirmed, and
  there is no peer credential call to match the checks above. Named pipes give
  the descriptor and the server-process query.
- **Why go-winio and `golang.org/x/sys`.** Named pipes need overlapped I/O,
  per-instance accept and security descriptors, which is too much to write
  safely by hand. go-winio v0.6.4 (MIT license, which allows bundling and
  redistribution with its notice; actively maintained by Microsoft; widely used
  by container tooling; small, its dependencies are `x/sys`) is the latest
  release. `golang.org/x/sys` supplies the peer credential calls and the Windows
  security calls.
- **A subset interface for the directory checks.** The checks need only `Lstat`
  of paths below the data directory; they read through a small interface so
  tests can present a directory owned by another user, which a test cannot
  create. The real implementation uses `os.Lstat`; `ipc` is the one runtime
  package besides `fsx` allowed to touch files, because it owns the socket
  files.

## Errors

| Error                   | Meaning                                                               | Reported as                           |
| ----------------------- | --------------------------------------------------------------------- | ------------------------------------- |
| `ErrDaemonNotRunning`   | Socket or directory absent, `ECONNREFUSED`, pipe not found            | `unavailable`/`daemon_not_running`    |
| `*PeerCheckError`       | A check failed; carries the `Failure`, the path and the system error  | `unavailable`/`peer_check_failed`     |
| `*PathTooLongError`     | The socket path exceeds the platform limit                            | `init`, start and connect each report it |
| `ErrLockNotHeld`        | `Listen` without proof of the instance lock                           | programming error                     |

A failed check is never reported as "not running": it may mean the place was
taken by someone else, and sending the user to start a daemon would hide that.

## Where

- Code: `internal/node/ipc` (`locate_*.go`, `listen*.go`, `dial*.go`,
  `files_unix.go`, `peer_*.go`, `pipename.go`, `sid_windows.go`).
- Tests (Unix, run on macOS and Linux):
  `TestClientRefusesSocketDirectoryThatIsNotOwnerOnly0700`,
  `TestClientRefusesSocketDirectoryNotOwnedByUser`,
  `TestClientRefusesASocketThatIsNotTheUsers`,
  `TestClientRefusesAServerOfAnotherUser`,
  `TestListenerTurnsAwayAPeerThatIsNotTheUser`,
  `TestPeerUIDOfARealConnectionIsTheCurrentUser`,
  `TestListenRemovesAStaleSocketOnlyWhileHoldingTheLock`,
  `TestListenDoesNotDeleteWhatIsNotASocket`,
  `TestListenCreatesAnOwnerOnlySocket`, `TestLocateRefusesPathLongerThanThePlatformAllows`,
  `TestDialReportsDaemonNotRunning`.
- Tests (every platform, pure rules): `TestSocketPathLengthLimit`,
  `TestPipeHashKnownAnswers`, `TestPipeSDDLGrantsOnlyTheUser`,
  `TestCanonicalPipeHomeIsAbsoluteCleanAndLowerCase`.
- Tests (Windows, pending verification by CI on main):
  `TestSecondListenerOnSamePipeNameFails`, `TestTwoConsecutiveConnectionsSucceed`,
  `TestConnectionsMayOverlap`, `TestClientCanReadTheServerProcessAndItsTokenUser`,
  `TestPipeIsOwnedByAndGrantedToTheUserOnly`, `TestPipeNameFollowsUserSIDAndDataDirectory`.

## Known untested items

- **Windows, all of it:** pending verification by CI on main. The Windows
  tests print what they measure (process id, `TokenUser`, security descriptor).
- A listener's error code when the name exists: not asserted, the requirement is
  only that the second listener fails.
- Whether go-winio accepts further instances under this descriptor (`GA`
  includes the right to create pipe instances) is covered only by the Windows
  test of consecutive connections.
- Whether `GetNamedPipeServerProcessId` accepts a client handle, and whether the
  default owner under elevation is the user: asserted by Windows tests whose
  first result comes from CI. If the server-token step is refused in common
  elevated and unelevated combinations, the fallback is to read the owner of
  the pipe (`GetSecurityInfo` owner, which the descriptor already sets) as the
  second check; that is a fix to make then, not built twice now.
- **Cross-user takeover is not tested on any platform:** a single-user CI can
  prove that the owner can connect and that a same-name listener fails, not
  that another user is refused. `TestClientRefusesAPipeServedByAnotherUser`
  is skipped for that reason. On Unix another user's directory or socket is
  presented by a test double, not by a second account.
- An elevated daemon with an unelevated client on Windows (`TOKEN_QUERY` of the
  elevated process's token).
- Process id reuse between reading the server's id and opening the process: the
  window is tiny and undocumented; no special handling.
- Linux `SO_PEERCRED` and the 107-byte limit: pending verification by CI on
  main. macOS behavior is verified locally.
