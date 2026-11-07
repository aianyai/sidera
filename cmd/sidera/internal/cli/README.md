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

Global flags (`--home`, `--json`, `--color`, `--help`) are removed before dispatch by `readGlobals`. `--color`
sets `command.color`, and `readGlobals` builds the `out` and `errOut` `termout.Stream`s from it and the
environment, once per run and even when a flag is wrong, since the error is reported through them. They
are what a command's human output is written through (`Stdout` and `Stderr` stay the raw writers); the
agent commands, the management commands and the operations (`update`, `adapter setup`, `daemon restart`, `init`, `identity export-revocation`, `trust list`, `trust request` and the controller notice) write through them. Command discovery registers the local
flags once; global scanning reads their arity through `FlagSet.Lookup` and the
standard-library `IsBoolFlag` convention, then local parsing uses that same set
and its bound values. A local value stays attached, including a body spelled
`--json`. Parsing accepts flags after positional arguments and honors `--`.

- `arguments.go`: command discovery and registration before global scanning.
- `cli.go`: global parsing and dispatch. An agent command is dispatched in two steps: `agentRequest`
  (in `agent.go`) checks its arguments and builds its one call, and only then is the Agent client
  opened, so a usage error is reported before any call and a data directory or socket that cannot
  be used never hides it. `mcp` checks its arguments the same way before it opens the client.
  `setup` is not a command group: `run` answers every form of it with a usage error that names
  `adapter setup <instance>`, the command that replaced it, before any flag is read.
- `agent.go`: `agentRequest`, which turns the arguments of an agent command into its method,
  parameters and the way its answer is written; the receive, acknowledge and status arguments.
- `message.go`: message and control flags; one body source for send and reply.
- `query.go`: identity, declaration patch presence, directory, peers and probe.
- `output.go`: `result`, which writes the answer of an agent command (JSON with `--json`, the
  layouts below otherwise) through `c.out`; `jsonText` for the generic management listing.
- `agent_output.go`: the layouts of the agent commands (`me`, `declare`, `peers`, `directory`,
  `send`/`reply`/`control`, `ack`, `status <message-id>`), each a function from the stream and the
  result to text, so a test reads them without a process. They share one convention (see "Human
  output" in the command-line reference): a line that says what was done, blocks of labelled
  lines, tables with the identifier in the last column, protocol words written as the protocol
  names them, identifiers whole. A new field of an agent result is given its human form here when
  it is added. Text from outside that no source validates goes through `termout.SanitizeLine`
  before it is printed; the fields printed today are identifiers, enumerations and names checked
  where they enter the node.
- `ops_output.go`, `update_output.go`, `adapter_setup_output.go`: the layouts of the operations, in the
  convention of `agent_output.go`: `daemon restart`, `init`, the instruction of `identity export-revocation`,
  `trust list`, `trust request` and the controller notice (`ops_output.go`); the plan, the verdict of `--check` and
  the report of `update`; the plan, the questions, the notes and the report of `adapter setup`. The
  words an adapter's setup program writes are two kinds. The ids, the status of an outcome and the
  problem of a difference are shown as they are, because `adaptersetup.Result.Validate` (which
  `setupSession.call` applies) holds them to single words. The free text (the summary and target of
  an item, a message, a note, the summary of a difference) and the cause in the controller notice are
  not validated, so these layouts show them through `termout.SanitizeLine`, and `execSetup` shows the
  end of the standard error it quotes through `termout.SanitizeText` (the line breaks of a tail
  stay); a new place that prints such text does the same. An item of a setup
  plan is written by its summary alone: the adapter's `action` is the verb of that summary, and
  its `kind` is for `--json`.
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
  asks the confirmation (the plan to standard error, the answer from stdin), writes the
  report (layouts in `update_output.go`), and implements the run's `Node` over the management client, opened at its first
  call so that a run that stops early never needs the data directory. It reads the answers
  loosely. A run without `--yes` needs a terminal on stdin, which `termout.IsTerminal` says.
- `adapter_setup.go`: `adapter setup`. `openSetupSession` asks the daemon (`adapters.setup_info`) how the
  instance's setup runs and builds the one request every call of the command shares; `setupSession.call`
  runs the program (`execSetup`: argv exec'd directly, request on stdin, result on stdout, the tail of
  stderr quoted when it fails, five minutes at most) and checks the result with `Result.Validate`;
  `runSetup` is the modes (plan or remove, ask, apply; check; refresh) over a session, `setupDialog`
  the questions, written to the standard-error stream and read from one reader, and `applyInStages` the one place that
  honours an item's `requires` once the questions are answered (an item whose required item did not
  complete is not sent to the adapter). The executor and the dialog are the seams the
  tests use; the same session is what `update` refreshes the setups with (`managementNode.RefreshSetup` in
  `update.go` runs `runSetup` in refresh mode and puts the result in the update's words). `prompt.go` is the question that `update` and `adapter setup` share, one implementation with two defaults (`askDefaultNo`, `[y/N]`, for `update`; `askDefaultYes`, `[Y/n]`, for the items of `adapter setup`; the end of the input is a no for both), and the
  "no terminal, add --yes" refusal. `program.go` is `programPath`, the path
  of this program that `update` and `adapter setup` name, and the helpers that go with it: `shellWord`, `warnIfTemporary`, `isDefaultDataDir`. `refuseRemoveWhileSetUp` is the guard `manage` runs before
  `adapters.remove`: a `check` of the instance's setup, and a refusal when it has a record.
- `crashtest*.go`, `testclock*.go`: independently tagged process controls, their
  explicit environment inputs, and ordinary-build defaults.
- `manage.go`: management arguments, expected versions and one retained request
  for a single resend after an unknown outcome (`callResending`); the commands `network address set|remove`
  and `network listen set` have three words, which `commandPath` discovers.
- `manage_output.go`: `managementOutput`, which writes the answer of a management command: the
  complete response with `--json`, otherwise the layout of the command's method in `humanLayouts`.
  A method without a layout is an error that names it, never a dump of the answer, and
  `TestEveryManagementCommandHasALayout` walks the commands to find one added without. `read` and
  `change` make a layout from a function over the type that reads the answer; `change` adds the line
  that says a replayed request changed nothing this time. `managementResult` and the helpers under it
  (`humanFields`, `humanList`, `humanValue`) remain for one caller that is being restyled and are to be removed with it.
  `statusWithHome` adds `home`, the directory the command resolved, to a node status, which the
  daemon does not report.
- `manage_read_output.go`: the layouts of the management commands that only read (`status`,
  `node show`, `network show`, `preset list`, `adapter list`, `endpoint list`, `alerts list`), in the
  convention of `agent_output.go`. Each has a type that reads the daemon's answer, which is the one
  place the CLI names its fields, and a function from the stream and that type to text. A new
  field in one of these answers is given its human form here, or deliberately left to `--json`
  and listed in the comment at the top of the file: a type that does not name the field drops it
  from the human output without a sound, and no command falls back to printing the answer as JSON.
- `manage_change_output.go`: the layouts of the management commands that change something, in the
  same convention: the first line says what was done, under it come only the facts the owner needs
  from the answer, the last line says what to run next. A new field in an answer is given its human
  form here, or left to `--json`, and a type that does not name it drops it without a sound. The
  operation identifier and the replay flag are never printed here (`--json` has them); `change` adds
  the replay line. A new change command is added to `humanLayouts`.
- `trust.go`: `trust request` (the answer printed with its code first, then the client-side wait
  that polls `trust.list`; a refusal by the target is an error of category `refused`, a wait that
  ends otherwise is a plain failure, exit 1) and the human `trust list`. The other `trust`
  commands use the common management path in `manage.go`.
- `export.go`: verified online export, the precise missing-daemon offline
  fallback, and writing a new private copy through `fsx.CreatePrivateFile`.
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
comma-separated `send --to` values as a list, so the Agent boundary alone decides how many
recipients a message may have; it takes exactly one (`invalid_argument/single_recipient_only`,
exit 3), and a list is refused there. A receive writes the client's page unchanged, adds no
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
