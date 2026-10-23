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
with adapters. This package takes values the protocol layer has decoded; it does
not frame, send or store anything.

## Entry points

| Name | Decides |
| ---- | ------- |
| `Admit(capabilities, manifest)` | Whether an adapter is admitted, and the `Admission` that records what it may do. Refusals are an `*AdmitError` with a typed `AdmitReason`. |
| `Admission.Declares`, `DeclaredKinds`, `Signals(kind)`, `AcceptsSignal` | The endpoint kinds the adapter may own and the control signals each accepts. |
| `Admission.Budget(frontend)`, `Markers()` | The output budget of `recv` per front end (the default when the manifest gives none) and the compiled acknowledgement markers. |
| `MethodAllowed(kind, method)`, `RequirementOf` | The method requirement matrix: forbidden, optional or required per kind. |
| `Admission.Check(kind, method)` | The error code to answer a call with (`0` when it is acceptable): wrong endpoint kind, or a capability not declared. |
| `CheckHandshake(sent, result, manifest)` | That the adapter reports the manifest's id and version, and a protocol version in both ranges. |
| `PromptOutcome`, `PromptOutcomeOnError`, `ReconcileOutcome` | A prompt's stage, an error response and a reconciliation verdict as `handoff.AttemptOutcome`. |
| `Signals(conditions, endpoint, received, offline, offlineSince)` | An availability snapshot as `handoff.Signals`, with `Since`. |
| `StatusConditions(...)` | The `conditions` of a status reply. |
| `CreateReply`, `CreateUnconfirmed`, `InterruptReply`, `CloseReply` | Adapter results as the reply to a control signal. |
| `ItemAck`, `AckReason`, `ActionOf` | Names of `delivery` and `handoff` values and of authorization actions as the protocol spells them. |

## Rules

- **Endpoint kinds.** Declaring a runtime brings two kinds, `runtime` and
  `session`: session endpoints exist only through a runtime. Declaring direct
  endpoints brings `direct`. Declaring neither is refused, because the adapter
  could never own an endpoint. A capability flag cannot be declared without a
  runtime, because the flags are part of the runtime declaration.
- **Signals** an endpoint accepts: runtime: `session.create`, `endpoint.status`;
  session: `message.send` (delivered as a prompt), `session.close`,
  `endpoint.status`, and `session.interrupt` when the adapter declared interrupt;
  direct: `message.send`, `endpoint.status`. A signal that authorization allows
  but the endpoint does not accept is answered by the daemon as unsupported,
  without calling the adapter.
- **Budgets.** A manifest budget for `cli` or `mcp` needs a runtime that declares
  that front end, and one for `direct` needs direct endpoints; otherwise the
  adapter is refused. A front end without a budget uses `handoff.DefaultBudget()`.
- **Markers** are compiled by `handoff` here and nowhere else; a marker that is
  empty, does not compile or matches the empty text refuses the adapter.
- **Methods.** The matrix is one column for runtime and session endpoints and one
  for direct ones, except that `recv.observed` concerns session endpoints only.
  The `endpoint.*` methods are forbidden on session endpoints: their messages
  must be read by the model itself, and an adapter reading them for it would
  swallow them. `session.interrupt`, `recv.observed` and `native_request.handled`
  also need the capability that enables them.
- **Error responses and attempts.** An error response means the request had no
  effect (the attempt failed) except `-32603` and `-32800` (the attempt is
  unknown, to be reconciled). A missing response, a timeout and a closed
  connection are unknown as well; the caller records that itself.
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
