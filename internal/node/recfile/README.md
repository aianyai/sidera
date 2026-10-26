# recfile

The framing of a record file of the node: one first line, then the body.

```
sidera-record 1 <kind> <crc32c> <length> <key>\n<body>
```

Read [../../../docs/references/storage/README.md](../../../docs/references/storage/README.md)
for the reasons. The package is below the packages that own records (`store`
for `state/`, `keys` for `keys/`), which put their records in it; it knows
nothing of what a kind, a key or a body means, imports only the standard
library, and does no I/O.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `EncodeFrame(kind, key, body)` | The bytes of a record file; refuses what no reader accepts. |
| `ParseHeader(data)` | Checks the syntax of the first line (step 1) and returns it with the bytes after it. |
| `CheckBody(h, body)` | The length and the checksum (steps 5 and 6). |
| `DecodeFrame(data)` | Both of the above, for a caller that checks the kind and key itself. |
| `FileName(kind, key)` | The name of the file of a record whose name is derived: the hex SHA-256 of kind, a space and the key, then `.rec`. The files of `store`'s `out/` put the sending endpoint before it; `store` makes that name and this package is unchanged. |
| `EncodeSingleton`, `DecodeSingleton` | A record of which there is one: its key is its kind and its file name is fixed by the owner. |
| `Damage`, `DamageClass`, `DamageReason`, `HeaderDamage` | What a failed validation reports, with a class and a typed reason. |
| `Checksum`, `ChecksumText`, `ValidChecksumText` | The CRC32C of the format, for other formats that check pieces the same way. |

## Rules

- **The checksum covers the body only.** The kind and the key of the first line
  are checked by the file name, which is their hash: a flipped bit anywhere in
  the first line makes the name not match, so a first line that passed can be
  trusted to name its key. The checksum cannot cover the first line, or a damaged
  first line could not be told from a damaged body, and the owners need to read
  a key out of a file whose body is gone.
- **The two classes of damage are `HeaderUnreadable` and `BodyDamaged`.** Only
  the second carries the kind and the key, and only because the first line was
  readable.
- **Everything is canonical.** A first line has no leading zeros, upper case or
  padding, so `EncodeFrame(DecodeFrame(x)) == x` for every `x` that decodes.
- **A damage never echoes the first line.** Its detail names the rule and the
  number of the field: a damaged newline can glue the body of a file, a private
  key for one under `keys/`, into the first line.
- **Limits.** The first line is at most `MaxHeaderLen` bytes, the body at most
  `MaxBodyLen`.
- **A singleton is never replaced by the package that finds it damaged.** Its
  owner refuses to start and says which file.

## What the caller must guarantee

- Check the file name (derived kinds) or open the one path of the kind
  (singletons) before it trusts a key from `DecodeFrame`.
- The owner's directory decides which kinds a file may hold; this package does
  not.

## Where the tests are

`TestFrameGoldenVectors` (vectors written by hand, checksums from a separate
implementation), `TestDecodeFrameDamageByRule` (one row per rule),
`TestSingletonRecords`, `TestHeaderDamageNeverEchoesTheFirstLine`, and
`FuzzDecodeFrame`.
