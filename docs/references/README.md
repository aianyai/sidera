# Engineering references

Living documents, organized by topic, committed with the code they describe. They are the one authoritative description of Sidera as built: its glossary and its decision record. When you have a question about a topic, read its directory first, not git history.

Each topic is a directory `docs/references/<topic>/` whose `README.md` is its entry point. Each document answers three things: **what** (the rule, protocol or format), **why** (the trade-off and the alternatives rejected), **where** (the code paths and tests). A document describes only what is committed and changes in the same commit as the code it describes. When a document and the code disagree, the code wins and the document gets fixed.

## Topics

The commit that lands a subsystem's first code adds its directory and a row to this table.

| Topic | Scope |
| ----- | ----- |
| [identity](identity/README.md) | Identifiers for nodes, people and endpoints, public keys, identifier derivation and the bech32m text form |
| [trust](trust/README.md) | Direct and inherited trust, online vouchers and the grace period, local revocation, eviction, offline revoke directives |
| [policy](policy/README.md) | The node-local access policy: rules, allow/suspend/reject outcomes, reply permission, endpoint declarations, and tighten/loosen classification of policy changes |
| [administration](administration/README.md) | Who may administer a node: authorized-persons list, levels, operation catalogue, offline directives, management request semantics |
| [delivery](delivery/README.md) | Delivery states and transitions, deadlines and retention, pull leases and acknowledgements, deduplication, sender feedback. |
| [handoff](handoff/README.md) | Wake-up prompts, blocked-delivery detection, what one `recv` prints (output budget, paging, parts) and the check before acknowledging |
| [timing](timing/README.md) | Time bounds every node applies identically: the clock tolerance shared by trust, delivery and administration |
| [adapter-claude](adapter-claude/README.md) | The Claude adapter's session-creation state machine: crash-safe start, retry and readiness without a second process or session |

Language: everything in this directory is English.
