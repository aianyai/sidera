# fsxtest

Test doubles for `fsx`: a crashable in-memory file system, a fault injector,
and the behavioural suite every `fsx.FS` must pass. It is an ordinary package
rather than `_test.go` files because the tests of several packages use it, as
`testing/fstest` and `net/http/httptest` are. Read
[../../../../docs/references/storage/README.md](../../../../docs/references/storage/README.md)
for the crash model, its sources and what it can and cannot prove.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `NewMem()` | An empty in-memory `fsx.FS` that distinguishes synced from unsynced state. |
| `Mem.Clone()` | The state a process crash leaves: every completed call survives, open files and locks are gone. |
| `Mem.CrashClone(rng)` | One possible state after a power loss at this instant; synced state always survives, the rest is chosen with `rng`. |
| `Mem.CrashCloneWorst()` | Deterministic power loss: only what was synced survives. |
| `Mem.SetAvailable(n)`, `Mem.SetOwnerOnly(path, bool)` | Stand-ins for the free space of the volume and for file permissions. |
| `NewFaulty(t, base)`, `Faulty.Fail`, `Faulty.FailShortWrite`, `Rule.Hits` | Fails chosen calls of any `FS`; the test fails if a rule never fired. |
| `Match` | Selects calls by transaction, role, step, ordinal of the key and path. |
| `RunConformance(t, newFS)` | The behaviour `Mem` and the real file system must share. |

## Rules of the model

- A file's synced content and size survive a power loss. A file's synced content
  is always a prefix of its content: files are never overwritten.
- Syncing a file does not make its directory entry durable. A directory's entries
  become durable only by `SyncDir` of that directory, which does not cover the
  content of its files or the entries of its subdirectories.
- Unsynced file data survives to any length from the synced length to the written
  length, and each 4 KiB block beyond the synced length is independently real,
  zero or garbage.
- Each unsynced directory name independently keeps its synced or its current
  binding, except that the two names of a rename are decided together, for a file
  as for a directory (and every name of renames that share a name): a single
  rename is atomic on journaling and copy-on-write file systems, so it is moved or
  not, never duplicated and never lost. A rename over a target leaves the old
  file under the target name with the source present, or the new file under the
  target name with the source gone. Different renames, and different names, stay
  independent. Non-journaling ext2, where a rename can be torn, is not modelled.
- A name rebound several times between two syncs of its directory shows either its
  synced binding or its last one, never one in between. Every operation of `fsx`
  that binds a name syncs the directory before it returns, so the next binding of
  the same name starts from a synced one.
- An unsynced `mkdir` can vanish with its content; `RemoveAll` can be half done.
- Unsynced data and entries are kept with the probabilities in the package
  comment; a seed fixes the result, so tests are reproducible.
- Windows is not modelled: `Mem` is weaker than any journaling or copy-on-write
  file system, and the Windows paragraphs of the reference stand on their own
  tests.

## Rules of the doubles

- **Clones are independent.** `Clone`, `CrashClone` and `CrashCloneWorst` return a
  new `Mem`; the original and sibling clones do not change. A power-loss clone is
  fully durable: losing power again changes nothing.
- **Every fault rule must fire.** `NewFaulty` checks in `t.Cleanup` that each rule
  fired at least once. A fault that was never injected proves nothing.
- **Faults go through the interface, never through build tags.** `Faulty` can
  wrap `Mem` or the real file system.
- **Cleanup is not selected by accident.** Steps tagged with the reserved
  transactions (`log`, `cleanup`) are selected only by a rule naming that
  transaction, so a rule for "any remove" does not fire on failure-path cleanup.
- **Reader calls have no tag or step.** A rule selects them by path only.
- **`Mem` names temporary files with a counter**, so the same seed gives the same
  clone; the real file system uses random names.
- **The tests state outcomes from the model's rules**, written out in the tests,
  never read from `Mem`'s own state. Randomized tests use fixed seeds and assert
  how often each outcome occurred.
