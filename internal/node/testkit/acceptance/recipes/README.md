# Acceptance recipes

Read the parent [README](../README.md) first. `New(Bindings)` builds a `Catalog`
without creating resources or invoking callbacks. Construct it before every run:
closures remember returned message identifiers only for that run.

`ScriptRecipe` owns the ordinary `scenario.Script`, its repeatable `Drain` and
its final domain `Check`. `StartupRecipe` describes actual Init or Start, including
an optional advance before Start. Names, steps, budgets and recovery responsibilities
have one source in these descriptions. Bindings contain named, typed operations
only for capabilities absent from `scenario.Env`; there is no generic dispatcher.

The catalog contains real initialization, certificate-renewing startup and a direct
send with receive and acknowledgement. A successful send must have its exact body
visible in the external ledger; accepting a message alone does not satisfy it.
The existing shared scenario judgements also apply, without waivers.

Budgets are fixed from recordings at `(3*n+1)/2`, where `n` counts full main targets
without setup or sync steps. They are checked on later runs, never recalculated to
make a growing script pass. The initial recordings have 55, 11 and 28 points for
initialization, renewing startup and send respectively.

Delivery variants cover evidence-bearing replies, declaration narrowing at the
second receive check, queue expiry and lease redelivery. `DeliveryEvidence` relates
each successful send's returned identifier and resolved recipients to body sightings
or a terminal system notice obtained through real receive. Notice checking has one
home here: it verifies the local system sender, the committed header and public kind
summaries, and exact original message and target references. Repeated reads of one
notice identifier are one logical notification; another identifier for the same
message, target and kind is a duplicate. Acknowledgements name only the parts just
returned by that receive, and their real outcomes must accept those parts.

Session recipes bind `SessionOps` for runtime addresses by explicit instance,
session agents, checkpoint writes and final closed-session checks. They exercise
creation with an initial body, failed and expired creation, interrupt, prompting,
checkpoint removal on close and blocked/recovered notices. External runtime and
agent evidence proves visibility; the final closed-session audit alone reads the
endpoint's status, token and remaining checkpoint facts.

Recovery descriptions explicitly own non-sync Recovery declarations and their real
first-crash or offline-damage trigger. `RecoveryTriggers` adds complete recovery
recordings without assigning a declaration; the binding deduplicates these with
owned triggers by recipe name and the entire trigger. Offline damage names select
existing source, target or creating-control records, or an existing empty layout
directory. Input preparation never manufactures recovery debris.

Recovery delivery variants use `DeliveryEvidence` for accepted-body obligations,
loss notices and acknowledgement evidence. Their drains release held adapter
requests and associate real creation requests with native creation confirmations
by attempt, then receive through the resulting session agent. Final disk audits
check recovery work, terminal deliveries and control records, and the original
quarantined creating session's closed state and cleared token.
