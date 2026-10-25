# ipc

How the Agent and management interfaces are reached on this machine, and how
each side makes sure it is talking to the current user. Read
[../../README.md](../../README.md) first for the conventions of `internal/`, and
[../../../docs/references/local-ipc/README.md](../../../docs/references/local-ipc/README.md)
for the reasons behind the rules below.

This is a runtime package: it creates sockets and pipes. It starts no goroutine
of its own (`Accept` runs in the caller's; on Windows go-winio's listener runs
one until `Close`), reads no clock and uses no randomness. It may touch the
file system itself, through `net` and `os`, because it owns the socket files and
the checks on their directory; it is an exception to the rule that runtime
packages touch files only through `fsx`. The checks read the socket directory
and the socket with `os.Lstat` because they need what `fsx.Info` deliberately
does not express: whether a path is a symbolic link, whether it is a socket,
and its exact permission bits and owner, for a single user.

## Entry points

| Function / type                                    | Purpose                                                                                                   |
| -------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `Locate(home, kind) (Address, error)`              | Where the interface lives: a socket path (Unix) or a pipe name (Windows). The one path-length check lives here. |
| `Listen(addr, lock, opts) (net.Listener, error)`   | Serve the interface. Needs the caller's assertion that the instance lock is held.                        |
| `Dial(ctx, addr, opts) (net.Conn, error)`          | Connect, after checking the way to the server and the server itself.                                      |
| `InstanceLock`                                     | The assertion Listen asks for: `Held() bool`. A precondition, not a security boundary.                    |
| `ErrDaemonNotRunning`, `ErrPeerCheckFailed`, `ErrLockNotHeld` | Sentinel errors, matched with `errors.Is`.                                                      |
| `*PeerCheckError`, `Failure`                       | Which check failed (a stable string), on what path, with the system error. Matches `ErrPeerCheckFailed`.  |
| `*PathTooLongError`                                | The path, its length and the platform's limit.                                                            |

`Kind` is `KindAgent` or `KindManage`. The Agent socket is `<home>/run/agent.sock`,
the management socket `<home>/manage/manage.sock`, and on Windows the pipes are
`\\.\pipe\sidera-<h>-agent` and `\\.\pipe\sidera-<h>-manage`.

## Rules

- **One path check.** `Locate` is the only way to get an `Address` and the only
  place that checks the socket path against the platform limit (103 bytes on
  macOS, 107 on Linux, absolute path, without the NUL). `init`, daemon start and
  client connect all go through it. The zero `Address` is refused by `Listen`
  and `Dial`.
- **Unix, listener.** The socket directory must already exist, be a real
  directory, be owned by the user and have mode exactly 0700; otherwise `Listen`
  fails. A leftover socket is deleted only if it is a socket owned by the user,
  and only because the caller holds the instance lock. Anything else at the path
  is left alone and reported. The socket is then chmod 0600 (not `umask`, which
  is process-wide). Every accepted connection is checked with the kernel's peer
  credentials (`LOCAL_PEERCRED` on macOS, `SO_PEERCRED` on Linux); a connection
  from another uid, or whose credentials cannot be read, is closed and logged
  and `Accept` goes on waiting. The returned listener has only `Accept`,
  `Close` and `Addr`: `AcceptUnix`, `File` and `SetUnlinkOnClose` of the
  underlying listener are deliberately not reachable, because they would accept
  or hand out the descriptor without the credential check.
- **Unix, client.** Before connecting: the directory is a real directory (not a
  symbolic link), owned by the user, mode exactly 0700; the socket is a socket
  (not a link) owned by the user. After connecting: the server's credentials
  must be the user's. A failed check returns a `*PeerCheckError` and nothing is
  sent. A missing directory or socket, or `ECONNREFUSED`, is
  `ErrDaemonNotRunning` instead, and the two never match each other. `EACCES`
  on connect is a failed check (`access_denied`), not "not running": the user's
  own socket always lets the user connect, so a refusal means someone else holds
  the name.
- **Windows, listener.** The pipe is created as the first instance of its name,
  so a second listener on the name fails. Its security descriptor is
  `O:<SID>D:P(A;;GA;;;<SID>)` for the user's own SID, never the owner-rights
  shorthand. Remote clients are rejected. There is no peer check on accept: the
  descriptor lets only the user open the pipe. There is no leftover to delete.
- **Windows, client.** After opening the pipe the client makes two checks, in
  this order, and any step that fails fails the check (fail closed), with one
  `Failure` per step. First the owner of the pipe, read from the client's handle
  (`GetSecurityInfo`), must be the user's SID (`pipe_owner`). This is the check
  that holds: the server process id the system reports can be stale, since a
  process can hand the pipe handle to another and exit and its id can then be
  reused by a process an attacker controls, whereas an unprivileged process
  cannot make another user the owner of an object it creates, and the
  listener's descriptor names the user as owner explicitly, elevated or not.
  Second, the server process read from the handle (`GetNamedPipeServerProcessId`)
  must have the user's `TokenUser`. Refusal of access when opening the pipe is a
  failed check (`access_denied`): the daemon's pipe always grants the user.
- **Fail closed.** A nil or released `InstanceLock`, an empty data directory, an
  unknown `Kind` and an unreadable owner are errors, never defaults. The zero
  `Options` is valid and logs nothing.
- **Test seams.** `Options` has three unexported fields that only tests set:
  `files` (`lstater`, a `Lstat` of a path below the data directory, to present a
  directory another user owns), `peerUID` (the peer's uid on Unix) and
  `pipeServer` (the pipe's owner and server user on Windows). Nil means the real
  calls; production code never sets them.
- Logs and errors name paths, uids, SIDs and process ids; they never contain
  request data or tokens.

## What the caller must guarantee

- `init` creates `run/` and `manage/` with mode 0700 through `fsx`; `Listen`
  checks them and never creates or repairs them.
- The daemon holds `daemon.lock` before it calls `Listen` and passes a value
  whose `Held` reports it. `Listen` cannot verify this: the value is the
  caller's assertion, and it only catches ordering mistakes in the composition
  root.
- Only a connection accepted by the `KindManage` listener may be mapped to
  `admin.LocalOwner()`; a connection accepted by the agent listener never may.
  The management interface identifies the owner by the user's uid alone, so
  keeping an agent off it is the sandbox's job (see the reference).
- `Dial` callers pass a context with a deadline: a busy Windows pipe is retried
  until the context ends.
- A caller maps `ErrDaemonNotRunning` to `unavailable`/`daemon_not_running` and
  `ErrPeerCheckFailed` to `unavailable`/`peer_check_failed`, with the error text
  as the message.

## Files

| File                                          | Contents                                                              |
| --------------------------------------------- | --------------------------------------------------------------------- |
| `address.go`, `errors.go`, `lock.go`, `options.go` | Shared types.                                                    |
| `pathlen.go`, `pipename.go`                   | Pure rules (path limit, pipe name, descriptor), tested everywhere.    |
| `listen.go`, `dial.go`                        | Platform-independent entry points.                                    |
| `*_unix.go`, `peer_darwin.go`, `peer_linux.go`, `limit_*.go` | Unix and macOS/Linux specifics.                        |
| `*_windows.go`                                | Windows specifics: pipes, SID, pipe owner and server token check.     |

## Follow-ups

- The Windows and Linux tests pass in the CI jobs of each platform; the reference
  lists the items that stay untested, pending verification by CI on main or not
  exercised by CI.
