# Adapter setup

Some adapters drive a client that has configuration of its own: Claude Code and
Codex need the `sidera` MCP server registered, its tools allowed and a Skill
installed, and Hermes needs its Skill in a profile. That configuration is the
client's, outside Sidera's data directory, and it goes stale when the program moves
or the adapter is updated. **Setup** is the adapter's own program that brings it in
line with an instance, and this document is the contract between that program and
the core CLI that runs it. The command is `sidera adapter setup` (see
[the command-line reference](../command-line/README.md)); the SDK package is
[`sdk/go/adaptersetup`](../../../sdk/go/adaptersetup/README.md).

An adapter declares its setup program in the manifest's optional `setup` member
(see [the adapter-protocol reference](../adapter-protocol/README.md)), and the
management method `adapters.setup_info` tells the CLI the absolute argv to run
(see [the daemon reference](../daemon/README.md)). An adapter without the member has
no setup.

## Transport

The CLI runs the argv directly (no shell), with its own working directory and
environment, writes **one request** as JSON to its standard input and reads **one
result** as JSON from its standard output. The program logs to standard error and
never reads or writes the terminal: every question to the person is the CLI's, asked
from the plan the program returned. Why: the program is run both by a person and by
`sidera update`, which must be able to run it without one; and a program that asks
for itself would ask in its own words and its own way, once per adapter.

A program that exits non-zero, or whose output is not a result the CLI can act on,
is an error that names the instance and quotes the end of its standard error. The
exit codes are those of [the command-line reference](../command-line/README.md#conventions-of-every-program):
0 a result was written, 1 the program could not do the op (the reason is on standard
error), 2 a request it cannot take (not JSON, a version it does not support, an op it
does not know).

## Request

| Member | Meaning |
| ------ | ------- |
| `version` | The major version of the contract the core program speaks (see [Versions](#versions)) |
| `op` | `plan`, `apply`, `refresh`, `check` or `remove` |
| `home`, `home_is_default` | The absolute data directory the command resolved, and whether it is the default of the platform (no `--home`, no `SIDERA_HOME`). A client's registration of `sidera mcp` names the directory only when it is not the default |
| `instance`, `state_dir` | The instance and the directory its adapter keeps its state in |
| `config` | The values the owner set for the instance, as `adapter.start` passes them, with no default applied. It may hold a secret and is never logged |
| `sidera_program` | The absolute path of the core program as it was launched, links kept, and never quoted; only when the launch path is not known, what the platform reports for the running executable, made absolute. It is the file `sidera update` replaces in place, so a client's configuration stays valid across an update, a package manager's link keeps pointing at the current version, and setup and a later refresh pass the same string. It goes into a client's configuration as data; a program that prints it for a shell quotes it itself |
| `options` | The adapter's own arguments: the words after `--` on the command line, in order and untouched |
| `accepted` | For `apply`: the ids of the items to do |
| `declined` | For `apply` (not with `undo`), optional: the ids of the items that need consent, that the person was asked about and answered no to. Not the items left alone because an item they require was declined or did not complete (never asked), and nothing under `--yes`. See [What was declined](#what-was-declined) |
| `undo` | For `apply`: `accepted` names items of a `remove` listing, to be undone |

## Ops

Everything that asks is two-phase: the adapter says what it would do, the CLI asks,
and the adapter then does what was accepted.

| Op | The adapter |
| -- | ----------- |
| `plan` | Lists the items setup would do and changes nothing |
| `apply` | Does the items of `accepted`, which came from a `plan`, and answers an outcome for each. With `undo`, the ids came from a `remove` listing and the items are undone |
| `remove` | Lists the recorded items setup would undo (each with the action `remove`), and changes nothing. The CLI asks and then sends `apply` with `undo` |
| `refresh` | Brings recorded items that still exist and whose content has changed up to date, at once and without asking. It never creates an item and never makes again one that is gone: those are reported as drift, except what the person declined. With no record it answers `none` and reports nothing |
| `check` | Says whether a record exists (one that holds an installed item; see [the record](#the-record)) and lists the drift, leaving out what the person declined; changes nothing. It returns an error only when it cannot read the record: a client it cannot probe is drift of the problem `broken`, so that an instance whose client is gone can still be removed. With no record it still lists what setup would have to make (`needs_setup`), so that `--check` tells a person an instance has no setup yet |

`apply` is told by `undo`, not by the spelling of an id, whether its ids came from
`plan` or from `remove`: an item that setup recorded is listed by both, under the same
id, with different meaning, and a mistake would delete what it should create.

### Items

An item is `{id, kind, target, action, summary, consent, requires?}`. The `id` is unique in a
result and stable from the `plan` to the `apply` that follows, because the adapter
recomputes what the id means when it applies; an id it no longer lists is answered
`skipped`. `kind` and `target` are the adapter's words for what and where, and the CLI
only shows them. `action` is `create`, `update`, `remove` or `adopt` (take something
that was already there, and that setup did not make, into the record). `remove`
is what a `remove` listing says of every recorded item; a plan may also list it for
something setup did not make that stands in the way of what it wants (an earlier
installation of the same thing), and such an item always has `consent: true`, which
the result check enforces: nothing is removed from a person's client without asking. `summary` is one
phrase written as an instruction, which the CLI shows and asks as a question.

`consent` says whether the CLI asks. An item with `consent: false` is one the adapter
may do without asking: a file inside its own state directory, which nothing but the
adapter reads. The CLI always includes those in `accepted`. Everything else is asked
one by one: whatever touches the client's configuration, adopts something that was
there before, or removes something the user could have changed. A person who declines
an item declines only it.

`requires` lists ids of earlier items of the same listing that the item cannot be done
without (the source of a plugin cannot be deleted while the plugin stays installed). The
CLI, in one place, does not ask about an item whose required item was declined, applies
the items in stages so that an item whose required item did not complete (it failed, or
was skipped) is not sent to the adapter, and says in the prompt or the report that it was
left. So declining "uninstall the plugin" cannot leave an item that needs no consent,
such as deleting the plugin's source, running. It names earlier items only, so that the
order of the listing is a valid order of doing and there is no cycle.

### Results

A result is `{version, op, status, recorded?, items?, outcomes?, drift?}`. An outcome
is `{id, kind, target, status, message?}` with the status `done`, `skipped` (not done,
and not a failure: not asked, no longer applies, depends on an item that was not done) or
`failed`; a status a later release adds counts as `failed`, with the adapter's message. A
drift entry is `{id, kind, target, problem, summary}` with the problem `missing`,
`changed`, `stale`, `broken` or `needs_setup`; the CLI shows it and acts on nothing but
whether there is drift. `check` and `refresh` always say `recorded`: the guard of
`adapter remove` acts on it, so a result without it is refused.

The status of the result as a whole is derived by the SDK helper from the lists, so an
adapter cannot report one that contradicts them:

| Op | Status |
| -- | ------ |
| `plan`, `remove` | `ok` |
| `apply` | `ok`, or `error` when an outcome failed |
| `check` | `drift` when there is drift; else `none` when there is no record; else `ok` |
| `refresh` | `error` when an outcome failed; else `drift` when there is drift; else `none` when there is no record; else `refreshed` (a record exists and nothing is left to do; the outcomes say what was rewritten, which may be nothing) |

A status the op cannot have (a refresh that says `ok`, a check that says `error`, a plan
that says `drift`) makes the result invalid, whoever wrote it: the status decides the exit
code and the report line, and a result that contradicts its op would be judged differently by
each program that reads it.

With no record, `refresh` is `none` and may carry neither outcomes nor drift, and a `refresh`
that did (`done`) an item the record before it did not hold has created something: the
helper refuses both as the adapter's bug (exit 1), so that no adapter has to remember the
rule that makes a user's deletion stand. `check` is the exception for drift: with no record
it still lists `needs_setup`, and the status is then `drift` with `recorded: false`.

`update`'s report per instance is these statuses plus two the CLI decides: `skipped`
(there is no setup to refresh: the adapter declares none, the daemon has no `setup_info`, the
adapter's package cannot be loaded, or the adapter is not installed) and `error` (the program
failed, or the refresh had an item fail or the adapter reported an error).

## The command

`sidera adapter setup <instance> [--check | --remove | --refresh] [--yes] [-- <adapter arguments>]`
is the only caller of the contract besides `sidera update`; its flags, exit codes and
output are in [the command-line reference](../command-line/README.md#adapter-setup). What
matters for the contract:

- **The CLI asks, the adapter obeys.** It runs `plan` (or `remove`), shows the items, asks
  one question per item that needs consent and sends `apply` with the accepted ids and the
  ids of the items that need none. An adapter never has to wonder whether it may.
- **`--refresh` is the mode `update` uses.** It runs `refresh`, which asks nothing and
  creates nothing; an instance whose record does not exist answers `none`, so the CLI
  asks every instance that declares a setup and needs no record of its own.
- **`--check` reads the same record**, so a person and `update` see the same drift.
- **The daemon says how to run the program.** `adapters.setup_info` returns the argv, the
  state directory and the owner's values, so the CLI never reads a manifest and an old
  program can run a new package's setup.
- **A request is built once per command** and differs between calls only in `op`,
  `accepted`, `declined` and `undo`.

### Removing the instance

`sidera adapter remove` deletes the instance's state directory, which holds the record and,
for some adapters, the source directory of what was installed in the client (a plugin
whose source is gone vanishes from the client without a word). So before it calls the
daemon, the command runs `check` of the instance's setup, if the adapter declares one, and
refuses when a record exists (`check` always says `recorded`, which `Validate` requires, so
the guard cannot read a missing member as "no record"): the person undoes it with
`adapter setup <instance> --remove` first. That refusal does not offer `--force`, because
`--force` would delete the directory a client installation still points at, which is what
the guard prevents. Why `check` and not the record: the CLI never reads the record, and the adapter alone
knows what its record means. An instance or adapter the daemon does not know, a daemon
without `adapters.setup_info` (it cannot have a package with a setup) and an adapter without
setup are not checked. If the check itself cannot be made (the package cannot be loaded, the
program fails) the removal is refused with that error, because "no record" is not known;
this is the refusal that offers `--force`.
`check` itself returns an error only when the adapter cannot read its record; a client it
cannot probe (not installed, not answering) is drift of the problem `broken`, so an
instance whose client is gone is still removable. For an instance whose setup program is
broken, `sidera adapter remove <instance> --force` skips the check: the instance is removed
and whatever setup installed in the client is left in place, which the command says in a
warning that points to the guide's manual cleanup for each adapter. The flag exists as the
way out when the check cannot be made, so that no setup program can keep an instance from
being removed.

## Versions

Request and result both carry `version`, the major version of the contract. The core
program writes the newest version it speaks and demands in the result the version of the request it answers. The
adapter's helper takes every version from `MinVersion` to `Version`, answers in the version
it was asked, and hands it to the adapter's code (`Request.Version`), so a release can move
to a new major version while the core programs it still has to serve speak the old one. A
side that is given a version it does not support fails, naming both; the helper does so
with exit 2. Both sides decode leniently, ignoring a member they do not know, so a member
can be added without a new version. `declined` is such a member: an adapter that predates
it ignores it and goes on reporting what the person declined as `needs_setup`, and a core
program that predates it never sends it, so the adapter records no declines and reports as
before.

What is additive and what is not:

| Change | Version |
| ------ | ------- |
| A new request or result member | Same |
| A new `action` or `problem` value | Same: the CLI shows it as it is and acts on nothing but whether there is drift |
| A new outcome status that is a failure to an older reader | Same: a status the reader does not know counts as `failed`, with the adapter's message |
| A new result `status`, a new `op`, or a change in the meaning of a member | New: `status` decides the exit code and an older program must not guess |

The contract crosses versions. `sidera update` replaces the programs first, and the
**old** core program then runs the setup of the **new** adapter package (it asks the
daemon, which has just restarted into the new version, for the argv). So an adapter keeps
taking the version its oldest supported core program speaks. This is why the CLI refuses
no result for an `action`, `problem` or outcome status it does not know: the first later
release that adds a kind of drift would otherwise break the update of every program that
predates it. [The update reference](../update/README.md#what-the-management-interface-must-keep)
lists the contract with the management methods it travels with.

## The record

An adapter's setup remembers what it did in `<state_dir>/setup/record.json`: per item
`{id, kind, target, digest, version}` (the digest of what it wrote, so that a later
check can tell its own work from someone else's change), the `sidera_program` the items
name, the adapter's version and optionally `declined`, the ids the person declined (see
[What was declined](#what-was-declined)). The record is **the adapter's alone**: the CLI and the
daemon never read it, which is why a `check` or a `refresh` is an op of the adapter. It
has the members above and no others: a member the SDK does not know is dropped when the
record is rewritten, so an adapter that needs more keeps it in a file of its own. It is
written for its owner alone and atomically.

The rule that follows from it, and the reason for it: **setup touches only what it
recorded, and only while that still exists.** A refresh rewrites a recorded item whose
content is out of date. It does not make again an item the user deleted (the user's
decision stands; the adapter reports it as drift), and it does not touch anything it
never recorded. Anything outside the record changes only after the person agrees to
that item, one by one.

A record "exists", as `check` and `refresh` say it in `recorded`, when it holds an item
setup did. A record that holds only declines (the person said no to everything) records
nothing that was done: the adapter answers for it as for no record, and `adapter remove`
is not refused on its account.

### What was declined

A person who declines an item on purpose would otherwise see drift (`needs_setup`) and a
"run setup" hint after every `sidera update`, for something they decided against. So the
item they declined is remembered, and `refresh` and `check` do not report it. The rules:

- **Who writes it.** `Handle` merges `declined` into the record once the adapter's `apply`
  returns, for every adapter. An adapter reads its record, changes `items` and writes it
  back as it does today, and never edits `declined`; a record rewritten from scratch
  would forget the earlier declines, which only brings the drift back. The declined set
  becomes what it was plus `declined`, minus the ids in `accepted` (a person who accepts an
  item later wants it, whether or not it worked) and minus the ids the record holds. A record
  is made when only declines are to be kept. A request without `declined` changes
  nothing.
- **What reads it.** `Reconcile` leaves a wanted item that no record holds and the record
  lists as declined out of the drift (it is returned as `Reconciliation.Declined`); an
  item a later release wants, which nobody declined, is still `needs_setup`. An item that
  is recorded is never treated as declined.
- **A plan still offers it.** The person who runs `sidera adapter setup` by hand sees
  the plan, so they can change their mind. An adapter builds its plan from what it wants
  and what is there, and never filters it by `declined`; one that builds the plan from
  `Reconcile` adds `Reconciliation.Declined` to the items it lists, in its own order.
- **An undo forgets it.** An undo that leaves no item recorded removes the record, and the
  declines with it (`Handle` does so after the adapter's undo returns), so that a setup
  that is started afresh asks about everything.

The CLI sends `declined` only with the `apply` of a plain setup, for the items it asked
about, and says in its dialogue which were declined and that they are not reported again.
When every item is declined it still sends one `apply` with no `accepted`, to hand over the
declines.

### The state directory

The state directory must already exist: the daemon makes it, with the checks it applies
to everything in it, the first time the instance runs. Setup never makes it. The helper
refuses a missing directory before it calls the adapter for `plan`, `apply` (and the undo)
and `remove`, as does the writing of the record, with an
error that says to enable the instance and start the daemon so that it runs once;
`check` and `refresh` find no record in it and answer `none`.

## Shared code

`sdk/go/adaptersetup` holds what every adapter's setup would otherwise write again:

- the types of the contract and `Result.Validate`, which the CLI also applies to what it
  reads;
- `Command`, the adapter program's `setup` command for `adapterkit.Program`: it decodes
  the request, checks the version, calls the `Handler` method of the op, derives the
  status and writes the result, mapping errors to the exit codes above;
- the record: `ReadRecord`, `WriteRecord`, `RemoveRecord`, `RecordPath`, `Digest`,
  `RequireStateDir`, and `Record.Installed` and `Record.IsDeclined`, which an adapter uses
  where it would test the record for nil (`recorded` is `rec.Installed()`, and a refresh
  of a record that is not installed answers as for none);
- `Reconcile`, the classification that a check and a refresh share: each item the adapter
  wants is probed at its recorded target and found intact, stale (a refresh rewrites it),
  changed or gone (drift, which a refresh leaves), or without a record (`needs_setup`,
  unless the person declined it);
  Because both ops read the same answer, a refresh can only rewrite what a check calls stale;
- `WritePrivateFile`, the atomic write for the owner alone: a file of its own is written,
  flushed and renamed over the target, mode 0600 in directories of 0700. On Windows the
  mode has no effect and the file has the access its directory gives it, which under a
  state directory is the owner's alone. The rename is not flushed to the directory: a
  crash may lose the write, and setup is then run again.

Asking a person y/N is not in the SDK; the CLI asks.

### Running a client's command line

`ClientCommand.Run` is how a setup runs `claude`, `codex` and the like. Each rule is there
so that what setup sees and changes is the user's own client configuration, and so that a
run always ends:

- **The program is an absolute path, run without a shell.** Nothing is looked up on `PATH`
  at run time and no argument is ever interpreted by a shell.
- **The working directory is required, must exist, and is a directory with no project
  configuration of its own.** A client reads the configuration of the project it is
  started in (a trusted project's `.codex/config.toml`, a `.mcp.json`), so run in the
  caller's directory it would report, and change, something other than the user's
  configuration, differing from one invocation of setup to the next. A directory that is
  missing or is not one is refused before anything starts, with an ordinary error: left to
  the operating system it would come back as the program's own "no such file" and be read
  as "the client is not installed".
- **Standard input is the null device** (a nil `Stdin` is that on every platform), so a
  client that would prompt reads end of file and fails instead of waiting on a terminal.
- **The run is bounded, and its end kills the client's process group.** A program that
  outlives its limit (or whose caller's context ends) is killed and the error is a timeout
  naming the program and the limit (`context.DeadlineExceeded`). On Linux and macOS the
  client runs in a process group of its own and the kill is a SIGKILL to the whole group,
  because an npm-installed `codex` is a node wrapper that cannot forward a kill: killing the
  wrapper alone would leave the native program running, free to write the client's
  configuration after setup has reported a failure. A client in its own group also does not
  receive a terminal's Ctrl-C; it ends when the context does. On Windows only the program
  itself is killed, and a process it started may survive it. A grandchild that holds the
  output pipes open delays the return by at most a fixed 2 seconds, not for as long as it
  lives. The timeout is reported before the exit status, because a killed program's status
  would otherwise read as an exit code; but only when the program did not end by itself
  with a result: one that exited 0 is a success even if the context ended just after, and so
  is one that exited 0 while a grandchild still held its output (what it wrote by then is
  returned).
- **The environment is the caller's policy**; nil is the caller's own.
- **A non-zero exit is a result, not an error.** What an exit code means is the adapter's
  to say (`claude mcp get` of a missing server exits non-zero and that is the answer;
  `codex mcp remove` of one exits 0). A program that could not be started is a
  `*NotStartedError` wrapping the system's reason, so "the client is not installed"
  (`errors.Is(err, os.ErrNotExist)`) is told from "the client failed". `Failure` words a
  command that did not do what was asked, with each argument quoted and the last 2000 bytes
  of its standard error. The message is reported and logged, so no secret is passed as an
  argument.

### Editing a client's JSON file

`EditJSONFile(path, edit)` is how a setup edits a file the client owns and the user
may also edit (Claude Code's `settings.json`). It is conservative because a mistake
here costs the person their configuration, silently:

- **A link is followed and the file it leads to is written.** A settings file is often
  a link into a dotfiles directory; replacing it by a regular file would cut it from
  there. Relative, chained and dangling links are followed. A relative link is
  resolved from the directory the link is really in, as the kernel does, not from the path
  it was reached by: with `~/.claude` a link into a dotfiles repository, a settings file
  that is a link `../shared/settings.json` leads into the repository, not beside `~/.claude`.
  A dangling link gets its target made, if the target's directory exists; a directory is
  never made behind a link; a loop is an error.
- **The file is parsed strictly, and refused otherwise.** Valid JSON with an object at
  the root, no comment, no trailing comma, no byte-order mark, no repeated key (readers
  disagree on which value wins), nothing after the object; an empty file is not an
  object. The text must be valid UTF-8 and hold no half of a surrogate pair written as a
  `\u` escape: a rewrite would replace either with U+FFFD, changing what the person wrote
  without a word. The client reads such a file strictly and ignores a file it cannot read as a
  whole, so repairing it would be guessing at what the person meant. The error says
  that nothing was changed.
- **The edit keeps the file's style.** Members keep their order and numbers their
  spelling, and the indentation unit, the line ending and the final newline are those
  of the file. The unit is that of the first member of the root object that starts a
  line; an object whose members have no line break between them stays compact (a final
  newline alone is not a layout), and a file with no member to learn from (`{}`, or a
  file that is made) gets two spaces. Short arrays the person wrote on one line are
  written one element per line.
- **The mode is kept**; a file that does not exist is made with 0600 and, unless the
  path was a link, its directory with 0700. The file is replaced by a rename, so a hard
  link to it keeps the old content and the owner is that of the user running the setup.
- **The content is read again just before the rename and compared** with what was
  edited. If it differs (the client writes this file too) the edit is dropped with
  `ErrChangedDuringEdit` and the other writer's content stands. The instant between the
  comparison and the rename is not closed; it is the smallest this can make it without a
  lock the client does not take.
- **The edit navigates by type.** `Object` has `ObjectAt` (get or make a nested object),
  `HasString`, `AddString` (only if absent) and `RemoveString` (every copy) on the array at
  a key path. A key on the way that holds another type (`permissions` an array, `allow` a
  string) is `ErrUnexpectedType` naming the path and what was found, and nothing is
  overwritten. `ReadJSONFile` reads a file by the same rules (links, strict parsing) for a
  check that only looks, and a missing file is an empty object.
- An edit that reports no change writes nothing, so a rerun leaves the file byte for byte
  and with its modification time.

On Windows the rename is `MoveFileEx` with replacement, so an editor or client that
holds the file open without sharing makes the write fail with an error, and the file
stays as it was. The mode of the file is not meaningful there; the new file takes the
access its directory gives.
