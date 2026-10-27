# adapterreg

The daemon's pure decisions about adapters: whether an adapter's declaration is
acceptable, which endpoint kinds and control signals it gets, which methods are
valid for which kind of endpoint, and how what an adapter reports maps onto the
contracts of `delivery` and `handoff`. Read [../README.md](../README.md) first for
the conventions every contract package follows, and
[the adapter-protocol reference](../../docs/references/adapter-protocol/README.md)
for the reasons behind the rules.

The messages, the manifest and the version negotiation are defined in the SDK
module `sdk/go` (packages `adapterproto` and `manifest`), which the daemon shares
with adapters; the rules of a JSON-RPC frame, the error codes below 1000 and
the effect rule of an error are those of its `peer` package. This package takes values the protocol layer has decoded; it does
not frame, send or store anything.

## Entry points

| Name | Decides |
| ---- | ------- |
| `Admit(capabilities, manifest)` | Whether an adapter is admitted, and the `Admission` that records what it may do. Refusals are an `*AdmitError` with a typed `AdmitReason`. |
| `Admission.Declares`, `DeclaredKinds`, `Signals(kind)`, `AcceptsSignal` | The endpoint kinds the adapter may own and the control signals each accepts. |
| `Admission.Budget(frontend)`, `Markers()` | The output budget of `recv` per front end (the default when the manifest gives none) and the compiled acknowledgement markers. |
| `Admission.PartBudgets(kind)` | The budgets a long message is cut for on an endpoint of that kind, whichever front end reads it: those of every front end the endpoint can be read through, so that every front end cuts it in the same parts. |
| `Admission.RecvWait(frontend)`, `DefaultRecvWait` | The longest an agent's `recv` may wait through an agent front end (`cli` or `mcp`): the manifest's limit, or `DefaultRecvWait` (8 seconds) when it gives none. It does not apply to direct endpoints. |
| `MethodAllowed(kind, method)`, `RequirementOf` | The method requirement matrix: forbidden, optional or required per kind. |
| `Admission.Check(kind, method)` | The error code to answer a call the adapter made with (`0` when it is acceptable): method not found (unknown, or a method only the daemon may call), wrong endpoint kind, or a capability not declared. |
| `CheckHandshake(sent, result, manifest)` | That the adapter reports the manifest's id and version, and a protocol version in both ranges. |
| `PromptOutcome`, `PromptOutcomeOnError`, `ReconcileOutcome` | A prompt's stage, the error a `prompt.deliver` call returned and a reconciliation verdict as `handoff.AttemptOutcome`. |
| `Signals(conditions, endpoint, received, offline, offlineSince)` | An availability snapshot as `handoff.Signals`, with `Since`. |
| `BlockedCause(handoff.Cause)` | The cause of a blocked delivery as the delivery rules record and tell it. |
| `StatusConditions(...)` | The `conditions` of a status reply, from the snapshot and the daemon's own observations, `version_untested` among them. |
| `RunsOn(manifest, goos, goarch)` | Whether the manifest lists the platform: the one judge of whether a package may be installed on this machine. |
| `RuntimeVersionUntested(manifest, runtime)` | Whether the probed runtime version is outside every tested range of the manifest: the one judge of the status condition `version_untested`. |
| `CreateReply`, `CreateUnconfirmed`, `InterruptReply`, `CloseReply` | Adapter results as the reply to a control signal. |
| `DaemonCreateFailure(DaemonCreateReason)` | The reply to a `session.create` the daemon refuses itself, before calling the adapter: preset unknown, preset closed to local sources, adapter offline, node endpoint limit. |
| `ItemAck`, `AckReason`, `ActionOf` | Names of `delivery` and `handoff` values and of authorization actions as the protocol spells them. |

## Rules

- **Endpoint kinds.** Declaring a runtime brings two kinds, `runtime` and
  `session`: session endpoints exist only through a runtime. Declaring direct
  endpoints brings `direct`. Declaring neither is refused, because the adapter
  could never own an endpoint. A capability flag cannot be declared without a
  runtime, because the flags are part of the runtime declaration.
  `EndpointKind.TargetKind` gives the target kind authorization uses; a value
  that is not a kind gives the zero one, which authorization refuses as
  malformed.
- **Signals** an endpoint accepts: runtime: `session.create`, `endpoint.status`;
  session: `message.send` (delivered as a prompt), `session.close`,
  `endpoint.status`, and `session.interrupt` when the adapter declared interrupt;
  direct: `message.send`, `endpoint.status`. A signal that authorization allows
  but the endpoint does not accept is answered by the daemon as unsupported,
  without calling the adapter.
- **Budgets.** A manifest budget for `cli` or `mcp` needs a runtime that declares
  that front end, and one for `direct` needs direct endpoints; otherwise the
  adapter is refused. A front end without a budget uses `handoff.DefaultBudget()`. A
  message is cut for the budgets of all the front ends of the runtime (`PartBudgets`),
  the default counting for one that declares none.
- **Receive wait.** A limit applies to the agent front ends, `cli` and `mcp`;
  the manifest refuses one for `direct`. A limit for an agent front end that no
  declared runtime lists refuses the adapter, as a budget does
  (`ReasonRecvWaitFrontendUndeclared`). `Admission.RecvWait` returns the declared
  limit, and `DefaultRecvWait` for a front end without one, an unknown or direct
  front end and the zero `Admission`. A direct endpoint's wait is chosen by the
  adapter in the `wait_s` of `endpoint.recv`, capped by
  `adapterproto.MaxWaitSeconds`; `RecvWait` does not apply to it. The default
  is 8 seconds because the shortest known client timeout is 10: Codex returns its
  default unified-exec call after 10 seconds with the command still running, and
  its one-shot mode kills the command then, so 8 seconds leaves 2 for the
  connection and the output. A page returned after the client gave up is not seen
  by the agent, but nothing is lost: the message stays leased and comes back,
  marked as a possible duplicate, when the lease expires. A wait that is too
  short only makes the agent ask again. `manifest.MaxRecvWaitSeconds`, 120
  seconds, is the longest an agent's `recv` may wait whatever a manifest
  declares; the manifest rejects a limit above it, so `RecvWait` never exceeds
  it.
- **Markers** are compiled by `handoff` here and nowhere else; a marker that is
  empty, does not compile or matches the empty text refuses the adapter.
- **Methods.** The matrix is one column for runtime and session endpoints and one
  for direct ones, except that `recv.observed` concerns session endpoints only.
  The `endpoint.*` methods are forbidden on session endpoints: their messages
  must be read by the model itself, and an adapter reading them for it would
  swallow them. `recv.observed` and `native_request.handled` also need the
  capability that enables them; `session.interrupt` is the daemon's call, so
  whether it is sent is `Signals` and `AcceptsSignal`, not `Check`.
- **Direction.** `Check` judges calls the adapter made. A method that only the
  daemon may call (`session.create`, `prompt.deliver`, `initialize`, ...) is
  answered `-32601`, as an unknown method is, whatever the kind and the
  declaration: it does not exist in that direction.
- **Errors and attempts.** The error of a `prompt.deliver` call is judged by the
  effect rule of `sdk/go/peer`, which this package restates only where it
  differs: when the rule proves the request had no effect the attempt failed,
  otherwise (`-32603`, `-32800`, a missing response, a timeout, a closed
  connection, anything the rule does not know) the attempt is unknown, to be
  reconciled. The difference is `1005 attempt_conflict`: the attempt identifier
  is already known with other parameters, so an earlier request of the same
  attempt reached the adapter and may have delivered; the attempt is unknown.
- **Tested runtime version.** The adapter's probe result reports only the
  runtime's name and version; the daemon alone compares them with
  `manifest.Manifest.Tests` (`RuntimeVersionUntested`) and writes status
  condition 6 itself. An adapter cannot report it, because a second judge could
  disagree with the manifest it was admitted under. It is no availability cause
  and blocks nothing. No probed runtime makes no claim; a manifest without
  tested ranges contains no version, so any probed version is untested.
- **`Since` of a cause** is unknown (zero) unless every condition of the winning
  cause reports a start that is not later than the moment the daemon received the
  snapshot; the adapter's clock is for reference only.

## What the caller must guarantee

- Values come from `adapterproto.Decode` or have been validated; the mappings
  return `ErrUnmapped` for a value outside the protocol rather than guess.
- The caller resolves a call's endpoint to its kind first (unknown endpoint is
  `1001`) and passes that kind to `Check`.
- `received` and `offlineSince` are the daemon's own observations, in UTC.
- A zero `Admission` declares nothing and accepts no call; it is what a caller
  holds for an adapter that has not been admitted.
