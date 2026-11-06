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
| `undo` | For `apply`: `accepted` names items of a `remove` listing, to be undone |

## Ops

Everything that asks is two-phase: the adapter says what it would do, the CLI asks,
and the adapter then does what was accepted.

| Op | The adapter |
| -- | ----------- |
| `plan` | Lists the items setup would do and changes nothing |
| `apply` | Does the items of `accepted`, which came from a `plan`, and answers an outcome for each. With `undo`, the ids came from a `remove` listing and the items are undone |
| `remove` | Lists the recorded items setup would undo (each with the action `remove`), and changes nothing. The CLI asks and then sends `apply` with `undo` |
| `refresh` | Brings recorded items that still exist and whose content has changed up to date, at once and without asking. It never creates an item and never makes again one that is gone: those are reported as drift. With no record it answers `none` and reports nothing |
| `check` | Says whether a record exists and lists the drift; changes nothing. It returns an error only when it cannot read the record: a client it cannot probe is drift of the problem `broken`, so that an instance whose client is gone can still be removed. With no record it still lists what setup would have to make (`needs_setup`), so that `--check` tells a person an instance has no setup yet |

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

With no record, `refresh` is `none` and may carry neither outcomes nor drift, and a `refresh`
that did (`done`) an item the record before it did not hold has created something: the
helper refuses both as the adapter's bug (exit 1), so that no adapter has to remember the
rule that makes a user's deletion stand. `check` is the exception for drift: with no record
it still lists `needs_setup`, and the status is then `drift` with `recorded: false`.

`update`'s report per instance is these statuses plus two the CLI decides: `skipped`
(the adapter declares no setup) and `error` (the program failed).

## Versions

Request and result both carry `version`, the major version of the contract. The core
program writes the newest version it speaks and demands in the result the version of the request it answers. The
adapter's helper takes every version from `MinVersion` to `Version`, answers in the version
it was asked, and hands it to the adapter's code (`Request.Version`), so a release can move
to a new major version while the core programs it still has to serve speak the old one. A
side that is given a version it does not support fails, naming both; the helper does so
with exit 2. Both sides decode leniently, ignoring a member they do not know, so a member
can be added without a new version.

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
name and the adapter's version. The record is **the adapter's alone**: the CLI and the
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
  `RequireStateDir`;
- `Reconcile`, the classification that a check and a refresh share: each item the adapter
  wants is probed at its recorded target and found intact, stale (a refresh rewrites it),
  changed or gone (drift, which a refresh leaves), or without a record (`needs_setup`).
  Because both ops read the same answer, a refresh can only rewrite what a check calls stale;
- `WritePrivateFile`, the atomic write for the owner alone: a file of its own is written,
  flushed and renamed over the target, mode 0600 in directories of 0700. On Windows the
  mode has no effect and the file has the access its directory gives it, which under a
  state directory is the owner's alone. The rename is not flushed to the directory: a
  crash may lose the write, and setup is then run again.

Asking a person y/N is not in the SDK; the CLI asks.
