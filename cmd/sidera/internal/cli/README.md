# cli

`Run(context.Context, argv, Config)` is the executable's dispatcher; `argv`
includes the executable name. The owner supplies standard streams, an environment
lookup, the path of the running program (`Executable`), what the program was built from (`release.Info`) and
how it was started (`Launch`, recorded by `main`, or `LaunchErr`). The dispatcher returns an exit code and never exits the
process itself, with one exception: after a foreground daemon that was asked to
restart has finished and everything is closed, it calls `Launch.Replace`, which on
Unix replaces the process and returns only on failure (`error: the daemon stopped but
could not start <program> again: …`, exit 1) and on Windows returns once a detached
process runs. It borrows the streams, owns and closes the Agent client, and
waits for a foreground daemon to finish before returning. For `mcp`, it passes
stdin/stdout ownership to the MCP peer and borrows stderr for diagnostics. MCP
process failures always use stderr, including with `--json`; tool and RPC failures
remain protocol messages.

Global flags are removed before dispatch. Command discovery registers the local
flags once; global scanning reads their arity through `FlagSet.Lookup` and the
standard-library `IsBoolFlag` convention, then local parsing uses that same set
and its bound values. A local value stays attached, including a body spelled
`--json`. Parsing accepts flags after positional arguments and honors `--`.

- `arguments.go`: command discovery and registration before global scanning.
- `cli.go`: global parsing, dispatch, receive and acknowledgement arguments.
- `message.go`: message and control flags; one body source for send and reply.
- `query.go`: identity, declaration patch presence, directory, peers and probe.
- `output.go`: human results; structured results use the shared JSON encoder.
- `local.go`: initialization (with its `--port` flag, judged by
  `config.ValidatePort`) and foreground daemon composition, including the daemon's
  `Relauncher`: it refuses a restart when `LaunchErr` is set or `launch.Restartable`
  says no (wrapping `daemon.ErrRestartUnavailable`) and otherwise probes the program
  within its own 20 seconds, the daemon's context ending only with the daemon.
- `restart.go`: `daemon restart`. `restartDaemon` is the one place that sends
  `daemon.restart` and waits for the new daemon, whoever asks; the wait reads `status`
  until a `start_id` other than the old one runs, treating a daemon that is gone, going
  or starting (and a cut-off read) as "not yet" and any other failure as the answer.
- `update.go`: `update`. It builds the run's configuration from the process (the built-in
  key, the real GitHub client, the user cache directory, the program as it was started),
  asks the confirmation (the plan to the output, the answer from stdin; to standard error
  with `--json`), writes the report, and implements the run's `Node` over the management
  client, opened at its first call so that a run that stops early never needs the data
  directory. It reads the answers loosely. `terminal*.go` say whether stdin is a terminal,
  which a run without `--yes` needs.
- `crashtest*.go`, `testclock*.go`: independently tagged process controls, their
  explicit environment inputs, and ordinary-build defaults.
- `manage.go`: management arguments, expected versions and one retained request
  for a single resend after an unknown outcome (`callResending`); the commands `network address set|remove`
  and `network listen set` have three words, which `commandPath` discovers.
- `manage_output.go`: complete JSON responses and human settings and lists; a `contact`
  line of a result is printed first, on a line of its own, so that it can be copied.
  `statusWithHome` adds `home`, the directory the command resolved, to a node status, which
  the daemon does not report.
- `trust.go`: `trust request` (the answer printed with its code first, then the client-side wait
  that polls `trust.list`; a refusal by the target is an error of category `refused`, a wait that
  ends otherwise is a plain failure, exit 1) and the human `trust list`. The other `trust`
  commands use the common management path in `manage.go`.
- `export.go`: verified online export, the precise missing-daemon offline
  fallback, and writing a new private copy through `fsx.CreatePrivateFile`.
- `setup.go`: `setup controller`, which prints the commands that add the `sidera` MCP server to
  Claude Code and Codex, and the permission each client needs to run its tools without a
  prompt, and does nothing else. The server is named by the absolute path of the running
  program (`Config.Executable`): a client starts it without a shell and may not have the
  program's directory on its `PATH`. A data directory that is not the default is named in the
  commands by `--home`: the client's environment for the server may not carry `SIDERA_HOME`. A
  program in a temporary directory (a `go run` build) is warned about on standard error: its path
  would not last. The clients' files are named under `CLAUDE_CONFIG_DIR` and `CODEX_HOME` when the
  environment sets them.
- `controller.go`: the attachment of an agent command run inside a session a person
  opened (the `controller` package does the work; the command gives its Agent client the
  token and prints the wake notice on stderr only when the adapter cannot wake the
  session). The attachment is made by the command's first call to the node, after its
  arguments are parsed and checked, so a command refused for them attaches nothing. It
  carries the wake keys but is not repeated between commands: the endpoint is closed a
  queue time after the last command when nothing waits on it, and the wake values the
  node holds in memory are lost when it restarts until the next command.
- `help.go`: usage, purpose and example for every command.

Agent flags are assembled into presence-preserving objects for `Client.Call`;
explicit empty strings and zeroes reach the existing interface validation rather
than disappearing through typed optional fields. Business rules stay in the
Agent interface and engine. The dispatcher sends
comma-separated `send --to` values as a list so the Agent boundary alone owns
the one-recipient rule. A receive writes the client's page unchanged, adds no
newline and does not acknowledge it. Errors use `apiwire` and its exit table;
daemon failures retain `daemon.ExitCode`, including fatal storage code 74.
The `mcp` command parses its empty local flag set, then calls `mcp.Run` directly
with the existing client and build version, and with the management client and the
environment when the process holds no token (controller mode). A non-empty probe credential enables
the MCP package's automatic connection probe; ordinary credentials stay separate.

Management commands parse their inputs once, read an omitted collection version
once, and choose an operation identifier once. Only a change with an unknown
outcome is sent again, at most once, with the same request. A version conflict is
reported to the caller. `--json` writes the complete response, preserving
`operation_id` and `replayed`; human change output also lists those fields.
A human unknown-outcome error prints the retained identifier from its error
details. A human `network listen set` says that a restart is needed. Human adapter-enable output explains that probing continues in the
background and directs the owner to `sidera adapter list`.

With `--out`, JSON export reports only the digest and supplied file path under
`result`; the armored statement stays out of stdout. Without `--out`, JSON keeps
the complete response. Online and offline export use the same management result
type.

An export's armor is parsed only by `keys.ParseRevocationExport`, and its verified
digest must match the response before an output file is created. The shared
private-file boundary owns exclusive creation and platform permission checks;
the command owns writing, syncing, closing, and cleanup after that returns a
file. Cleanup is registered only after creation succeeds, including for returned
stdout/stderr errors. Existing names are never removed. Local file failures
keep the OS explanation, the stdout alternative when privacy cannot be verified,
and any cleanup failure in the error message. Only these local failures use
`exportFileFailure`; remote errors and armor or digest validation never enter
that wrapper and retain the shared error mapping.

Only a `crashtest` build reads `SIDERA_TEST_SEED`, `SIDERA_CRASH_GEN`,
`SIDERA_CRASH_AT` and `SIDERA_CRASH_RECORD`. A nonempty seed selects two independently
locked ChaCha8 readers, directly derived with SHA-256 from distinct fixed purpose
namespaces plus the seed, command role (`init`, `daemon`, `cli`) and positive
generation (default 1). The semantic reader retains the `sidera-test-random-v1`
namespace; the temporary-name reader uses `sidera-test-temp-random-v1`. Local
commands pass the latter as `daemon.Config.TempRand`, so asynchronous filesystem
work cannot change subsequent identifiers by consuming their random stream. Fixtures supply
call-specific seeds; the fake runtime already derives `<base>/<native-ref>/<n>`
for its CLI calls. Empty seeds retain system randomness and leave `TempRand` nil;
ordinary builds do the same without reading these test controls. The command opens the
record with `O_APPEND`, installs the daemon's observer explicitly, and closes the
record after completion. Each process owns a separate record file. The daemon
role reads its record, target and generation together, then immediately removes
those three crash variables
from its process environment before validation or startup. Adapter processes,
native runtimes and their CLI tools therefore inherit none of the daemon
generation's controls. Seed and clock variables remain available, and separately
configured CLI invocations still accept their own crash controls. Invalid
explicit generation or target values fail before the command runs; a target
requires a record. A receive reaches `proc/cli.recv-after-response` after the
complete HTTP page arrives and before its first stdout write.

Only a `testclock` build reads `SIDERA_TEST_CLOCK_START_MS`, a signed Unix
millisecond origin. It injects one fake clock into the daemon and adapter host.
An absent origin leaves system time in place. Ordinary builds ignore every test
variable and install neither a crash observer nor a process hook.

`TestDaemonCrashEnvironmentStaysWithItsOwner` performs the environment mutation
only in a helper process. It launches the real fake adapter through the host's
exec launcher, then a native runtime whose exec action starts the tagged CLI.
The native CLI must succeed without appending to the daemon's record; a CLI
with explicitly supplied controls must still reach its own crash target.
