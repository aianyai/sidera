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
`run/` is granted to agents, to connect only (see Sandboxes and the
socket directory below). The instance lock is `<home>/daemon.lock`, at the
root, because `state/` can be deleted as a whole and a lock inside it would
allow a second daemon while it is gone.

### Unix

- `run/` and `manage/` are owned by the user with mode 0700; sockets are 0600.
  The listener checks the directory and never creates it. After `listen` the
  socket is chmod 0600; between the two calls the 0700 directory already keeps
  everyone else out.
- The daemon deletes a leftover socket only while it holds `daemon.lock`, and
  only if it is a socket owned by the user. The caller asserts that it holds the
  lock; the assertion is a precondition that catches ordering mistakes in the
  composition root, not a security boundary.
- The listener reads the peer's uid for every accepted connection (macOS
  `LOCAL_PEERCRED`, Linux `SO_PEERCRED`) and closes a connection whose uid is not
  its own, or whose credentials cannot be read. The listener it returns has only
  `Accept`, `Close` and `Addr`; the underlying listener's `AcceptUnix`, `File` and
  `SetUnlinkOnClose` are not reachable, because they would accept or hand out the
  descriptor without that check.
- The client, before connecting: the directory is a real directory (not a
  link), owned by the user, mode exactly 0700; the socket is a real socket (not
  a link), owned by the user. After connecting it reads the server's
  credentials and requires the user's uid. Nothing is sent when a check fails.
  `EACCES` on connect is a failed check (`access_denied`), not "not running": the
  user's own socket lets the user connect, so a refusal means someone else holds
  the name.
- Linux abstract-namespace sockets are not used: they are not protected by file
  permissions.

#### Socket path length

The absolute path in bytes must not exceed 103 on macOS and 107 on Linux. The
numbers: macOS `sun_path` is 104 bytes and a 104-byte path was measured to fail
with "invalid argument", so 103 is the longest that works; Linux `sun_path` is
108 bytes including the NUL (the Linux limit is not exercised by CI, that is, no
CI job observes it: the tests check the length rule, none binds a socket on a
path of that length). Some other Unix implementations are as short as 92 bytes;
they are not supported. `Locate` is the one check, shared by `init`,
daemon start and client connect, and its error names the path, its length and
the limit. Leave room for a long user name and for a `SIDERA_HOME` override.

Sandboxes that allow connecting to the socket match the resolved path:
Codex normalizes the allowed path (`/tmp` becomes `/private/tmp`), Claude Code
matches by sub-path. Allow the canonical path.

On Linux the directory must be searchable and writable by the user and the
socket writable by the connecting process (unix(7)); both hold for the owner.

#### Sandboxes and the socket directory

A sandbox may allow connecting to `agent.sock`; it must not allow writing to or
deleting in `run/`. An agent in the sandbox runs as the daemon's own user, so
every check this document describes (the directory's owner and mode, the
socket's owner, the peer's uid) holds for it as well. With `run/` writable it
could remove `agent.sock`, listen on the name in its place, and collect the
tokens that other sessions send to what they take for the daemon. The client's
checks cannot tell the two apart, because both have the user's uid. The
directory permission of the sandbox is therefore the only thing that keeps the
name, and it has to be read-only for the agent (connect only).

### Windows

Established by the Windows tests listed under Where, which pass on the
GitHub-hosted `windows-latest` runner. What they do not establish is listed under
Known untested items.

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
- Client: after opening the pipe it makes two checks, in this order, and any
  failing step refuses the connection (fail closed). First it reads the owner of
  the pipe from its own handle (`GetSecurityInfo`, owner only) and requires it
  to be its own SID. Second it reads the server process id from the handle,
  opens the process and its token, and requires the token's `TokenUser` to equal
  its own. Together they stop another local user who created the pipe while the
  daemon was down from collecting the token a client sends. A refusal of access
  when opening the pipe is a failed check too (`access_denied`): the daemon's
  pipe always grants the user, so a refusal means someone else created the name.
- A pipe is a kernel object that disappears with its last handle, so there is no
  leftover to delete.
- The same holds as on Unix: a process of the same user, including an agent in a
  sandbox, passes the owner and token checks. While the daemon is not running it
  can create the pipe under the daemon's name and collect the tokens that
  clients send; the sandbox must not let an agent create pipes under that name.

### The management interface and the local owner

A connection accepted on the management interface is the local owner
(`admin.LocalOwner()`), and a connection accepted on the agent interface never
is. The management interface identifies the owner by the user's uid (the user's
SID on Windows) alone: it uses no token, so any process of the same user outside
a sandbox is the local owner, and so is any process inside a sandbox that is
allowed to reach the management endpoint. A sandbox therefore must deny
connecting to `<home>/manage/manage.sock` on Unix and opening the management
pipe `\\.\pipe\sidera-<h>-manage` on Windows. On Windows both pipes carry the
same security descriptor, so only the sandbox can tell them apart, by name; on
Unix the two sockets are in different directories, so the sandbox grants one
directory and not the other (see Sandboxes and the socket directory above).

## Why

- **Directory check on the client, not only 0600 on the socket.** The socket's
  mode is not enough for a client: another user who can write in a parent
  directory could replace the socket, and the client would send its token to
  it. A directory that only the user can write, owned by the user, closes that.
  The time between these checks and the connect, and a writable directory above
  the data directory, are covered by the credential check after the connect: it
  reads the process that actually accepted, whatever the paths looked like a
  moment before.
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
- **Client check of the pipe owner, then of the server's token.** Nothing
  prevents another user from creating the pipe first while the daemon is down;
  only the client can notice. The owner is the check that holds. The server
  process id that `GetNamedPipeServerProcessId` reports can be stale: a process
  can hand the pipe handle to another process and exit, and its id can be reused
  by a process the attacker controls, a window the attacker chooses (the id
  reflects the process that opened the pipe, as shown in Project Zero's 2019
  "Windows Exploitation Tricks: Spoofing Named Pipe Client PID"). An
  unprivileged process cannot make another user's SID the owner of an object it
  creates (Microsoft, "Owner of a new object"), and .NET's
  `PipeOptions.CurrentUserOnly` validates the pipe's owner the same way. The
  listener's descriptor sets `O:<user SID>` explicitly, so the owner is the user
  whether or not the daemon is elevated. The token user of the reported server
  process stays as the second line.
- **AF_UNIX on Windows rejected.** It would be one mechanism on every platform,
  but it supports only stream sockets, no ancillary data and no socketpair,
  needs the old file deleted before binding, its path limit is unconfirmed, and
  there is no peer credential call to match the checks above. Named pipes give
  the descriptor and the server-process query.
- **Why go-winio and `golang.org/x/sys`.** Named pipes need overlapped I/O,
  per-instance accept and security descriptors, which is too much to write
  safely by hand. go-winio v0.6.4 (MIT license, which allows bundling and
  redistribution with its notice; actively maintained by Microsoft; widely used
  by container tooling) is the latest release. The packages we import
  (`go-winio`, `internal/fs`, `internal/socket`, `internal/stringbuffer`,
  `pkg/guid`) depend only on `golang.org/x/sys`; go-winio's module graph also
  lists logrus (used only by its ETW logging subpackage) and the modules
  `x/mod`, `x/sync` and `x/tools` (build tooling), none of which is linked into
  our binaries (checked with `GOOS=windows go list -deps ./...`).
  `golang.org/x/sys` supplies the peer credential calls and the Windows
  security calls. go-winio and x/sys require `go 1.26.0`, which is why the root
  module's Go line and toolchain line are as they are.
- **`os.Lstat` for the directory checks, behind a small test seam.** The checks
  need to know whether a path is a symbolic link, whether it is a socket, its
  exact permission bits and its owner, for a single user. `fsx.Info`
  deliberately does not express those, so `ipc` reads the socket directory and
  the socket with `os.Lstat` itself; it is the one runtime package besides
  `fsx` allowed to touch files, because it owns the socket files. The checks
  read through a one-method interface (`lstater`, `Lstat` of a path below the
  data directory) only so that tests can present a directory owned by another
  user, which a test cannot create; nothing in production sets it.

## Errors

| Error                   | Meaning                                                               | Reported as                           |
| ----------------------- | --------------------------------------------------------------------- | ------------------------------------- |
| `ErrDaemonNotRunning`   | Socket or directory absent, `ECONNREFUSED`, pipe not found            | `unavailable`/`daemon_not_running`    |
| `*PeerCheckError`       | A check failed; carries the `Failure`, the path and the system error  | `unavailable`/`peer_check_failed`     |
| `*PathTooLongError`     | The socket path exceeds the platform limit                            | `init`, start and connect each report it |
| `ErrLockNotHeld`        | `Listen` without the caller's assertion that it holds the instance lock | programming error                   |

The `Failure` values of a `*PeerCheckError` are stable strings:

| Failure                                                                             | Check that failed                                                                  |
| ----------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `directory_missing`, `directory_not_directory`, `directory_symlink`, `directory_owner`, `directory_mode` | The socket directory (Unix): exists, is a real directory, owner, mode 0700. |
| `socket_missing`, `socket_not_socket`, `socket_owner`                               | The socket file (Unix): exists, is a socket, owner.                                |
| `stat_failed`                                                                       | A file could not be inspected for another reason.                                  |
| `server_credentials`, `server_uid`                                                  | The process serving the socket (Unix): credentials unreadable, or not the user.    |
| `access_denied`                                                                     | The system refused the user access to the socket (`EACCES`) or pipe (`ERROR_ACCESS_DENIED`): someone else holds the name. |
| `own_sid`                                                                           | The client could not determine its own SID (Windows).                              |
| `pipe_owner`                                                                        | The pipe's owner is unreadable or is not the user (Windows).                       |
| `server_process_id`, `server_process_open`, `server_token_open`, `server_token_user`, `server_sid` | The server process of the pipe (Windows): one value per step, and `server_sid` when its user is not ours. |
| `server_query`                                                                      | Reading the pipe's owner and server failed in a way no step above names (Windows). |

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
  `TestDialReportsDaemonNotRunning`,
  `TestClientReportsASocketTheSystemRefusesAsAFailedCheck`,
  `TestListenerDoesNotExposeAnUncheckedAccept`.
- Tests (every platform, pure rules): `TestSocketPathLengthLimit`,
  `TestPipeHashKnownAnswers`, `TestPipeSDDLGrantsOnlyTheUser`,
  `TestCanonicalPipeHomeIsAbsoluteCleanAndLowerCase`.
- Tests (Windows, run by the Windows CI jobs, all of them passing; the one
  test that needs a second local user is skipped, see Known untested items):
  `TestSecondListenerOnSamePipeNameFails`, `TestTwoConsecutiveConnectionsSucceed`,
  `TestConnectionsMayOverlap`, `TestClientCanReadTheServerProcessAndItsTokenUser`,
  `TestPipeIsOwnedByAndGrantedToTheUserOnly`, `TestPipeNameFollowsUserSIDAndDataDirectory`,
  `TestDialReportsDaemonNotRunning`, `TestCheckAcceptsThePipeOfTheCurrentUser`,
  `TestClientRefusesAPipeOwnedByAnotherUser`,
  `TestClientRefusesAServerRunningAsAnotherUser`,
  `TestClientFailsClosedWhenThePipeServerCannotBeRead`,
  `TestClientReportsAPipeThatDeniesTheUserAsAFailedCheck`,
  `TestOpenErrorClassification`. The owner and server-user checks run on a real
  pipe of the user and, for the refusals, with the owner and server user
  presented through an unexported seam of `Options`.

## Known untested items

"Pending verification by CI on main" marks what a CI job can observe and does not
yet; "not exercised by CI" marks what no CI job can observe, with the reason.

- **Windows measurements:** the Windows tests print what they measure (process
  id, `TokenUser`, security descriptor), but CI runs `go test` without `-v`, so
  those values are not in its log; pending verification by CI on main. Running the
  tests with `-v` on a Windows machine shows them.
- **Remote pipe clients and junction spellings on Windows:** that the listener
  rejects remote clients, and that two spellings of one directory through a
  junction give two pipe names, follow from go-winio's behavior and from the
  hash rule; no test connects remotely or through a junction.
- A listener's error code when the name exists: not asserted, the requirement is
  only that the second listener fails.
- Whether go-winio accepts further instances under this descriptor (`GA`
  includes the right to create pipe instances) is covered only by the Windows
  test of consecutive connections.
- Whether `GetNamedPipeServerProcessId` accepts a client handle, whether
  `GetSecurityInfo` reads the owner from a client handle, and whether the owner
  under elevation is the user: asserted by Windows tests that pass on the
  `windows-latest` runner (`TestClientCanReadTheServerProcessAndItsTokenUser`,
  `TestPipeIsOwnedByAndGrantedToTheUserOnly`); whether that runner's process is
  elevated is not recorded, so the elevated case is not established by them. The
  owner is the first line of the client check and the server token the second; if
  the token step is refused in common elevated and unelevated combinations, that
  is a fix to make then, because the owner check already holds on its own.
- **Cross-user takeover is not tested on any platform:** a single-user CI can
  prove that the owner can connect and that a same-name listener fails, not
  that another user is refused. `TestClientRefusesAPipeServedByAnotherUser`
  is skipped for that reason. On Unix another user's directory or socket is
  presented by a test double, not by a second account.
- An elevated daemon with an unelevated client on Windows (`TOKEN_QUERY` of the
  elevated process's token).
- Process id reuse between reading the server's id and opening the process: the
  server id can be stale (a process can hand the pipe handle to another and
  exit, and its id can be reused), so the token of that process is not the
  decisive check; the pipe's owner is, and it does not depend on a process id.
- The Linux 107-byte limit: that the kernel accepts a 107-byte socket path and
  refuses a 108-byte one is not exercised by CI;
  `TestLocateAcceptsALongestFittingPath` builds a path of that length, but only
  `Locate` sees it, nothing binds it. Linux `SO_PEERCRED` and `EACCES` on connect
  to a socket without write permission are exercised on the Linux CI runner
  (`TestPeerUIDOfARealConnectionIsTheCurrentUser`,
  `TestClientReportsASocketTheSystemRefusesAsAFailedCheck`). macOS behavior is
  verified locally.
- The cross-user claims of the owner check (an unprivileged process cannot make
  another user the owner of a pipe it creates) rest on Microsoft's documentation
  and on how .NET validates the same property; no test creates a pipe as another
  user.
