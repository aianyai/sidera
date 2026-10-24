# internal

Two kinds of package live here: **contract packages**, which are every
directory in this tree except `node/`, and the **runtime packages** under
[`node/`](node/README.md).

A contract package holds a set of rules or formats that the daemon, the CLI,
relays and adapters must all apply identically. Every contract package follows
the conventions below. A runtime package does the I/O, runs the goroutines and
reads the clock on top of them, and follows its own rules in `node/README.md`.
A contract package never imports `internal/node/`; a test in `node/` enforces
that on every supported platform and tag set. Code that belongs only to the
command line goes in `cmd/sidera/internal/`.

- **One home per rule.** A rule lives in exactly one package and callers never
  re-implement a check. Otherwise two components can disagree about the same
  input, which on a network of independent nodes is a protocol fork.
- **Pure.** No I/O, no goroutines, no global state, and no reading of the
  clock: callers pass `now time.Time` (UTC) explicitly, and randomness is
  injected as an `io.Reader`. Two nodes evaluating the same inputs must reach
  the same verdict, and property tests must be able to replay any order of
  events.
- **Minimal dependencies.** No dependency on the libp2p runtime or on storage.
  Only the standard library and other contract packages, plus third-party
  dependencies explicitly approved for that package, and the protocol
  definitions in the `sdk/go` module, which the daemon shares with adapters. This
  keeps the contracts usable from every process, including ones that never open
  a network socket.
- **Table-driven tests.** Each table row names the rule it exercises, so a
  failure points at the rule and not only at the input. Decision functions
  return a verdict together with a machine-readable reason, so callers and
  tests can tell why without parsing text.
- **Documented.** Every package has a `README.md` listing its entry points and
  invariants. A change to a rule or format updates that README and its topic
  under [`docs/references/<topic>/`](../docs/references/README.md) in the same
  commit, so the code and the normative description never drift apart.
