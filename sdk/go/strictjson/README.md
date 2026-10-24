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
- **Any depth.** The rules about keys, required fields and `null` apply inside
  nested structs, slices, Go arrays and map values alike.
- **Opaque positions.** `json.RawMessage`, `any` and a type with its own
  `UnmarshalJSON` or `UnmarshalText` keep or decode their value themselves and
  answer for its strictness; such a type may call `Decode`.

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
