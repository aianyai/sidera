# manifest

The adapter package manifest, `sidera-adapter.json`: the file at the root of an
adapter package that says what the adapter is, how to start it, how to run its client setup,
where it runs, what it needs, what configuration
it takes, how much output one `recv` may print for it and how long one `recv` may
wait for a message. `Parse` reads it
strictly and `Validate` states the structural rules once. The daemon reads the
manifest when it installs an adapter and again, with the capabilities the adapter
declares, when it admits one (`internal/adapterreg`).

See [the adapter-protocol reference](../../../docs/references/adapter-protocol/README.md)
for what each field is for, and `protocol/adapter/v1/manifest.schema.json` for
the same rules in JSON Schema.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `Parse(data)` | Strict decode and `Validate`; the only way to get a `Manifest` from bytes |
| `Manifest.Validate()` | Every structural rule; use it on a manifest built in code |
| `Encode(m)` | The text of `sidera-adapter.json` for a manifest built in code: validated, indented, with a trailing newline |
| `WritePackage(dir, m, binary)` | An installable package directory: the manifest and a copy of the binary built for the platform at the manifest's `entry.program`, with `.exe` added to both for a Windows package, and to a setup program that names the same binary; what each adapter's `package` subcommand ends in |
| `Field.CheckValue(raw)`, `CheckValues(fields, values)` | The rule for a configuration value, and for the values of one scope (an instance, or one adapter's section of a preset): the kind, the options of an enum, the bounds of an integer, the limits of a string and a list, an absolute directory, no unknown key, every required field present. The daemon applies it when a value is written; an adapter author can use it to check a configuration in a test. A default is held to the same rule except that a directory default need not be absolute |
| `FieldTypes()`, `BudgetFrontends()`, `Units()`, `KnownOS()`, `KnownArch()` | The vocabularies the schema is compared with |

## Rules

- **Strict.** Unknown fields, a field name that differs in case, `null`, a
  missing required field, a repeated key, and an explicit empty string, number,
  list or object for an optional field (omit it) are errors.
- `id` matches `[a-z0-9][a-z0-9-]{0,31}`; the program convention is
  `sidera-adapter-<id>`. `version` is SemVer 2.0 with at most nine digits in each
  of major, minor and patch; the adapter reports the same version in `initialize`.
- `protocol` is a range of protocol versions, 1 to 65535, `min` not above `max`.
- `entry.program` is a path inside the package: not absolute (no leading slash or
  backslash, no drive letter) and with no `..` segment, split at either kind of
  slash. `entry.args` are strings.
- `setup` is optional and has the shape and the rules of `entry` (both are an
  `Entry`): the program, with its arguments, that the core CLI runs to bring an
  instance's client in line with the instance. An adapter without it has no setup.
  Errors name it as `setup.program` or `setup.args[i]`.
- `platforms` is a non-empty list of distinct `os`/`arch` pairs in Go's GOOS and
  GOARCH vocabulary. `requires` is for display and the install check only; the
  probe is what proves a program works.
- `config` has two groups, `instance` and `preset`, of fields with a key
  (`[a-z][a-z0-9_]{0,63}`, unique in the group), a type, a title and optional
  description, `required`, `default`, `min`, `max` and `options`. Types are
  `string`, `integer`, `boolean`, `enum`, `directory` and `string_list`. `options`
  belong to enums and are required for them; `min` and `max` belong to integers;
  a `default` has the type of the field, lies among the options of an enum and
  within the bounds of an integer. A directory value must be an absolute path, which the
  daemon checks when it writes a value; that the directory exists is for the
  adapter to report.
- `budgets` give the output budget per front end (`cli`, `mcp`, `direct`), at most
  one each, with a unit (`chars` or `tokens`) and a limit of 1 to 131072. The
  author subtracts any safety margin.
- `recv_wait_limits` give the longest an agent's `recv` may wait per front end
  (`cli`, `mcp`: the agent front ends of the adapter protocol, not `direct`), at
  most one each, `max_s` a whole number of seconds from 1 to
  `MaxRecvWaitSeconds` (120, the longest an agent's `recv` may wait whatever a
  manifest declares, and the only place it is written). The client that runs a
  front end ends a call at its own timeout, and only the adapter knows what that
  is. A front end without a limit gets the daemon's default
  (`adapterreg.DefaultRecvWait`). A direct endpoint's `endpoint.recv` has its own
  cap, `adapterproto.MaxWaitSeconds`, which the adapter uses in `wait_s`; this
  field does not govern it.
- `ack_markers` has two lists, `replaced` and `truncated`, of markers. A marker
  is `{literal}` or `{regexp}`, exactly one. **Only that shape is checked here.**
  Whether the text is usable is decided in one place, when the daemon compiles the
  markers as it admits the adapter.

## Compatibility

`manifest_version` stays 1 while no implementation exists outside this
repository: a member may be added (or removed) in place, as `recv_wait_limits`
and `setup` were. A reader decodes strictly, so an older daemon refuses a package
whose manifest has a member it does not know; the only older daemons are Sidera's
own releases, and `sidera update` restarts the daemon before it installs the
adapter packages. From the first implementation outside this repository on, a
change of this kind needs a new `manifest_version`. The adapter-protocol
reference lists the in-place changes.

## What the caller must guarantee

- `WritePackage` replaces files of the same names and keeps others in `dir`; a caller
  that wants a new directory checks it first. The binary is written at the
  manifest's `entry.program`, which the installer requires the package to hold, so
  the two cannot differ. A package for one Windows platform gets `.exe` appended to
  `entry.program` if it lacks it (Windows starts no file without an executable
  extension), in the file and in the manifest written, and so is a `setup.program`
  that names the same binary; an adapter names its program the same way for every
  platform and does not handle the extension. A `setup.program` that names another
  file is one the caller adds to the package before `WritePackage` runs (the
  manifest is written last, so a package cut short has no manifest): the installer
  requires every program the manifest names to be in the package, and on Windows
  that file carries its own `.exe` name.
- A `Manifest` that did not come from `Parse` is validated before use.
- The front ends a budget or a receive wait limit names and the ones the adapter
  declares as capabilities must agree; that check needs both and lives in `internal/adapterreg`.
