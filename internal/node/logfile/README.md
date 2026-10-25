# logfile

How the daemon and its adapter hosts write their logs. Read
[../../../docs/references/daemon/README.md](../../../docs/references/daemon/README.md)
for the reasons and for where the logs are used.

This is a runtime package of layer 2 of [`internal/node/README.md`](../README.md):
it imports `fsx` and nothing else of this module, and reads no clock. It touches
files only through the `fsx.FS` it is handed.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `New(fs, Budget)`, `Handle` | The restricted handle: it can open, rotate and remove logs under `logs/`, and nothing else. It knows the budget the logs share. |
| `Budget` | The total the logs may take and the source of the number of enabled instances (nil is none), both asked at every write, and a floor (zero is `MinThreshold`; only a test that rotates after a few lines sets another). |
| `Handle.Open(name)`, `Log.Write`, `Log.Close` | An open log. A write is one line; the threshold is the budget's share, asked at every write. |
| `Handle.Remove(name)` | Deletes a log and its rotation, as when its instance is disabled. |
| `Threshold(total, instances)` | The size at which each log is rotated: the rule `Budget` applies. |
| `Dir`, `MinThreshold`, `ErrBadName` | `logs`, 1 MiB, and the refusal of a name that is not a log's. |

## Rules

- **A log is not a crash point.** Every step carries the reserved `log` tag, which
  the numbering of crash points and the fault rules skip. Logs are written by many
  goroutines, so a numbered step would make the numbering of the real writes depend
  on how much was logged, and the tail of a log lost in a crash harms nothing. This
  is the one exception to "every persistent write goes through a transaction", and
  it is the whole of it: the handle has no operation that writes anything but a log.
- **A name is a log's name or it is refused.** One lower-case alphanumeric start,
  letters, digits and hyphens, `.log`, at most 64 characters before the extension:
  no separator and no dot but the extension's, so a name cannot lead out of
  `logs/` or to a rotation, a hidden file or a temporary file.
- **One rotation.** A log and its rotation (`<name>.1`) are all there is. A write
  that would take a file that already holds something past the threshold makes the
  file the rotation, replacing the older one, and starts a new file; a line longer
  than the threshold goes whole into a file of its own. A line is never cut, and
  the two files never hold more than twice the threshold plus one line.
- **The threshold follows the total, and the handle alone applies the rule.**
  `Threshold` shares the total among two files for the daemon and for each enabled
  instance, with a floor of 1 MiB, which an extreme number of instances can push the
  real total past by a little. The handle is given the total and the source of the
  instance count once, in its `Budget`, and every log it opens reads them at each
  write: the daemon and the adapter host open their logs by name and never count
  instances or divide anything, so the rule has one home and two callers cannot
  disagree about the share.
- **A failed write is the caller's to drop.** `Write` returns the error and keeps
  nothing of the line; the next write goes on. A failed rotation opens the file
  again as it was, so a failing disk never leaves the log without a file.

## What the caller must guarantee

- `logs/` exists: `init` makes it and the start makes it again.
- Hold the instance lock while writing: there is one writer of the data directory.
- Never log a message body, a token or a key; the handle writes what it is given.
- Close a log before the daemon exits; a line not yet written is lost, as in a crash.

## Where the tests are

`logfile_test.go`: the names the handle accepts, that no step of a log is a crash
point, the rotation and its bound, that a line is never cut, the threshold read at
every write and the size of a reopened log, concurrent writers, the removal, the
numbers of `Threshold` worked out by hand, the share of the budget that two logs of
one handle follow as the number of instances changes, the defaults of a `Budget`, a
handle without a total, a failed write and a failed rotation.
