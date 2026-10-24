# ipc

How the Agent and management interfaces are reached on this machine, and how
each side makes sure it is talking to the current user. Read
[../../README.md](../../README.md) first for the conventions of `internal/`, and
[../../../docs/references/local-ipc/README.md](../../../docs/references/local-ipc/README.md)
for the reasons behind the rules below.

This is a runtime package: it creates sockets and pipes. It starts no goroutine
of its own (`Accept` runs in the caller's), reads no clock and uses no
randomness. It may touch the file system itself, through `net` and `os`, because
it owns the socket files and the checks on their directory; it is an
exception to the rule that runtime packages touch files only through `fsx`.

## Entry points

| Function / type                                    | Purpose                                                                                                   |
| -------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `Locate(home, kind) (Address, error)`              | Where the interface lives: a socket path (Unix) or a pipe name (Windows). The one path-length check lives here. |
| `Listen(addr, lock, opts) (net.Listener, error)`   | Serve the interface. Needs proof that the instance lock is held.                                          |
| `Dial(ctx, addr, opts) (net.Conn, error)`          | Connect, after checking the way to the server and the server itself.                                      |
| `InstanceLock`                                     | The proof Listen asks for: `Held() bool`.                                                                 |
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
  and `Accept` goes on waiting.
- **Unix, client.** Before connecting: the directory is a real directory (not a
  symbolic link), owned by the user, mode exactly 0700; the socket is a socket
  (not a link) owned by the user. After connecting: the server's credentials
  must be the user's. A failed check returns a `*PeerCheckError` and nothing is
  sent. A missing directory or socket, or `ECONNREFUSED`, is
  `ErrDaemonNotRunning` instead, and the two never match each other.
- **Windows, listener.** The pipe is created as the first instance of its name,
  so a second listener on the name fails. Its security descriptor is
  `O:<SID>D:P(A;;GA;;;<SID>)` for the user's own SID, never the owner-rights
  shorthand. Remote clients are rejected. There is no peer check on accept: the
  descriptor lets only the user open the pipe. There is no leftover to delete.
- **Windows, client.** After opening the pipe the client reads the server
  process from its own handle (`GetNamedPipeServerProcessId`), opens that
  process's token and compares `TokenUser` with its own. Any step that fails
  fails the check (fail closed), with one `Failure` per step.
- **Fail closed.** A nil or released `InstanceLock`, an empty data directory, an
  unknown `Kind` and an unreadable owner are errors, never defaults. The zero
  `Options` is valid and logs nothing.
- Logs and errors name paths, uids, SIDs and process ids; they never contain
  request data or tokens.

## What the caller must guarantee

- `init` creates `run/` and `manage/` with mode 0700 through `fsx`; `Listen`
  checks them and never creates or repairs them.
- The daemon holds `daemon.lock` before it calls `Listen` and passes a value
  whose `Held` reports it.
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
| `*_windows.go`                                | Windows specifics: pipes, SID, server token check.                    |

## Follow-ups

- The directory and socket checks read the file system through a small local
  interface (`lstater`: `Lstat` of a path relative to the data directory). Wire
  it to `fsx.Reader` once that offers `Lstat` with the owner of the file.
- Windows and Linux behavior is pending verification by CI on main; see the
  reference for the items that stay untested.
