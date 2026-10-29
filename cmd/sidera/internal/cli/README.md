# cli

`Run(context.Context, argv, Config)` is the executable's dispatcher; `argv`
includes the executable name. The owner supplies standard streams, an environment
lookup and a version. The dispatcher returns an exit code and never exits the
process itself. It borrows the streams, owns and closes the Agent client, and
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
- `local.go`: initialization and foreground daemon composition.
- `crashtest*.go`, `testclock*.go`: independently tagged process controls, their
  explicit environment inputs, and ordinary-build defaults.
- `manage.go`: management arguments, expected versions and one retained request
  for a single resend after an unknown outcome.
- `manage_output.go`: complete JSON responses and human settings and lists.
- `export.go`: verified online export, the precise missing-daemon offline
  fallback, and writing a new private copy through `fsx.CreatePrivateFile`.
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
with the existing client and build version. A non-empty probe credential enables
the MCP package's automatic connection probe; ordinary credentials stay separate.

Management commands parse their inputs once, read an omitted collection version
once, and choose an operation identifier once. Only a change with an unknown
outcome is sent again, at most once, with the same request. A version conflict is
reported to the caller. `--json` writes the complete response, preserving
`operation_id` and `replayed`; human change output also lists those fields.
A human unknown-outcome error prints the retained identifier from its error
details. Human adapter-enable output explains that probing continues in the
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
`SIDERA_CRASH_AT` and `SIDERA_CRASH_RECORD`. The seed selects a locked ChaCha8
reader derived with a SHA-256 domain containing the seed, command role
(`init`, `daemon`, `cli`) and positive generation (default 1). Fixtures supply
call-specific seeds; the fake runtime already derives `<base>/<native-ref>/<n>`
for its CLI calls. Empty seeds retain system randomness. The command opens the
record with `O_APPEND`, installs the daemon's observer explicitly, and closes the
record after completion. Each process owns a separate record file. Invalid
explicit generation or target values fail before the command runs; a target
requires a record. A receive reaches `proc/cli.recv-after-response` after the
complete HTTP page arrives and before its first stdout write.

Only a `testclock` build reads `SIDERA_TEST_CLOCK_START_MS`, a signed Unix
millisecond origin. It injects one fake clock into the daemon and adapter host.
An absent origin leaves system time in place. Ordinary builds ignore every test
variable and install neither a crash observer nor a process hook.
