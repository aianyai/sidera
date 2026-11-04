# kanban

The client of the Hermes Kanban board: it runs `hermes kanban --board <slug> ...
--json` and turns the answers into typed values and classified errors. What the
commands are, which Hermes releases are supported and why the rules below exist is
in [`docs/references/adapter-hermes/kanban-cli.md`](../../../../docs/references/adapter-hermes/kanban-cli.md).

Standard library only; it does not import the SDK, so it can be read and tested
without the adapter around it.

## Entry points

| Name | Holds |
| ---- | ----- |
| `New(Config)` / `Client` | One board of one Hermes home. Methods: `List`, `Show`, `Create`, `Claim`, `Block`, `Comment`, `Complete`, `Attach`, `Attachments`, `NotifyList`, `NotifySubscribe`, `Version`, `Assignees`, `BoardExists`, `Probe`. |
| `Error`, `Class`, `ErrTransient`, `ErrPermanent`, `ErrArgTooLong`, `ErrUnexpectedOutput` | The failure of every method (`client.go` runs, `error.go` classifies). |
| `BlockKind` | The four block kinds Hermes accepts; the one home of that vocabulary. |
| `SupportedFrom`, `SupportedBefore`, `ParseVersion`, `CheckVersion`, `CheckExecutable`, `CheckProfiles` | The probe rules; pure functions apart from `Probe`. |
| `types.go` | The decoded shapes. Each `UnmarshalJSON` names the fields the adapter relies on. |

## Invariants

- **No remote text on a command line.** A card body goes through standard input.
  Every argument above `MaxArgBytes` (8 KiB) is refused in `run`, the single place
  hermes is started, before anything runs. Long text is attached with `Attach`.
- **`HERMES_HOME` is always set explicitly** in the child environment, and the
  Kanban variables the service may have inherited (`HERMES_KANBAN_TASK`,
  `HERMES_KANBAN_BOARD`, ...) are removed (`childEnv`).
- **`--board <slug>` precedes the subcommand** and is on every call; the current
  board of Hermes is never used.
- **Values that may begin with `-`** are passed as `--option=value`, and free text
  positionals come after `--`.
- **Every failure is an `*Error` with a `Class`.** `classify` decides it from the
  exit code and stderr; anything it does not recognise is transient.
- **A field the adapter relies on that is missing from the JSON is an error**;
  fields it does not read are ignored.

## Re-recording the samples

`testdata/` holds real outputs of the supported Hermes. After a Hermes upgrade,
run the commands in an isolated profile and board (never the owner's: create a
profile with `hermes profile create <name> --no-alias --no-skills`, set
`HERMES_HOME` to its directory and `HERMES_KANBAN_HOME` to a scratch directory),
save each `--json` output, and replace home directories, host names and personal
profile names with neutral values. Failure samples in `testdata/errors/` are
`exit <code>` on the first line and Hermes' stderr after it; `recordedErrors` in
`error_test.go` must list each with the class it gets. A sample that no longer
decodes or classifies as before is a change to handle in the client, and a Hermes
release outside `SupportedFrom` / `SupportedBefore` stays refused until it is.
