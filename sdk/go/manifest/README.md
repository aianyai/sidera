# manifest

The adapter package manifest, `sidera-adapter.json`: the file at the root of an
adapter package that says what the adapter is, how to start it, where it runs,
what it needs, which runtime versions it was tested against, what configuration
it takes and how much output one `recv` may print for it. `Parse` reads it
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
| `Manifest.Tests(runtime, version)` | Whether a runtime version is inside a tested range |
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
- `platforms` is a non-empty list of distinct `os`/`arch` pairs in Go's GOOS and
  GOARCH vocabulary. `requires` is for display and the install check only; the
  probe is what proves a program works.
- `tested` ranges are closed and compared as SemVer, `min` not above `max`;
  build metadata does not order versions.
- `config` has two groups, `instance` and `preset`, of fields with a key
  (`[a-z][a-z0-9_]{0,63}`, unique in the group), a type, a title and optional
  description, `required`, `default`, `min`, `max` and `options`. Types are
  `string`, `integer`, `boolean`, `enum`, `directory` and `string_list`. `options`
  belong to enums and are required for them; `min` and `max` belong to integers;
  a `default` has the type of the field, lies among the options of an enum and
  within the bounds of an integer. A directory value must be an absolute path that
  exists, which the daemon checks when it validates a value.
- `budgets` give the output budget per front end (`cli`, `mcp`, `direct`), at most
  one each, with a unit (`chars` or `tokens`) and a limit of 1 to 131072. The
  author subtracts any safety margin.
- `ack_markers` has two lists, `replaced` and `truncated`, of markers. A marker
  is `{literal}` or `{regexp}`, exactly one. **Only that shape is checked here.**
  Whether the text is usable is decided in one place, when the daemon compiles the
  markers as it admits the adapter.

## What the caller must guarantee

- A `Manifest` that did not come from `Parse` is validated before use.
- The front ends a budget names and the ones the adapter declares as capabilities
  must agree; that check needs both and lives in `internal/adapterreg`.
