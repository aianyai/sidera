# strictjson

The strict JSON decoder of the SDK. `encoding/json` is lenient in ways a wire
message, a manifest or a configuration file cannot afford, so every reader of
such a document decodes with this package: the adapter protocol and the manifest
do, and any other reader, in the daemon or outside the repository, applies the
same rules because it calls the same code. The package is public so that a
reader outside the SDK needs no second copy, which would give the rules two
homes; `DisallowUnknownFields` is not a substitute, since it still matches keys
case-insensitively and accepts repeated keys and `null`.

See [the adapter-protocol reference](../../../docs/references/adapter-protocol/README.md)
for how the protocol layers use it.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `Decode(data, &v)` | Decode one JSON value into a struct, slice or map under the rules below |
| `DecodeOpen(data, &v)` | `Decode` for one object whose unknown members are ignored; for the `UnmarshalJSON` of a type that a newer sender may extend |
| `NoDuplicateTopLevelKeys(data)` | Reject a repeated key in the outer object only, for a caller that reads just that object and leaves the rest to `Decode` |
| `Error` | A failure in the input, with the JSON path of the offending value |

## Rules

- **Exact keys.** A key matches a field name byte for byte; `{"PID":7}` does not
  fill `pid`.
- **Required by default.** A field without `omitempty` must be present, even when
  its zero value would be legal.
- **No `null`.** `null` is never a value, except in a position typed
  `json.RawMessage` or `any`.
- **`omitempty` means absent.** For a non-pointer field with `omitempty`, an
  explicit empty string, `0`, empty list or empty object is an error, because
  after decoding it could not be told from absence. A pointer field accepts any
  value of its type, and `false` is always accepted for a bool.
- **One document.** The input is one valid UTF-8 JSON value with nothing after it.
  Unknown fields are errors, except in the object given to `DecodeOpen`.
- **No repeated key**, at any depth (ignored members of an open object included),
  because parsers disagree about which duplicate wins.
- **The right type.** A value must have the JSON type of its Go field: a string,
  a boolean, a number, an array or an object. A number must fit the field: no
  fraction or exponent for an integer type, non-negative for an unsigned one, within the range of
  the type. The same holds for the keys of a map with integer keys.
- **A path for nearly every input error.** An `*Error` names the offending value
  by its path (`sessions[2].process.pid`; a map key is a path element), for a
  wrong JSON type or a number that does not fit just as for an unknown field, a
  repeat or a `null`. The messages `strictjson` produces say what was expected
  and never repeat the value. Some messages are not its own and may contain the
  value: a type that decodes itself refuses with its own message, and a field with
  the `string` option, or a `json.Number` given a string that does not spell a
  number, is checked by `encoding/json` alone and carries its message. A caller
  that must keep values secret does not show these.
  `Path` is empty when the whole document is at fault (input that is not valid
  UTF-8 or JSON, data after the value, a root of the wrong type) and where the
  failure cannot be placed: a member whose key is the empty string, whose path
  element the syntax cannot show; a field with the `string` option; a
  `json.Number` given a string that does not spell a number; a refusal by a type
  that decodes itself that offering its value again does not reproduce, because
  the type decides on byte form, member order or state; and the refusal of a map
  key by a key type that decodes itself. Where a document has several problems,
  the same input always reports the same one: object members are walked in sorted
  key order.
- **Any depth.** The rules about keys, required fields and `null` apply inside
  nested structs, slices, Go arrays included, and map values alike.
- **Opaque positions.** `json.RawMessage`, `any` and a type with its own
  `UnmarshalJSON` or `UnmarshalText` keep or decode their value themselves and
  answer for its strictness; such a type may call `Decode`. When one refuses its
  value, the error carries the path of that value, and the path of the error the
  type itself raised through `Decode` or `DecodeOpen` continues from there, for
  example `inner.open.m`. The refusal is located only after decoding has failed,
  by offering each such value to a fresh copy of its type, so the position never
  changes what is accepted. A map with a key type that `encoding/json` cannot
  decode into (a bool or float key, say) is left to `encoding/json`, which refuses
  it.

## What the caller must guarantee

- The target of `Decode` is a non-nil pointer, and of `DecodeOpen` a non-nil
  pointer to a struct; anything else is an error that is not an `*Error`.
- Every exported field of a struct that is decoded into has a `json` tag (the tag
  `-` leaves it out) and is not embedded; a struct that breaks this makes
  `Decode` panic, because the mistake is in the program and not in the input.
- A type that calls `DecodeOpen` from its `UnmarshalJSON` passes a pointer to a
  defined type with the same fields and no such method, or the call recurses.
- Every problem in the input is an `*Error`; its message begins with
  `strictjson: `, which a caller that wraps it in its own error type may trim.
- A type that decodes itself through `Decode` or `DecodeOpen` is offered its
  value once more after a failure, on a fresh instance and with the value
  re-encoded (members sorted, escapes normalised), to find which one refused. Its
  decoder must therefore have no side effects and decide only on the content of
  the value, not on its byte form, the order of its members or state of its own;
  otherwise the path may name the wrong position or be missing.
- Such types must not nest without bound on untrusted input. On the failure path
  each one is decoded twice, so `k` of them nested inside one another cost `2^k`
  decodes (pinned by `TestDecodeFailureCostOfNestedSelfDecodingTypes`).
