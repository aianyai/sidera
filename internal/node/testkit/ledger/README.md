# ledger

The append-only record of what the fakes did. A test never asks the daemon what
happened; it asks the ledger, which was written by programs that know nothing of
the daemon's state. Read [`../README.md`](../README.md) first for where this
package sits and the rules every testkit package follows.

It imports the standard library and the SDK module (`sdk/go/strictjson`) and
nothing else: the ledger is a test oracle, so it must stay independent of the
code under test, `fsx` included.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Entry`, `Attrs` | One line of a ledger and its attributes. |
| `Encode`, `Decode` | The canonical line of an entry, and strict reading of a line. |
| `Factory`, `NewMemory`, `NewDir` | The two backends. `Open(writer, pid, now)` returns the `Writer` of one process. |
| `Writer.Append(kind, attrs)` | Numbers, stamps, validates and stores one entry. |
| `Memory.SetStep`, `Dir.SetStep` | The script step the fixture announces; entries written afterwards carry it. |
| `Memory.Log`, `ReadDir` | Read everything back, validated, as a `Log`. |
| `Log.Entries`, `Kind`, `Writer`, `Select`, `Count`, `Steps` | Queries. |
| `Kinds`, the `Kind…` constants | The vocabulary of entry kinds. |

## The format

One JSON object per line, members in this order:

`v` (1), `writer`, `pid`, `seq`, `at_ms`, `step`, `kind`, `attrs`.

- `writer` is `adapter/<instance>`, `runtime/<native ref>`, `agent/<endpoint>` or
  `fixture`.
- `pid` is the writing process, `0` for a writer that lives in the test process.
- `seq` numbers the entries of one writer in one process from 1 without a gap.
- `at_ms` is the time on the shared test clock, never the wall clock.
- `step` is the script step the fixture had announced; it may be empty.
- `attrs` holds the attributes the kind takes; `kinds.go` lists, for every kind,
  which are required and which are optional. A message body is never an
  attribute: a digest stands for it.

The line is canonical: `Decode` accepts only the line `Encode` writes for the
entry it read, so equal entries are equal bytes. A decoder that read members in
another order, with other spacing, or with an attribute spelled in another case
would let two readers of one ledger disagree.

## Rules

- **Strict.** An unknown member, a missing one, a repeated one, `null`, an
  empty value for an optional attribute (omit it), an unknown kind, an
  attribute the kind does not take, a missing required one and a negative
  number are errors.
- **An entry is written before the effect is confirmed.** The writers of the
  fakes append the entry for an effect before they tell the other side that it
  happened, so an effect that took place is always in the ledger. The file
  backend syncs every entry before `Append` returns, so this holds across the
  death of the writing process.
- **A refused or failed entry consumes no sequence number,** so a gap in a
  stream always means a lost entry.
- **One file per writer and process** (`<writer>-<pid>.jsonl`, the slash of the
  writer replaced by an underscore): appends of different processes to one file
  are not atomic on every platform. A second `Open` of one file is an error.
  Restarting an adapter gives it a new process, hence a new file whose sequence
  starts at 1.
- **`ReadDir` validates every file** against its own name (writer, pid),
  requires sequence numbers from 1 without a gap, and rejects any damaged line.
  The one damage it forgives is the last line of a process that a `fixture.kill`
  or `fixture.crash` entry names: a killed process may leave a line without its
  newline. A cut line is dropped, a complete one is kept.
- **Order.** `Memory` keeps the order in which entries arrived, a total order
  across writers. A directory ledger has only the time (which stands still on a
  test clock) and the order inside one writer, so judge it by counts and by the
  order inside a writer.

## What the caller must guarantee

- Pass the shared test clock as `now`. A writer with a real clock would put
  times on the ledger that no fake clock can reproduce.
- Open each (writer, pid) once per process. `Memory` continues the sequence of a
  stream that exists, which is how a restarted in-process writer behaves.
- Announce the step with `SetStep` before the action it names.

## Tests

`TestGoldenEntryVectors` (lines written out by hand), `TestDecodeRejects` and
`TestEncodeRefusesInvalidEntries` (one row per rule), `TestKindTableCoversEveryKind`,
`TestWriterStampsEveryEntry`, `TestWriterRefusesAnInvalidEntryWithoutConsumingASequenceNumber`,
`TestConcurrentWritersKeepEachSequence`, `TestDirWritesEachEntryBeforeAppendReturns`,
`TestReadDirRejects`, `TestReadDirToleratesTheLastLineOfAKilledProcess`;
`FuzzDecode` (an accepted line re-encodes to identical bytes) and
`FuzzParseStream`.
