# reconcile

The answer to `prompt.reconcile`, as a pure function of facts the runtime gathered.
Read [../../README.md](../../README.md) and
[the reference](../../../../docs/references/adapter-claude/README.md).

`Decide(Input)` returns the verdict, its grounds and, for a prompt found in the
record, the state of the turn it started. The order of the rules is the order of
the evidence: the record first (a prompt in it was delivered, whatever else is
true), then the checkpoint (no checkpoint proves nothing was started), then an
unreadable record, then a running turn process, and only then "absent".

The grounds always belong to the verdict as the protocol requires; the test
validates every row as a protocol result.
