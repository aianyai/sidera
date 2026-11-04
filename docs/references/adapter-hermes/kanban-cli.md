# Hermes Kanban client

How the Hermes adapter talks to the Kanban board of a Hermes Agent installation.
The adapter is a lane of that board: Hermes creates cards, the adapter reads and
completes them. All of it goes through the `hermes kanban` command line and its
`--json` output; the package is `cmd/sidera-adapter-hermes/internal/kanban`.

## What

### The rule: the command line, and only that

The client never opens `kanban.db` and never calls the dashboard's REST API. Both
are internals that Hermes changes between releases without notice; the command
line and its JSON are the surface that Hermes documents, that a version check can
pin and that recorded samples can guard.

Every call runs `hermes kanban --board <slug> <subcommand> ...` directly, without
a shell. `--board` is an option of the `kanban` parent, so it comes before the
subcommand; the board that Hermes calls current is never used. The child's
environment is the adapter's, with `HERMES_HOME` set explicitly (the adapter
inherits the daemon's service environment, not the owner's shell, and Hermes'
default home would be a different installation or profile), `HERMES_KANBAN_HOME`
set when configured, and every other `HERMES_KANBAN_*` variable removed except
`HERMES_KANBAN_BUSY_TIMEOUT_MS`: a task, run or board variable inherited from a
worker's environment would scope or redirect the call. Each call has the caller's
`context.Context` as its timeout and the child is killed when it ends.

### Commands used

Verified against Hermes 0.22.2 (`hermes_cli/kanban_parser.py` and
`hermes_cli/kanban.py`) and by real runs on an isolated profile and board.

| Method | Command | Output |
| ------ | ------- | ------ |
| `List` | `list --status=<s> --json` | JSON array of cards. A card has no parents here. |
| `Show` | `show <id> --json` | `task`, `latest_summary`, `parents`, `children`, `comments`, `events`, `runs` (events are not decoded). |
| `Create` | `create --json [--body-file -] [--assignee=] [--parent=]... [--idempotency-key=] [--created-by=] -- <title>` | The created card. The title is a required positional, after `--`; the body is standard input. A second create with the same idempotency key returns the first card as it was. |
| `Claim` | `claim <id> --ttl=<seconds>` | Text. |
| `Block` | `block <id> --kind=<kind> [-- <reason>]` | Text. Hermes writes the reason as a comment first and then tries the block, so a refused block can leave a `BLOCKED:` comment. |
| `Comment` | `comment <id> --author=<a> -- <text>` | Text. |
| `Complete` | `complete <id> [--summary=] [--result=] [--metadata=<json object>]` | Text. A completion with neither summary nor result is refused. Accepts a card in `running`, `ready`, `blocked` or `review`; a blocked card needs no unblock or new claim first (see the Hermes adapter reference, matching a received message to a card). |
| `Attach` | `attach <id> <path> --name=<name> --author=<a>` | Text. Hermes copies the bytes (25 MB cap) and renames a duplicate name to `name (1).ext`. The client writes the content to `attach-*` in the directory it is given and removes the file after; `RemoveLeftoverAttachFiles` removes what a process that died in between left. |
| `Attachments` | `attachments <id> --json` | Array of `id`, `filename`, `content_type`, `size`, `uploaded_by`, `stored_path`, `created_at`. |
| `NotifyList` | `notify-list <id> --json` | Array with every column of the subscription table, `delivery_metadata` as an object. A card without subscriptions, or an unknown card, gives `[]`. |
| `NotifySubscribe` | `notify-subscribe <id> --platform= --chat-id= --notifier-profile= [--thread-id=] [--user-id=] [--user-id-alt=] [--chat-type=] [--parent-chat-id=] [--guild-id=] [--delivery-mode=]` | Text. `--parent-chat-id` and `--guild-id` are stored in `delivery_metadata`; other keys of it (a scope id) cannot be set from the command line. A new subscription starts caught up on the card's events. |
| `Assignees` | `assignees --json` | Profiles on disk (`on_disk`) and card assignees, with counts. |
| `BoardExists` | `boards list --json` | Active boards; `--board` is ignored by `boards`. |
| `Version` | `--version` | First line `Hermes Agent v<major>.<minor>.<patch>[+build] (...)`. |

`--status` accepts `archived blocked done ready review running scheduled todo
triage`. The cards the adapter creates carry its own `--created-by` and
`--author` (default `sidera`); Hermes' defaults are `user` and the name of the
profile that owns `HERMES_HOME`, which are not true of a lane.

### Text from another agent is never an argument

Two rules keep text that another agent wrote off the command line.

1. **Hermes uploads the files a completed card names.** When a card is completed,
   the gateway looks in the event's summary and in the card's result for the paths
   of local files (absolute, `~/` or a drive letter, with a deliverable extension
   such as `.pdf`, `.txt`, `.md`, `.json`) and, for every one that exists and is not
   on a short denylist, uploads the file to the chat of each subscriber. A reply
   that carried a path would make Hermes send a file of this machine to a chat, and
   that cannot be undone. So the arguments `--summary` and `--result`, and every
   other argument (title, comment, block reason, metadata, attachment name), carry
   only text the adapter wrote: no remote text, and no path. Remote text goes to a
   card in two ways only: through standard input as the body of a created card
   (`--body-file -`), or as an attachment (`Attach`) of a card to be completed, which
   the result then points to by a name the adapter derived. The values the adapter
   quotes are bounded and are not another agent's: aliases and card ids, the words of
   an enumeration the node reports (a delivery state, a rejection reason), the fixed
   message of an error of the local node, and the address of a sender where no alias
   stands for it (in a title or a summary, and as `sidera_from` in metadata). An
   address is `node1.../ep1...`, and the daemon hands out only addresses it has parsed:
   bech32m text, whose alphabet has no `/`, `~`, `\` or `.`, so whatever endpoint part
   the remote node chose it cannot begin as a path or end in a file extension. The reasons the adapter gives for a
   card it cannot serve name the field that is wrong and do not repeat what the card
   holds, since the agent that wrote the card may have read text from another machine.
   The other value of a peer that travels as an argument is a message id, in the
   metadata of a completion and in an idempotency key: at most 128 bytes of visible
   ASCII, which the envelope decoder enforces, so no NUL, and Hermes reads only the
   `artifacts` member of metadata for paths, which the adapter never sets.
2. **The operating system limits arguments.** A single argument is limited to 128
   KiB on Linux and a whole command line to about 32K characters on Windows, and no
   argument can hold a NUL byte. The client therefore refuses, before it runs
   anything, an argument above `MaxArgBytes` (8 KiB) with an `Error` wrapping
   `ErrArgTooLong`, and one holding a NUL with `ErrArgInvalid`, both class
   permanent.

The client enforces, in `run` (the one place hermes is started), what it can see: the
size, the NUL and the executable (see Probe). It cannot tell whose words a line holds;
that the lines are the adapter's own is the callers' rule. The one flow that handled
another agent's words in a completion, the reply to a delegated card, attaches them.

- the body of a card goes through standard input (`--body-file -`);
- long text is stored with `Attach`, which writes it to a file in a directory the
  caller names, passes Hermes the path and removes the file;
- values are passed as `--option=value`, and the title, comment text and block
  reason come after `--`, so text beginning with `-` is never read as an option.

### Supported versions

`SupportedFrom` 0.22.2 up to, and excluding, `SupportedBefore` 0.23.0. Hermes is a
0.x project: a minor release may rename a flag or a field, and the samples
below are the guard. `ParseVersion` reads the number from the first
`Hermes Agent v...` line and ignores a build suffix (`+feishu`) and a prerelease
tag; `CheckVersion` refuses a version outside the range with an error that names
the version found and the range.

### Probe

`Probe` fails with the first of:

1. the resolved `hermes` executable is a Windows `.cmd` or `.bat` shim
   (`CheckExecutable(goos, path)`, a function of the path so that any machine can
   check it);
2. the version is outside the range;
3. the board is not among `boards list --json`;
4. a Hermes profile on disk is named `sidera`, `sidera-inbox` or begins with
   `sidera:` (`CheckProfiles` over `assignees --json`, `on_disk`): the dispatcher
   of Hermes tests each card's assignee with `profile_exists` on every tick and
   starts a worker for the card when such a profile exists. Names that only
   appear as assignees of cards, as the adapter's own do, are fine.

The first check is not only the probe's: `run`, the single place hermes is started,
resolves the executable and applies it before every command, so no command of the
client reaches a shim whatever the caller does. The other three are the probe's, and
the adapter does not leave them to a caller who may not ask: its gate runs `Probe`
before anything is read or written and again at intervals, and keeps receiving and
polling paused while it fails (see the Hermes adapter reference, availability). The
probe costs several processes and the loops ask at about the same time, so there is one
at a time: a caller that arrives while one runs waits for it and returns its result.

### Decoding

Each `--json` output is decoded into the types of `types.go`. Unknown fields are
ignored. A field the adapter relies on that is absent (a key missing; a `null`
value counts as present) fails the decode with an error naming it, wrapping
`ErrUnexpectedOutput`. Times are Unix seconds in Hermes and `time.Time` here, zero
for null.

### Errors and their classes

Every method returns `*Error` for a failed command: the subcommand, a `Class`, the
exit code (-1 when hermes did not run to an exit), the trimmed stderr and the
cause. `errors.Is(err, ErrTransient)` / `ErrPermanent` and `errors.As` with
`*Error` both work.

- **Permanent**: the same call will fail the same way: the card does not exist,
  its state does not allow the operation, an argument is invalid.
- **Transient**: the command cannot be run, it timed out or was cancelled, the
  database is locked, or the failure is not one this client recognises. Retrying
  is safe; calling a passing condition permanent would drop work.

Hermes prints no error codes. It exits 2 for an argument it rejects and 1 for
everything else, and for exit 1 only the text says why. The classification, from
the recorded outputs in `testdata/errors/`:

| Exit | stderr (start of line) | Class |
| ---- | ---------------------- | ----- |
| 2 | argparse usage error; `kanban: --metadata: ...`; `kanban: invalid board slug ...` | permanent |
| 1 | `no such task: ` (claim, show, attachments, notify-subscribe) | permanent |
| 1 | `kanban: unknown task ` (comment, attach, block with a reason) | permanent |
| 1 | `cannot claim <id>: status=...` | permanent |
| 1 | `cannot complete <id> (unknown id or terminal state)`, `... unsatisfied parent dependencies ...`, `... no result or summary evidence ...` | permanent |
| 1 | `cannot block <id>` (unknown, or the state does not allow it) | permanent |
| 1 | `kanban: unknown parent task(s)`, `kanban: title is required`, `kanban: no such file: `, `kanban: attachment exceeds 25 MB limit` | permanent |
| 1 | `kanban: could not initialize database: database is locked` (after Hermes' busy timeout, 120 s by default) | transient |
| 1 | a Python traceback, `OperationalError`, `disk I/O error` anywhere in stderr | transient |
| 1 | `kanban: board '<slug>' does not exist` | transient |
| any | anything else, a signal, an unknown exit code | transient |
| none | the executable cannot be started, the context ended, the output does not decode | transient |

The markers for a locked database or a traceback are checked first, because a
traceback can quote any text. The permanent patterns are anchored at the start of
a line so text that a card carries cannot reach them.

Cases the evidence does not settle, and that are therefore transient: a board
that does not exist (it may be created while the adapter retries, and the probe
reports it at start-up), an unrecognised message of exit 1, and a traceback
whose cause is unknown. A call that timed out may nevertheless have taken effect:
a retried `complete` then fails permanently with `unknown id or terminal state`,
which is for the caller to read as "already done".

## Why

- **The command line over the database or the REST API.** Hermes owns the schema
  and migrates it; a second writer would have to track every migration, and the
  dashboard API exists only while a dashboard is running. The command line is the
  interface Hermes' own workers use and its flags are documented in `--help`.
- **Standard input for bodies, a bound for the rest.** The alternatives were to
  pass everything as arguments and chunk it, which fails on Windows whatever the
  size, or to write every value to a file, which Hermes supports only for the card
  body and attachments. The bound is deliberately small: it is far below both
  limits and large enough for a summary line, a reason or a metadata object, and
  anything bigger is by nature a document that belongs in an attachment.
- **No remote text in a summary or a result.** Fencing it would not help: Hermes
  finds the paths in the text whatever surrounds them, and text that holds its own
  fence closes ours. The only safe place for text from another agent is one Hermes
  does not read for paths.
- **`--option=value` and `--`.** Remote-derived text can begin with `-`, and
  `argparse` would read it as an option (`--summary -x` fails with "expected one
  argument").
- **Classifying by text.** There is no other signal. The patterns are few, anchored
  and recorded, and the default is transient so that a new message in a newer
  Hermes is retried rather than dropped; a version range and the recorded samples
  turn such a change into a failing test and a refused start rather than a silent
  one.
- **Explicit `HERMES_HOME`, scrubbed Kanban variables.** A service environment
  has no shell profile, so Hermes would fall back to its default home. A worker's
  `HERMES_KANBAN_TASK` makes `complete`, `block` and `attach` on any other card
  fail with "worker is scoped to task ...".
- **`Probe` includes the profile check.** Creating a card whose assignee names a
  profile that exists makes Hermes run it as a task; refusing to start is cheaper
  than finding out from a worker that runs on the lane's cards.
- **Hermes' `assignees` for the profile list.** `hermes profile list` prints a
  table meant for people; `kanban assignees --json` is stable JSON and reports
  `on_disk` (a profile directory with a `config.yaml`, close to the
  `profile_exists` test of the dispatcher).

## Where

- `cmd/sidera-adapter-hermes/internal/kanban`: `client.go` (configuration,
  environment, the single place hermes is started, the argument bound),
  `commands.go` (one method per command), `types.go` (the decoded shapes, block
  kinds, delivery modes), `error.go` (`Error`, `Class` and `classify`),
  `probe.go` (versions and probe checks).
- Samples: `testdata/*.json` and `testdata/version.txt` are outputs of Hermes
  0.22.2 with the owner's paths and names replaced; `testdata/errors/*.txt` are the
  exit code and stderr of the failures. `README.md` of the package says how to
  record them again.
- Tests: `TestDecodeRecordedSamples` and `TestDecodeShapeChanges` (decoding),
  `TestClassifyRecordedErrors` (every recorded failure with its class; a new
  sample must be listed) and `TestClassifyUnrecorded`, `TestErrorMatching`,
  `TestCreateBodyThroughStdin` (a 256 KiB body, the explicit homes and the removed
  worker variable, through a stand-in for hermes), `TestArgTooLongIsRefusedWithoutRunning` (also the NUL rows),
  `TestTimeoutIsTransient`, `TestProbeAgainstRecordedOutputs`,
  `TestParseAndCheckVersion`, `TestCheckExecutable`, `TestShimIsRefusedByEveryCommand`,
  `TestCheckProfiles`.
- Not confirmed: the client has run on macOS against Hermes 0.22.2 only. The
  Windows rule is checked as a function of the path; whether Hermes installs a
  `hermes.exe` or a shim there is for the first run on Windows to show.
