# In-process acceptance

Read the parent [README](../README.md) first. `bindCatalog` converts a fresh recipe
catalog into existing `harness.WalkScenario` and `harness.StartupWalkScenario`
values. `freshScenario` selects a name from another fresh catalog before each run.
Constructing a catalog allocates no fixture.

Tests call `harness.NewWalkBackend(t).Run` directly and sequentially. A normal
recording must have one generation, no violations, no undeclared writes and no
Recovery writes, and stay within its fixed budget. Coverage diagnostics compare
actual hits, including real setup, with the complete daemon declaration table;
missing keys remain explicit. These recordings describe the implemented recipes,
without asserting that every declared family is represented.

`TestSendJudgementRejectsAnAcceptedBodyThatWasNotRead` removes the real receive
and acknowledgement drain and requires the domain judgement to reject the run.

`newSessionOps` binds session callbacks to the current fixture process.
`checkClosedSession` is the final read-only audit of a closed endpoint, its cleared
token and absent session checkpoint. Session recipes run with the real adapter
host and its fake native runtime; callbacks do not cache an engine across restarts.
