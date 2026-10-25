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
| `Writer.Append(kind, attrs)`, `Writer.AppendSeq` | Numbers, stamps, validates and stores one entry. `AppendSeq` also returns the sequence number the entry got, for a writer that must name it to another process. |
| `Memory.SetStep`, `Dir.SetStep` | The script step the fixture announces; entries written afterwards carry it. |
| `Memory.Log`, `ReadDir` | Read everything back, validated, as a `Log`. |
| `Log.Entries`, `Kind`, `Writer`, `Select`, `Count`, `Steps`, `PIDs` | Queries. `PIDs` lists the processes that wrote, for a fixture that ends what a test left behind. |
| `Log.CheckConfirmations` | The invariant that an effect is on record before it is confirmed (below). |
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
- **A confirmation names the entry it confirms.** The adapter's
  `native.create.returned` carries, with the `native_ref`, the `runtime_writer` and
  `runtime_seq` of the runtime's `runtime.session.created` entry for that session:
  the runtime's answer to the adapter carries its sequence number back, so the
  adapter can only have it once the entry is written and synced. The two writers
  are separate processes, and on a test clock that stands still the order of lines
  across files says nothing, so this reference is what proves "written first".
  The three attributes appear together or not at all (a creation that failed before
  any native session names none), and `CheckConfirmations` fails when the named entry
  does not exist or is not the creation of that session for that attempt.
- **Step names are unique.** An entry names its step and nothing else, so a name
  announced twice would put the entries of its second run among those of its first;
  `ReadDir` refuses such a ledger.
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
  across writers. A directory ledger is ordered by time, then by the script step
  the fixture announced (steps in the order it ran them), then by writer, process
  and sequence. Inside one step across writers only the time is known, which
  stands still on a test clock, so judge it by counts and by the order inside a
  writer.

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
`TestReadDirRejects`, `TestReadDirToleratesTheLastLineOfAKilledProcess`,
`TestReadDirRejectsARepeatedStepName`, `TestAppendSeqReturnsTheSequenceNumberOfTheEntry`,
`TestCheckConfirmations`;
`FuzzDecode` (an accepted line re-encodes to identical bytes) and
`FuzzParseStream`.
