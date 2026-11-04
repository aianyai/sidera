# launch

How this program was started, and how to start it again. `main` captures it first
of all (`Capture`), before the daemon changes the working directory, and hands it to
the dispatcher in `cli.Config`; a restart of the daemon is built on it.

- `launch.go`: `Launch` (the program path, the arguments as started, the start
  directory) and `Capture`.
- `probe.go`: `Probe`, a smoke run of the program as `sidera version --json`.
- `replace_unix.go`, `replace_windows.go`: `Restartable` and `Replace`.

Rules:

- **The program path is not resolved further than the system resolved it.** `argv[0]`
  is made absolute against the start directory, or looked up in `PATH` for a bare
  name, and symbolic links are kept: a link that a package manager keeps pointing at
  the current version starts the new version when it is started again. When `argv[0]`
  cannot be resolved or names another file than the running one, the path
  `os.Executable` reports is used; on Linux that has its links resolved.
- **`Probe` runs the program as `sidera`, never under the name it was started
  under.** The name `siderad` selects `daemon`, and `siderad version --json` would
  run `daemon version --json`. On Windows it is started without a console window,
  which a daemon that has no console would otherwise get for it. The output and the
  time are bounded; a program that fails, does not answer or prints no version is an
  error, so a restart is refused before anything is stopped.
- **`Replace` starts the program as it was started**: the same arguments, the
  environment this process has, the start directory. On Unix it is `execve` in place,
  so the process number, the terminal, the ignored signals and the standard streams
  stay, and it returns only on failure. On Windows nothing can be replaced in place:
  it starts a detached process through `sdk/go/adapterkit/process` and returns once
  that runs, and the caller exits. It refuses when `Restartable` says the job this
  process runs in would end the new process when it closes; `Restartable` is always
  true on Unix.
- The caller replaces only after everything the process held is released (the
  daemon's data lock, its logs and its sockets); the new process takes them over.

`Replace` has no test of its own: it is a call of `execve` or `process.Start`.
