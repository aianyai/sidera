# admin

Who may administer a node. Read [../README.md](../README.md) first for the
conventions every contract package follows, and
[../../docs/references/administration/README.md](../../docs/references/administration/README.md)
for the reasoning behind the rules.

## Entry points

| Function / type                                                         | Purpose                                                                                                   |
| ----------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `NewList(entries) (List, []ListProblem)`                                | Build and validate a node's authorized-persons list.                                                      |
| `EffectiveLevel(list, person, cert, now) Standing`                      | A remote person's rights now: the lower of the list level and the certificate cap.                        |
| `Authorize(list, actor, op, now) Verdict`                               | Allow or deny one operation, with a `Reason`.                                                             |
| `RequirementOf(op) (Requirement, bool)`                                 | The operation catalogue: which level, or the local owner alone, an operation needs.                       |
| `AcceptDirective(local, list, d, offlineSince, now)`                    | Execute, reject, or only warn about an offline directive.                                                 |
| `MayIssueReachabilityProof(list, person, notAfter, now) ProofVerdict`   | May this node issue a reachability proof to this person, valid until `notAfter`.                          |
| `CheckReachabilityProof(p, node, requester, now) ProofVerdict`          | Does this proof let this requester query this node's reachability records now: it names this node and requester, its issuer is not retired (`RetirementStatus`), and it has not ended. |
| `NewOperationID(r io.Reader) (OperationID, error)`                      | A random operation ID from an injected source; all-zero is refused.                                       |
| `KeyFor(actor, id) RequestKey`                                          | The key under which an actor's results are filed; the operation ID is scoped to the person.               |
| `ValidateRequest`, `CheckReplay`, `CheckVersion`                        | Shape of a management request, same-ID-same-result rule, version-conflict check.                          |

Actors are `LocalOwner()` or `RemotePerson(personID, DeviceCert)`.

## Operation levels

| Needs         | Operations                                                                                                                                                                                                                  |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| view          | `OpRead`, `OpQueryOperationResult` (the requester's own results).                                                                                                                                                           |
| manage        | Revoke trust; locally revoke a node; reject a trust request; disable inheritance; shorten the grace period; tighten policy; remove or narrow a preset; manage endpoints and sessions; edit declaration fields; relay settings; switch LAN discovery on or off; issue an offline directive. |
| full-control  | Confirm trust establishment; enable inheritance; extend the grace period; clear a locally-revoked record; install or upgrade an adapter; add or loosen a preset; loosen policy. A policy change is manage only when the policy package classifies it `Unchanged` or `Tighten`; `Loosen`, an unset or an unknown classification, and an unclassified preset change count as loosening. |
| local owner   | `OpModifyAuthorizedList`.                                                                                                                                                                                                   |

A batch local revocation is the CLI sending `OpLocallyRevokeNode` to each node;
every node answers with its own verdict from its own list.

## Invariants

- Only the local owner changes the list. `OpModifyAuthorizedList` requires
  `RequireLocalOwner`, which no list level satisfies; full control is no
  exception. A requirement this code does not know is treated the same way, so
  it is denied to every remote person.
- Effective level is `min(list level, certificate cap)`. A person who is not on
  this node's list, whose entry or certificate has expired, or whose
  certificate or root key is revoked, or whose revocation status is unchecked (the
  zero value of `DeviceCert.Revocation`), has no rights, whatever they can do on
  other nodes. Expiry takes effect at the expiry instant; a certificate is valid
  from its `NotBefore` instant.
- One criterion places every operation: what lets more nodes in or back, lets
  this machine run more code, or loosens policy needs full control; what only
  tightens or withdraws needs manage; reading needs view. A policy or preset
  change that is not classified as tightening or narrowing counts as loosening,
  and an operation outside the catalogue is denied to everyone. LAN discovery
  only finds candidates and admits no one, so it needs manage in both
  directions.
- An offline directive is checked in this order, and the first failure decides:
  it has a signing time that is not later than now plus the clock tolerance
  (`timing.ClockTolerance`) and not earlier than the moment this node lost
  contact minus the clock tolerance;
  it addresses this node; its kind is "revoke trust edge"; only then the
  signer's standing: on the current list with at least the level the catalogue
  requires for issuing a directive, after the certificate cap. A structurally
  valid directive from a signer who is no administrator here yields a warning to
  show the owner, never an action; a malformed one is rejected whoever signed
  it.
- This package does not decide which trust credential a directive revokes or
  whether that credential's edge involves this node. A directive names a
  credential digest, so a re-established edge, which has a new credential, is
  never cut by a replayed old directive. The caller runs `AcceptDirective`
  first and, on `DispositionExecute`, the trust package's applicability check
  second; only both together revoke anything.
- A reachability proof only allows querying reachability records. It grants no
  management right.
- A conflicting configuration version is never applied. An operation ID has one
  result: a resend of the same request gets the stored result; the same ID with
  different content is refused.

## Request handling order

The only order is: `Authorize`, then `ValidateRequest`, then `CheckReplay` with
the stored result filed under `KeyFor(actor, id)` (a stored result is returned
as it is, with no version check), and only when `CheckReplay` says execute:
`CheckVersion`, the execution, and storing the result together with the
request's fingerprint. `KeyFor` is called only for an actor that `Authorize`
allowed. `OpQueryOperationResult` reads a stored result under the requester's
own `KeyFor`, so nobody sees another person's results.

## What the caller must guarantee

- Signatures, certificate chains and revocation sets are already verified.
  `DeviceCert` carries only the verified facts (cap, validity window, the result of
  the revocation lookup); the caller sets `Revocation` to a checked outcome after
  looking up both the certificate and the root key, and leaving it zero is refused
  with `ReasonRevocationUnchecked`; a certificate that does not chain to
  the person's root key must never reach this package. A certificate that states
  no cap is passed with `MaxLevel` set to `LevelFullControl`; the zero value
  `LevelNone` grants nothing.
- `now` is UTC and comes from the caller's clock. Callers re-run `Authorize` for
  every request on a live connection, so removing or downgrading an entry takes
  effect on the next request. The connection of a person whose `EffectiveLevel`
  drops to none is closed by the re-decision `preauth` requires when the list
  changes (`Decision.Keeps`); the person reconnects as a non-member person.
- `offlineSince` passed to `AcceptDirective` is the start of the earliest
  offline period that is not yet synchronised with every directly trusted
  party, recorded by this node itself and never taken from a directive. The
  zero time is refused. A return to contact does not move it while some party
  was unreachable: the issuance gate lets voucher issuance through then
  (`trust.IssuanceState.Unreachable`), but a directive held by that party may
  still arrive, and a later offline period must not push the bound past it.
  It moves forward only when every directly trusted party has been
  synchronised, and it starts afresh with the next offline period after that.
- A request that touches several settings is authorized as every operation it
  contains and applied only if all are allowed. `OpEditDeclaration` covers only
  declaration fields that have no operation of their own; flipping inheritance
  through the declaration is `OpEnableInheritance` or `OpDisableInheritance`,
  and relay settings are `OpChangeRelaySettings`.
- `Operation.PolicyChange` is an `authz.Change`, the policy package's own
  classification of the change (`authz.Classify`). `Unchanged` and `Tighten`
  need manage; `Loosen`, the zero value (not a classification) and any value out
  of range need full control, so a caller that forgets the field never gets
  manage. Preset changes are classified by the caller (`PresetChangeClass`) from
  the preset before and after.
- `Fingerprint` is a collision-resistant digest of the whole request.
  Stored results are filed under `RequestKey` (person plus operation ID).
- The signature format of a reachability proof is outside this package;
  `ReachabilityProof` carries the verified facts only. One of them is the
  caller's lookup of the issuing node in the retirement records
  (`IssuerRetirement`): the proof is signed by the node's identity key, so a
  retired node's proof is void. The zero value `RetirementUnchecked` is refused
  with `ReasonRetirementUnchecked`; `revocation.Set.NodeRetirement` gives a
  checked status.
