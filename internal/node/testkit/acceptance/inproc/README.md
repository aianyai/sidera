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

Recovery evidence is recorded once per recipe and complete trigger. Only its
second generation contributes recovery coverage. Each owned target is then
actually interrupted in generation two: `Reached`, the exact last point, its
recorded path operands, a third generation and the domain judgement must all
agree. Observation alone proves no crash. When clean recordings are supplied,
Normal keys absent from every clean recording are derived from complete second
generations; each non-sync key receives a separate second-generation crash.

Offline damage reads the stopped disk directly and changes only records the
product wrote. The stopped creating endpoint is checked before its real control
record is damaged. Final recovery work uses the owners' existing predicates, and
closed-session checks share the session binding's audit.
