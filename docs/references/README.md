# Engineering references

Living documents, organized by topic, committed with the code they describe. They are the one authoritative description of Sidera as built: its glossary and its decision record. When you have a question about a topic, read its directory first, not git history.

Each topic is a directory `docs/references/<topic>/` whose `README.md` is its entry point. Each document answers three things: **what** (the rule, protocol or format), **why** (the trade-off and the alternatives rejected), **where** (the code paths and tests). A document describes only what is committed and changes in the same commit as the code it describes. When a document and the code disagree, the code wins and the document gets fixed.

## Topics

The commit that lands a subsystem's first code adds its directory and a row to this table.

| Topic | Scope |
| ----- | ----- |
| [identity](identity/README.md) | Identifiers for nodes, people and endpoints, public keys, identifier derivation and the bech32m text form; the node's keys on disk: files and formats, permissions, certificate serial and renewal, clock record, `init` rules, the self-revocation export |
| [trust](trust/README.md) | Direct and inherited trust, online vouchers and the grace period, local revocation, eviction, offline revoke directives |
| [policy](policy/README.md) | The node-local access policy: rules, allow/suspend/reject outcomes, reply permission, endpoint declarations, and tighten/loosen classification of policy changes |
| [administration](administration/README.md) | Who may administer a node: authorized-persons list, levels, operation catalogue, offline directives, management request semantics |
| [delivery](delivery/README.md) | Delivery states and transitions, deadlines and retention, pull leases and acknowledgements, deduplication, sender feedback. |
| [handoff](handoff/README.md) | Wake-up prompts, blocked-delivery detection, what one `recv` prints (output budget, paging, parts) and the check before acknowledging |
| [records](records/README.md) | The signed-record envelope: encoding, signature domains and digest, strict decoding, descriptors, partial signatures, `Peek`; every record kind defined so far: network-key and device certificates, trust records (credential, revocation, voucher, offline directive), publication records (declaration, reachability record, reachability proof) |
| [pairing](pairing/README.md) | Establishing trust between two nodes: the commit-reveal-open protocol and its six-digit code, the order of signing, the result exchange, the reveal budget, pause and cap, expectations, and what a stranger can and cannot do |
| [timing](timing/README.md) | Time bounds every node applies identically: the clock tolerance shared by trust, delivery, administration, record issuing and the node's wall-clock rollback judgment |
| [preauth](preauth/README.md) | What a peer may do before and after the certificate exchange: connection states, the protocol access table, the exchange messages, the state decision and the connection accounting |
| [envelope](envelope/README.md) | The message envelope, the signed status report and control reply, the single handling of a control reply on the sender, the digest used for deduplication, and reply permission |
| [adapter-protocol](adapter-protocol/README.md) | How the daemon talks to adapters: the peer layer (line framing, queues, overload, cancellation, write timeout, the effect rule of error responses), the JSON-RPC methods, version negotiation, error codes, control signals, native process identity, the package manifest, the schema that other languages implement, and the mapping to the Agent Client Protocol |
| [adapter-claude](adapter-claude/README.md) | The Claude adapter's session-creation state machine: crash-safe start, retry and readiness without a second process or session |
| [layout](layout/README.md) | Where code lives: contract packages and runtime packages, the import directions between them, and the rules that make the runtime's use of time, files and randomness checkable |
| [storage](storage/README.md) | How a node writes bytes that survive a crash: the file system interface and the durable write sequences, failure classes, per-platform behaviour, the crash model of the test file system and what it can and cannot prove, the transaction layer (declarations, commit point, deferred groups, cleanup, coverage), and the records of `state/` (record format and its validation, quarantine, bodies, archive, sequence floors, retention and the volume it implies, recovery rules, cold start) |
| [configuration](configuration/README.md) | What the owner of a node decides and where it is kept: node settings, the access policy, presets with their statement about local sources, adapter instances and endpoint settings, stable endpoint ids, kept revocations, adapter package layout; strict decoding, expected versions, the transactions that change them and their crash states |
| [ci](ci/README.md) | What the GitHub Actions workflow checks on which platform, what is deferred until the first release and why, and how to run the same checks locally |
| [local-ipc](local-ipc/README.md) | How the Agent and management interfaces are reached on one machine: Unix sockets and Windows named pipes, directory and socket permissions, peer checks on both sides, socket path limits, stale socket removal |
| [engine](engine/README.md) | The single writer of the node's runtime state: the loop and its commands, phases and submitters, failures and back pressure, the schedule, the registration and closing of endpoints with the cap and the tokens, the capacity check, and the two recovery entries |

Language: everything in this directory is English.
