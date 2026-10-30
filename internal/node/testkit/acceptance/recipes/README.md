# Acceptance recipes

Read the parent [README](../README.md) first. `New(Bindings)` builds a `Catalog`
without creating resources or invoking callbacks. Construct it before every run:
closures remember returned message identifiers only for that run.

`ScriptRecipe` owns the ordinary `scenario.Script`, its repeatable `Drain` and
its final domain `Check`. `TerminalRecipe` owns stop and retirement lifecycle
judgements outside ordinary scripts. `StartupRecipe` describes actual Init or Start, including
optional advances before Start and after each successful Start has quiesced. Only a successful Start leaves the live `complete` boundary; Init and terminal
requests have no after-return boundary. Names, steps, budgets and recovery responsibilities
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

Each feedback phase owns a separate zero-value private `feedbackDrain`. It
issues two one-minute advances, remembering issuance before calling `Advance`
and counting a step only after `Quiesce` succeeds. A crash during an advance's
response or its resulting writes therefore resumes quiescence at the selected
time instead of advancing again. Repeating completed waits could otherwise pass
a creation's reply deadline and create additional feedback during cleanup.
Different phases, including those sharing a sender and evidence object, retain
their own two minutes. After both steps, every reentry still calls real
`RecvAndAck`: an interrupted receive or acknowledgement is unfinished work.

`Bindings.LocalNode` supplies the actual node identity. `newDeliveryEvidence`
constructs every recipe's evidence object; `NewDeliveryEvidence` also permits
independent counterexamples. The first receive resolves and caches the identity.
Neither direct-agent `Me` nor the notice being checked supplies that identity.

`delivery-register-direct` registers through `Env.Direct` and repeats the same key
to require the same address. `delivery-register-declare` creates a real session
through its direct sender, resolves the public creation reply against external
native creation evidence, and checks `Declare(hidden)` through that session's
`Me`. `delivery-second-check` keeps a direct sender and creates its receiver the
same way. Its message is queued before the session narrows its accepted senders;
a completed narrowing requires the exact rejection notice. A cut before narrowing
still owes the accepted body. Both session drains finish a returned creation
before receiving, without declaring an operation the interrupted script never
reached. Their fresh main-point counts are 7, 52 and 100 respectively, with fixed
budgets of 11, 78 and 150.

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
check recovery work, queued or accepted deliveries and control records, and the original
quarantined creating session's closed state and cleared token.

The installation recipes share typed actions and final installed-version checks.
The ordinary recipe installs, upgrades and reinstalls the current version, then
rejects a package for another platform, without filesystem faults. Its interrupted
staging and unstaging supply adapter-garbage and transaction-trash recovery cases.
The rollback recipe observes two recoverable pointer failures with successful and
failed version cleanup, then retries through the real stale-version discard. Only
that recipe requires the pointer-failure capability. Every observed success is
replayed and checked against its stored result, installed current pointer and
version directories. Package specifications describe caller-owned input files;
no recipe writes node state. Each recipe records 74 main points and has a fixed
budget of 111.

Management recipes cover versioned node, preset and endpoint changes; a stale
version is refused without changing the setting. The maintenance recipe enables
an installed, disabled warning adapter inside its first step and waits for its
real probe to report a runtime version outside the manifest's tested range.
`ManagementOps.CreateVersionAlert` returns the resulting owner alert's actual
identifier. `RequireVersionAlert` judges all owner-alert facts together: exactly
one alert, with kind `version_untested` and the enabled instance as subject. The
same request identifier and expected version survive interruption; repeating the
drain resumes the real enable and probe instead of raising another alert.
The remaining actions cover acknowledgement, backup confirmation, clearing a
real clock rollback pause,
endpoint closure, and adapter disable/enable with pending messages and control
work. Named callbacks return real results after checking the same request's
persistent replay. The recipe then compares the result with the requested effect.
An interrupted request is retried with its original identifier and expected version.

`Catalog.Terminals` contains stop and identity retirement, with stable request
identifiers and fixed budgets. Bindings execute the real management request and
observe actual exit. `TerminalRecipe.Check` consumes pure `TerminalFacts`,
including the original response, independently read original durable result,
verified node-bound retirement, and actual restart outcomes. A confirmed answer
requires its original matching durable result; later replay cannot repair that
evidence. An unanswered interrupted retirement may have no operation result:
only verified irreversible retirement plus an actual identity-retired recovery
refusal and unchanged signing files can discharge that case. No response or
result is inferred from the retirement effect.

A restart after a selected crash remains a real walk generation, including a
retirement refusal. Once the chain is terminal, `TerminalAudit` records an
additional verification attempt separately. Stop requires successful startup,
`operation.result` and the same request's actual persistent replay. Retirement
requires identity-retired rejection without signing changes. Audit trace and
exit facts are logged and checked against the full table, and failures become
violations; audit hits supply no coverage or targets.
The instance recipe keeps its sender in another instance, drains actual loss
feedback, and checks that disabled endpoints are closed with empty tokens.
Its normal record writes the instance head; recovery may also write normal
transaction roles, whose observation source is reported separately.

The management recordings have 16, 44, 4, 7, 63 and 138 main points for settings,
maintenance, stop, retirement, endpoint close and instance disable/enable. Their
fixed budgets are 24, 66, 6, 11, 95 and 207 respectively.

The instance-management recipe also records a crash after the disabled
configuration is stored but before its first endpoint rewrite. Recovery then
closes the instance and drains its pending delivery and control records in the
same Normal transaction. Its extra trigger owns no Recovery declaration; the
observed Normal difference from all clean recordings determines the additional
interruptions.

A delivery that remains correctable may end the drain as `delivery_unknown`; its
matching actual system notice is evidence of the uncertainty. Instance management
retains the returned creation request and judges its actual result. An unknown
creation still requires the exact `delivery_unknown` notice for its message and
runtime target. Recovery before disable commits can instead finish creation;
`DeliveryEvidence` then requires the delivered public creation reply and the
returned session endpoint's matching external creation attempt and native
reference. Both branches retain the pending-message evidence, disabled instance,
zero open endpoints and closed-record audits. Final audits reject queued or
accepted work and never infer feedback from disk state.

The periodic-clock startup advances one minute while the actual daemon serves,
then waits for its own periodic task and stops. Its final read-only check requires
the persisted high-water mark to retain that advance without a rollback pause.
The recording has five main points and a fixed budget of eight.

The reply-after-write-failure variant uses one recoverable source-record create
failure before its visible replacement. The target has really accepted and read
the body while the sender still reports it queued; a real reply then carries the
evidence that releases the sender's retained body. `DeliveryOps` names this
preparation fault without exposing a file system or a transport to the recipe.
Its 68-point recording has a fixed budget of 102.

Recovery creation keeps its first native turn working while the initial message
becomes readable. The repeatable drain receives and acknowledges that real body
before explicitly ending the turn. Ending it earlier races an idle-triggered
prompt against the drain and changes the recording. A repeated drain may find the
turn already ended and accepts only the named `ErrNoTurn`; other failures remain
errors. The final domain check rejects an extra prompt or an unfinished turn.

A synchronous unknown-preset refusal includes the failed control reply in the
first receipt, releasing the source's initial body in the control-reply write.
The adapter-refusal and unknown-preset variants have 53 and 28 main points, with
fixed budgets of 80 and 42. The adapter-refusal variant declares its typed
`CreateRefusal` through `Env.SetAdapterInputs` before sending the creation, so
replacing the adapter cannot discard that external condition. Its three steps
remain refusal, creation and reply pull. Only after its real feedback drain
succeeds does it explicitly clear the declaration. The final check still requires
the failed control reply, actual feedback and no native-created ledger entry.
The unknown-preset variant has no adapter input declaration and keeps its two
steps. A held creation past its report window must return
`unconfirmed/ready_timeout` naming a closed session with no token. Its control
signal can be delivered while its separate initial-message delivery is still
unknown: the exact `accepted_unconfirmed` notice must name the derived session,
not the runtime control target. If a restart completes creation before the deadline
phase began, the actual created reply must resolve to native creation evidence and
the original initial body received and acknowledged at that session. This does not
replace the timeout judgement once its deadline phase started. The creation-expiry
budget is 122 from 81 points.

An absent-node delivery exercises the source's report deadline without a
transport knob. The real sender receives its exact `expired_unconfirmed` notice;
the source cannot be treated as expired merely because no call was answered at
the earlier delivery deadline. Its repeatable drain completes a returned send's
report-window advance if the script was interrupted before it, and remembers that
clock move before waiting for its resulting writes. A second crash cannot repeat
the four-hour advance past unread feedback. Its 44-point recording has a fixed
budget of 66.

The retention recipe reads a created session's initial body, sends and reads a
message from it, and closes the native session. Actual periodic sweeps must leave
intact archive summaries after the live records, bodies and closed endpoint are
gone, then remove those files after archive retention. `MaintenanceOps` returns
only these final facts from a read-only audit. A drain interrupted before the
sweep resumes from external creation results, receives and acknowledges pending
session messages, and completes their source settlement before retention. If an
unkeyed creation committed without returning its identifier, the private creation
owner uses a unique real create-attempt/native association only to locate a
session reader. It reuses `DeliveryEvidence.RecvAndAck` and recovers the identifier
only from that endpoint's actual initial-body sighting, matching the exact sender
and body digest. A prior sighting from the same run remains valid after its body
was acknowledged. Missing native or body evidence leaves the outcome unknown;
distinct associations or identifiers fail as ambiguous. The original creation-pull,
public Status and creation resolver still require a real created reply naming the
same endpoint and native. The owner retains receipt provenance: an unanswered
send gains no fabricated successful-send obligation, while returned creations
retain their original body obligation. Other recipes' drains are unchanged. Each
large clock move is remembered before its writes can be interrupted. The resumed
drain also runs one further ordinary sweep period: restart rearms the periodic
task, and the pass removing a session's last source deliberately leaves its
endpoint for the following pass. The original endpoint-removal and archive-expiry
requirements remain mandatory. The ordinary initial read and pre-close drain share
one private phase, completed only after the real receive and acknowledgement
succeed. Interrupted reads remain unfinished. A close may commit without returning
its result and recovery can already close the native; a completed read is therefore
not repeated through that closed session. The drain still repeats the identical
`retention-close` request and retains its identifier only after a successful real
response. Its 164-point recording has a fixed budget of 246.

A separate control-expiry variant fails the first create-result replacement at
its recoverable create point. The control reaches its report deadline before a
reply can commit, so it closes unconfirmed and the actual sender must read the
exact uncertainty notice for the runtime target. The owner closes the alerted
creating endpoint through management. Final checks require its unique real
`control_unknown` alert, cleared token, no native creation and no active records.
This is distinct from a timeout reply that was committed successfully. Its
71-point recording has a fixed budget of 107. A cut during the deadline step,
before its closing tick commits, can instead let restart retry the failed result
write and deliver an exact `unconfirmed/ready_timeout` reply naming the allocated
session. That reply proves the uncertain result without an additional notice;
the unique owner alert, no native creation and closed-session audit still apply.
The recipe remembers both the clock move and whether the script's deadline step
fully quiesced. Completing that step still requires the strict no-reply result;
repeating a drain never advances the four-hour window again.

A cut before the deadline phase may let restart finish creation. That branch
retains the original control result and
uses the shared creation judgement to require its delivered reply and matching
external native creation. The original request has no initial body. Its drain
receives through that actual session before closing it through management, and
requires the same native session's external close, the closed endpoint with no
token and no active records. Repeating the drain accepts the real management
result that the endpoint is already closed.

Body-cleanup variants send a nonempty message to an absent node. Their clean
recordings require the exact deadline notice. An additional offline-damage
recording flips bytes only in the source body the product actually retained;
the next process discovers the mismatch through its normal lazy read. Public
Status distinguishes that lost source and requires its exact `lost_at_sender`
notice, including the original identifier and target. A read-only binding audit
also requires the corresponding `corrupt_body` owner alert.

One variant lets feedback clearing remove the retained body. The other uses
`DeliveryOps.RetainLostSourceBody` to fail the existing feedback-clear and tick
source creates before replacement, preserving the real lost record for the
periodic archive sweep. It reads and acknowledges the loss notice before closing
its sender through management, then requires intact summaries and no live records
or bodies after retention. Successful receive/acknowledgement and its feedback
judgement complete a separate reporting phase before closing. An interrupted
close repeats the same request without reading the now-closed endpoint again.
Only a real response with no error code and `closed` or `already_closed` completes
closing: the durable effect can precede its stored result, so the retry need not
be a replay. Successful fault preparation is remembered across
drains: these faults belong to the process in which they were installed. After a
crash, startup may already replay and clear the feedback before the drain resumes,
so installing the old rules again would require an action that no longer exists.
The restarted process instead performs ordinary recovery. The drain remembers
completed clock advances so a second crash cannot advance past the unread notice's
lifetime. These are complete
recovery recordings; their Normal observations remain separate from clean ones
and supply actual additional interruption targets. The clean recordings have
44 and 65 main points, with fixed budgets of 66 and 98.
A send interrupted before returning creates no guessed message or summary
obligation. Its bounded drain still receives actual feedback, closes the sender
and requires all live records and bodies to be gone. Summary presence is required
only for a returned message with `ReplayFresh` whose resolved target and public Status confirm
the actual accepted delivery.

Recovery creation retains each successful control result. `DeliveryEvidence`
reads its public status and validated creation reply, associates the reply's
session with external creation-attempt evidence, and requires the requested
initial body at that session or its actual terminal feedback. A control without
a reply must have a terminal public result and matching runtime feedback. These
checks precede the additional disk-convergence audit. The shared creation-result
reader also serves the ordinary session resolver; resolved body evidence remains
valid after retention removes the source record.

An interrupted script owes only operations it actually reached. The session
declaration is checked only after its successful call. The blocked-session drain
finishes a blocking period only when that phase began, then restores delivery;
both blocked notices remain mandatory for a completed blocking phase. A send
before that phase still owes its accepted body. The lifecycle drain receives and
acknowledges pending session bodies unless the external ledger records an actual
close by that exact native runtime. A close attempt alone does not establish that
the reader is unavailable. Native closure selects the reader only; outstanding
deliveries still require real body receipts or matching closure feedback, and a
returned close identifier still requires its actual reply and closed-session audit.

The blocked-session recipe owns a complete `fakeadapter.ExternalInputs`
declaration through `Env.SetAdapterInputs`. Its prompt refusal and authentication
availability survive adapter generations without changing the product's rebuilt
connection state. The availability `SinceMS` is captured once from the real
ledger before the send and reused unchanged. `restore-prompt` clears only the
refusal; `restore-credentials` clears availability. An interrupted blocking phase
finishes before the drain explicitly clears both inputs, so startup cannot
forward the pending prompt before the strict blocked/recovered obligations are
established. The fourteen steps, clock advances and 285-point budget are retained.

`Needs` declares runtime-adapter use, extra instances, sweep period, package input,
an installed but disabled probe-warning adapter (`ProbeWarning`), offline damage
kind and each required filesystem fault. `Capabilities.Supports`
and `Catalog.Select` choose recipes before execution. `BinaryCapabilities`
includes probe-warning input and offline damage on the stopped real disk, with no
filesystem fault injection. Unknown damage and filesystem-fault kinds are never
supported, even if a capability list contains the same unknown name.

`Catalog.AnalyzeLayer` derives observed reachable keys from all complete clean and
recovery records of supported recipes. A key seen by any supported source is
reachable, even when another source requires a fault. `InProcessOnly` is the
full observed set minus that reachable union. Recovery-only Normal targets are
recomputed from the supported recordings, keeping each actual source recipe,
complete trigger and point ordinal. A reachable key does not make an unsupported
source's trigger reachable.

`RecoveryOps.InspectDamage` returns the actual damaged endpoint/body identity and
read-only alert counts. Recipes require an owner alert after record quarantine,
control uncertainty and a closed session after creation-control damage, and one
matching `corrupt_body` alert after source-body damage. These judgements run in
both layers. Recovery and extra-trigger recipes explicitly invoke the shared
closing audit through `RecoveryOps.CheckClosedRecords`.

Failed-creation drains share their recipe's `DeliveryEvidence` across restarts;
the final check validates those accumulated notifications before inspecting the
control reply and external absence of a native creation.

The engine fixture shares the daemon's durable clock lifecycle. Recipes that
advance time therefore include the actual minute saves, and maintenance includes
the immediate pause and post-clear saves. Re-recorded main-point counts and fixed
budgets for the remaining affected recipes are:

| Recipe | Main points | Budget |
| --- | --- | --- |
| `delivery-expiry` | 60 | 90 |
| `delivery-redelivery` | 120 | 180 |
| `session-lifecycle` | 216 | 324 |
| `session-blocked-recovered` | 190 | 285 |
| `recovery-send`, `recovery-source-trusted`, `recovery-source-unreadable`, `recovery-target` | 48 each | 72 each |
| `recovery-create` | 71 | 107 |
| `recovery-creating` | 75 | 113 |
| `recovery-body-feedback` | 44 | 66 |
| `recovery-body-archive` | 65 | 98 |

Unaffected budgets retain their existing values. Counts include all main targets;
clock writes are never filtered out, and domain judgements are unchanged.
