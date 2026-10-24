# protocol

The language-neutral definition of Sidera's wire protocols. Today it holds one:
the adapter protocol, version 1, under `adapter/v1/`. The JSON Schema files and
the golden messages here are the source of truth for an adapter written in any
language; the Go types in `sdk/go` are held to them by the tests in this
directory.

Read [the adapter-protocol reference](../docs/references/adapter-protocol/README.md)
for what the messages mean. This file is about the files.

## Layout

```
protocol/
  conformance_test.go   the tests that keep schema, examples and Go in agreement
  adapter/v1/
    message.schema.json   one JSON-RPC message: request, notification, success, error
    types.schema.json     shared definitions: identifiers, enums, process identity, capabilities, the recv page, control replies
    methods.schema.json   params and result of every method
    manifest.schema.json  the adapter package manifest
    examples/             golden messages, see below
```

All four files are JSON Schema 2020-12, written by hand, with
`additionalProperties: false` wherever the set of members is closed. The only
open objects are the ones that declare capabilities (`Capabilities` and its
`runtime` and `direct` parts, and `DaemonCapabilities`): they say nothing about
unknown members, which a reader ignores. Each has an
`$id` under `https://sidera.invalid/adapter/v1/`; the identifiers are never
fetched, they only let the files refer to each other
(`types.schema.json#/$defs/...`). A validator must be told to resolve those
`$id`s from the files.

## Conventions

- **The method list** is two `oneOf` lists in `methods.schema.json`, `requests`
  and `notifications`, each branch discriminated by the `const` of `method`.
  The annotation `x-direction` says who may send it (`daemon_to_adapter`,
  `adapter_to_daemon` or `both`) and, for a request, `x-result` names the
  definition of its result. A message is validated against `message.schema.json`
  first, then against the matching list; a result is validated against the
  definition `x-result` names.
- **Optional means omitted.** An optional member is left out rather than sent
  empty, zero or null; a required list or object may be empty. The schema states
  this with `minItems: 1`, `minLength: 1` and `minimum: 1` on optional members.
  A boolean may be sent as `false`. A member that accepts a legitimate zero (for
  example `wait_s`) says so.
- **Identifiers** (`EndpointRef`, `Address`, `MessageID`) are strings of 1 to 128
  characters with no pattern: they are opaque to adapters, and the daemon is the
  only place that parses their syntax. The examples write them as hexadecimal
  strings for readability; no sender or reader may rely on that spelling.
- **Patterns are anchored at both ends, and an implementation must anchor them
  fully.** Every pattern ends with `$`. In some regular-expression engines `$`
  also matches before a trailing newline (Python's `re.search`, Perl, PCRE
  without the dollar-end-only option), so `"abc\n"` would match `^[a-z]+$`. A
  validator or a hand-written check must match the whole string and nothing more
  (Python: `re.fullmatch`, or `\Z` in place of the final `$`; PCRE: the `D`
  modifier). The Go side uses RE2, where `$`
  without the multi-line flag matches only at the end of the text, and a
  conformance test checks it. A value with a trailing newline is not valid for
  any pattern of the protocol.
- **Numbers** are integers written as plain integer literals. The schema accepts
  `1.0` as an integer, as the specification says; the Go types do not, and
  neither should a sender.
- **Repeated keys** in one object are not allowed, at any depth: parsers disagree
  about which of two equal keys wins, so a message must not contain any. The
  schema cannot state it. The Go decoder rejects them in params and results, and
  the Go peer rejects a repeated member of the frame itself (`jsonrpc`, `id`,
  `method`, `params`, `result`, `error`) with `-32600` and a null id, before it
  reads the message. An implementation in another language must do both.

## What the schema cannot state

These rules are enforced by the Go validators only. They are the only places
where Go rejects something the schema accepts, and Go reports them with an error
that matches `adapterproto.ErrBeyondSchema` (`manifest.ErrBeyondSchema`).

- A version range has `min` not above `max` (`initialize`, the manifest, the
  data of the version error).
- A `state` or `checkpoint` is at most 16384 bytes in compact form.
- On a page, a part label reads `part index/total`, `index` is not beyond `total`,
  `next` is present exactly on every part but the last, names the same message
  and points at part `index + 1`.
- In a status reply `conditions` ascend and `presets` are sorted by name with no
  name twice.
- In a manifest: a tested range has `min` not above `max` in SemVer order; a field
  `default` lies within the field's options, or within `min` and `max`; `min` is
  not above `max`; the keys of one group of fields are distinct; no front end has
  two budgets and none has two receive wait limits.

## Examples

`adapter/v1/examples/` holds golden messages. Each is a complete JSON-RPC message
(a manifest is a whole manifest) and is what the tests check the schema and the
Go types against.

- `<method>/request-<case>.json`, `result-<case>.json`, `notification-<case>.json`
  and `error-<case>.json`. The directory is the method name; `$/cancel_request` is
  in `cancel_request/`. A result file is the response to the method of its
  directory. `frame/` holds errors that belong to no method.
- `manifest/<case>.json`.
- `open/<case>.json`: a document with members that a reader of this version does
  not know, in the places the protocol leaves open. A wrapper
  `{"kind", "method", "note", "ignoring": [JSON pointers], "document"}`: the schema
  and Go accept the document, and Go reads it as the same document without the
  members named in `ignoring`.
- `invalid/<case>.json`: one wrapper per case,

  ```json
  {"expect": "both", "kind": "request", "method": "ping",
   "note": "what is wrong", "document": { ... the message ... }}
  ```

  `kind` is `request`, `notification`, `result`, `error`, `manifest`, `frame`
  (checked against `message.schema.json` only) or `message` (a request or
  notification checked against both the message and the methods schema, whichever
  its shape says); `method` names the method for `request`, `notification` and
  `result`. `expect` says where the case must be rejected:
  `schema` (the schema rejects it; used for frame-level mistakes the message types
  never see), `go` (the schema cannot state the rule: the schema accepts it and Go
  rejects it with a rule beyond the schema), or `both`.

Every method has a request and a result example, every enum value and error code
appears in an example, and every example is accepted by the schema, decodes into
the Go type of its method and encodes back to the same data.

## Changing the protocol

A change to the protocol edits the schema, the Go type, the golden messages and
the reference together. The tests fail until they agree: the method list, the
directions, the enums, the error codes and the patterns are compared item by
item; every invalid case is checked at the layer it names; a differential test
mutates every golden message hundreds of thousands of times and requires the
schema and Go to give the same verdict (a fuzz target, `FuzzDifferential`, does
the same with the fuzzer's inputs). New behaviour inside version 1 is only ever
added as a capability: the peer that offers it declares it first (the adapter in
the `capabilities` of the `initialize` result, the daemon in the `capabilities`
of its `initialize` params), a reader ignores a capability it does not know, and
a sender never uses what the other side has not declared. Everything else that
would change a message, a new member of a message included, needs version 2.
Capabilities are the only place where an unknown member is not an error.

Run from the repository root, in the workspace:

```
go test ./protocol/
go test ./protocol/ -run='^$' -fuzz=FuzzDifferential -fuzztime=30s
```
