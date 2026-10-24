# Administration

Who may administer a node: the authorized-persons list, the three levels, which
operation needs which level, offline directives, reachability proofs, and the
semantics of management requests. This document describes the code as committed. The
admission of people to the list, certificate and signature verification, and the
signed record formats are out of scope here.

## What

**Two independent kinds of authority.** Nodes trust nodes (bidirectional trust
with inheritance); people administer nodes. Administration is granted per person
by each node's own list and is neither inherited nor transferable. Who may
administer a node does not change whom it trusts, and the reverse.

**The list.** One per node, like SSH `authorized_keys`: entries of person ID
(digest of the root key), level and optional expiry. A list is valid only if
every entry has a non-zero person, a level of view, manage or full-control, and
no person appears twice. Validation reports every problem with its position.
Only the local owner changes the list; no remote person can, full control
included.

**Levels.**

| Level        | May do                                                                                                    |
| ------------ | --------------------------------------------------------------------------------------------------------- |
| view         | Read state, policy, endpoints, logs, diagnostics, and the results of one's own earlier requests.          |
| manage       | Withdraw or tighten (below) and manage endpoints and sessions, the declaration, relay settings, LAN discovery, issue offline directives. Choose only among adapters and presets already approved. |
| full-control | Everything above plus whatever lets more nodes in or back, runs more code, or loosens policy.             |
| local owner  | Everything, and alone: modify the list and the operations for managing a single node (below).             |

**Operation catalogue.**

| Needs         | Operations                                                                                                                                                         |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| view          | Read; query the result of an operation (the requester's own).                                                                                                      |
| manage        | Revoke trust; locally revoke a node; reject a trust request; disable inheritance; shorten the grace period; tighten policy; remove or narrow a preset; manage endpoints and sessions; edit declaration fields; relay settings; switch LAN discovery on or off; issue an offline directive. |
| full-control  | Confirm trust establishment; enable inheritance; extend the grace period; clear a locally-revoked record; install or upgrade an adapter; add or loosen a preset; loosen policy. |
| local owner   | Modify the authorized-persons list; enable an adapter instance; disable an adapter instance; change an endpoint's settings; change the node's settings; acknowledge an alert; clear the clock-rollback pause; confirm the revocation backup; retire the identity; export the revocation statement; stop the daemon (the operations for managing a single node). |

The operations for managing a single node are held to the local owner until
remote management exists: a level that cannot yet be exercised
remotely cannot be validated, setting it now would be a guess, and loosening it
later is a one-line change of the catalogue guarded by tests, while tightening
it after remote people depend on it would take their capability away.
Enabling and disabling an adapter, and changing an endpoint's and the node's
settings, are separate operations because their remote levels may differ once
decided (enabling lets the machine run more code, disabling only withdraws), and
splitting an operation later would change every call site.

Exporting the revocation statement needs the local owner, never view: the
statement voids this node's identity, so whoever can read it can end the node.
It is the one operation above view that is not side-effecting. It changes
nothing, so replay protection has nothing to guard, and the stored result of a
side-effecting request would copy the statement into the stored results and
widen where the secret lives, for as long as replay checks need to keep it. A
request for it therefore needs no operation ID, and its result is never stored.

A batch local revocation is not an operation of its own: the CLI sends the
single local revocation to each node, and each node answers with its own verdict
from its own list, so one person can be allowed on one node and refused on
another.

A policy change is classified by the [policy](../policy/README.md) package (`authz.Change`): `Unchanged`
and `Tighten` need manage, anything else (`Loosen`, an unset or unknown value)
needs full control. An unclassifiable preset change counts as loosening. A preset change
that removes or narrows a preset needs manage; adding or loosening one needs
full control. An operation outside the catalogue, a requirement the code does
not know, or an unknown kind of caller, is denied to everyone.

**Effective level.** A remote person presents a device certificate whose chain
the caller has verified. Their level is the lower of the list level and the
certificate's cap. A certificate that states no cap is passed as full control, so
only the list level limits the person; a zero cap grants nothing. They have no
rights if they are not on this node's list, the entry or certificate has expired
(at the expiry instant), the certificate is not valid yet (it is valid from its
`NotBefore` instant), or the certificate or root key is revoked, or nobody
looked the revocation sets up. Rights on other nodes are irrelevant. The denial
reason names the first failing fact, and distinguishes a list level that is too
low from a certificate cap that is too low.

**Revocation status is three-valued.** The caller states the outcome of the
revocation lookup in `DeviceCert.Revocation`: clear, certificate revoked, or root
key revoked. The zero value means nobody looked and grants nothing; it is refused
with its own reason (`revocation_unchecked`), checked before the validity window
and the list, so a forgotten lookup is never mistaken for an expired certificate
or an unlisted person. A status outside the known values counts as unchecked.
When both the certificate and the root key are revoked, the status is the root
key's, the wider fact, and that is the reason reported.

**Removal is immediate.** There is no cached grant: every request is judged
against the current list, so removal or downgrade applies to the next request.

**Offline directives.** While a node is offline, a person with manage or above on
its list may sign a directive to revoke one of its trust edges. When the node
returns it checks the directive in this order, and the first failure decides:

1. it carries a signing time;
2. the claimed signing time is not later than now plus the clock tolerance
   (2 minutes, `timing.ClockTolerance`, see [timing](../timing/README.md));
3. the claimed signing time is not earlier than the moment this node itself
   recorded as losing contact, minus the clock tolerance;
4. it is addressed to this node;
5. its kind is "revoke trust edge" (the only kind there is);
6. the signer's certificate and root key were looked up and are not revoked, and
   the certificate was valid at the signing time (from `NotBefore`, until
   `NotAfter` exclusive);
7. the signer is on the current list, unexpired, with at least the level the
   catalogue requires for issuing a directive (manage) after the certificate
   cap.

A well-formed revoke directive failing only point 7 (not listed, entry expired,
level too low) is from someone who is not an administrator here, so it is shown
to the owner as a warning and never executed. Failing any other point is a
rejection, whoever signed. Other nodes only forward directives.

Passing all points means this package's rules are met, not that anything is
revoked. This package does not decide which trust credential the directive
revokes or whether that credential's edge involves this node. The caller runs
this acceptance first and then the trust package's applicability check. A
directive names a credential digest, so that a re-established edge, which has a
new credential, is never cut by a replayed old directive.

**Reachability proofs.** An administrator of a node may be given a proof that
lets them query the node's reachability records. The node may issue a proof to a
person who is on its list with an unexpired entry, at any level, and only if the
proof does not outlive the entry: its end may equal the entry's expiry but not
exceed it. A proof is accepted for a requester when it names exactly this node
and this requester, was not issued by a retired node, and the present instant is
before its end. A proof grants no
management right: it opens reachability records and nothing else.

**Management requests.** Each side-effecting request carries an operation ID.
Configuration changes (inheritance, grace period, policy, presets, declaration,
relay settings, LAN discovery, enabling and disabling an adapter, endpoint
settings, node settings) also carry the version of the setting the sender
last saw; a different current version is a conflict and the change is not
applied. An operation ID has one result: a resend of the same request returns
the stored result, the same ID with different content is refused. IDs are scoped
to the sender, and the sender can query the result of its own operation (view).
Rejecting a trust request is a one-off decision on one request and carries no
version; neither do acknowledging an alert, clearing the clock-rollback pause,
confirming the revocation backup, retiring the identity or stopping the daemon,
which are state transitions and not edits of a setting.

The declaration operation covers only declaration fields that have no operation
of their own; the inheritance switch and the relay settings are excluded. A
change touching several settings is authorized as every operation it contains
and applied only if all are allowed, so flipping inheritance through the
declaration is `OpEnableInheritance` or `OpDisableInheritance`, never a manage
level edit.

The only order of handling a request is: authorize; validate its shape; then,
for a side-effecting request only, check replay under the sender-scoped key (a
stored result is returned as it is, with no version check, because the request
already ran), and only when it is to execute, check the version, execute, and
store the result with its fingerprint. A request that is not side-effecting is
executed after validation; it is never checked for replay and its result is
never stored. The key is built only for a sender that was authorized, so a
denied caller can neither file nor probe results.

## Why

- **One criterion for levels.** Anything that lets more nodes in or back
  (directly, or through inheritance, since members may create sessions with
  every approved preset), lets this machine run more code, or loosens policy
  widens what outsiders can make this machine do, so it needs full control.
  Tightening and withdrawing cannot be abused to do that and are what an
  operator needs in an emergency, so manage suffices. Applied to the harder
  cases: rejecting a trust request only declines to admit a node, so manage;
  removing or narrowing a preset only shrinks what sessions may do, so manage,
  while adding or loosening one needs full control; LAN discovery only finds
  candidates and admits no one, so manage in both directions. A rule per
  operation would drift; one criterion classifies new operations too.
- **Local owner alone edits the list.** Otherwise a stolen full-control key
  could add attacker keys that survive its own revocation. Full control can
  already run arbitrary code on the machine; the list rule is the one place the
  software can still hold the line and it keeps the owner able to remove anyone.
- **Minimum of list level and certificate cap.** A key issued on a machine the
  person does not fully trust must not be able to do more than that machine
  deserves, however high the person's standing is, and a high cap must not raise
  a low list level. Taking the minimum satisfies both.
- **Unclassified policy and preset changes count as loosening.** The policy
  classification has no valid zero value, so an unset field falls here. Refusing a harmless change
  costs a retry by someone with more authority; allowing a loosening one by
  mistake hands out rights.
- **Directive authority at execution, certificate window at signing.** The node
  may be offline longer than a short-lived key lives, so judging the key's
  expiry at execution would make directives from temporary keys useless exactly
  when they are needed; a signature made while the key was valid does not become
  forged. Authority is the opposite: a person removed or downgraded between
  signing and execution should not get one more action. Revocations count
  whatever their date, because a compromised key can claim any signing time.
  Alternatives rejected: checking both at signing time (cannot see later
  removals) and both at execution time (breaks short-lived keys). Whether a
  signer's list history at the signing moment should also matter is not
  modelled: the node only holds its current list.
- **Signing time bounded below by the node's own offline start.** Judging the
  certificate at the claimed signing time would let an expired temporary key
  claim a date inside its old validity window. The signature time is the
  signer's claim, so the only anchor the node can trust is its own record of
  when it lost contact; a directive meant for an offline node cannot predate
  that, up to the clock tolerance. The anchor is the start of the earliest
  offline period not yet synchronised with every directly trusted party, not
  the latest offline start: the issuance gate lets issuance through while some
  parties are unreachable, so a directive missed on the first return can still
  be held by one of them. A node that slept a second time before receiving it
  would otherwise measure the window from the second sleep and reject the
  directive as backdated. The anchor moves forward only once every directly
  trusted party has been synchronised. Residual risk, accepted: a stolen device key
  that is unexpired and unrevoked can still sign directives during the offline
  window; only revocation or expiry of that key limits it, and a directive can
  only cut an edge, which the owner can rebuild. A zero offline start is
  refused rather than read as "no bound", because silently dropping the check
  would be worse than a loud failure.
- **Structure before standing.** A directive for another node or of an
  unsupported kind is wrong whoever signed it, so it is rejected; only a
  well-formed revoke directive from someone without authority is worth showing
  to the owner. Checking standing first would turn malformed directives from
  unlisted signers into warnings.
- **The directive level has one home.** The level to issue a directive is the
  catalogue's entry for issuing one; acceptance reads it from there, so
  changing the catalogue changes what a node accepts.
- **Which edge is revoked is not decided here.** The directive names a
  credential digest and the trust package decides whether it belongs to an edge
  of this node. Two places judging edges would drift, and naming the credential
  rather than the two nodes means a re-established edge is never cut by a
  replayed old directive.
- **Unchecked is not unrevoked.** A boolean "revoked" flag whose zero value
  means "not revoked" lets a caller that forgot the lookup execute a directive
  signed by a revoked device key, which cuts a trust edge irreversibly. The
  status's zero value is therefore "unchecked", and an unchecked certificate is
  rejected for a directive (never turned into a warning) and denied for a
  request. One status rather than two flags: it is one lookup with one result,
  and "both revoked" needs no representation because the root key's revocation
  already decides.
- **Warn versus reject.** A suggestion from someone with no authority is not an
  attack to drop silently; the owner may want to see it. A directive that
  claims authority but fails (revoked key, wrong node, wrong kind, bad signing
  time) is discarded.
- **Directives only withdraw.** A remote signer's directive can never let a
  node in, so a mistaken or stolen manage key can at worst cut an edge, which
  the owner can rebuild.
- **Operation ID plus content check.** Returning the stored result for any
  repeated ID would confirm an operation the sender never asked for if an ID were
  reused by mistake; a fingerprint compare makes that a loud conflict. Version
  conflicts are reported instead of overwritten so two administrators never
  silently undo each other.
- **A retired issuer voids its proofs.** A proof is signed by the issuing
  node's identity key, and a self-revocation voids everything that key signed. A
  checker that read only the proof's end would go on answering with a record for
  a person on the list of a node that no longer exists. The retirement lookup is
  an input like the revocation status of a device certificate, three-valued so a
  caller that forgot it is refused (`retirement_unchecked`), and the rule lives
  here, not in each query handler.
- **Reachability proofs cannot outlive the entry.** A proof is bearer
  authority a person carries around; if it could outlast their list entry, the
  expiry on the entry would no longer bound what they can see. It grants no
  management right, so a leaked proof exposes reachability records only.
- **One request per operation, the order fixed.** Authorization comes first so
  a caller without standing learns nothing and files nothing; replay comes
  before the version check so a stored answer is not turned into a conflict by a
  later change; the version is checked only for a request that will execute.
  A batch local revocation is left to the caller as one request per node
  because each node's list is its own and a single combined verdict would hide
  that.

## Where

- `internal/admin/level.go`, `list.go`, `actor.go`: levels, list, actors,
  `EffectiveLevel`.
- `internal/admin/operation.go`, `authorize.go`: catalogue, `RequirementOf`,
  `Authorize`. Every kind is declared above the unexported end sentinel
  `opEnd`, which the exhaustive test iterates up to.
- `internal/admin/directive.go`: `AcceptDirective`.
- `internal/admin/reachability.go`: `MayIssueReachabilityProof`,
  `CheckReachabilityProof`.
- `internal/admin/request.go`: operation IDs, versions, replay, `KeyFor`.
- `internal/admin/reason.go`: every machine-readable reason.
- Tests (every test function):
  - `list_test.go`: `TestNewList`, `TestListIsIsolatedFromCallerSlices`,
    `TestEntryExpiresAtTheInstant`.
  - `authorize_test.go`: `TestRequirementOf`, `TestCatalogueCoversEveryOperation`,
    `TestSingleNodeManagementOperationsNeedTheLocalOwner`,
    `TestUnknownOperationIsDeniedToEveryone`, `TestZeroActorHasNoRights`,
    `TestAuthorizeEveryOperationAtEveryLevel`, `TestEffectiveLevelAndAuthorizeRules`,
    `TestAcceptanceScenarioManageLevel`,
    `TestAcceptanceScenarioBatchRevocationIsPerNode`,
    `TestFullControlMayChangePresets`, `TestFullControlMayLoosenPolicy`,
    `TestRemovalAndDowngradeTakeEffectImmediately`, `TestEffectiveLevelStanding`,
    `TestRemoteNeverReachesLocalOwnerOnly`, `TestHigherLevelNeverLosesRights`,
    `TestUnknownRequirementIsDeniedToRemotePersons`.
  - `directive_test.go`: `TestAcceptDirective`,
    `TestAcceptDirectiveWithUnsetLocalNodeRejects`,
    `TestAcceptDirectiveWithoutOfflineSinceRejects`,
    `TestAcceptDirectiveSigningTimeAgainstOfflineWindow`,
    `TestAcceptDirectiveAfterTwoOfflinePeriods`.
  - `reachability_test.go`: `TestMayIssueReachabilityProof`,
    `TestCheckReachabilityProof`; with `publication`,
    `TestAProofOfARetiredNodeIsRefused`.
  - `request_test.go`: `TestValidateRequest`, `TestEveryConfigChangeIsSideEffecting`,
    `TestSideEffectingOperations`,
    `TestValidateRequestForSingleNodeManagementOperations`,
    `TestConfigChangesAreTheVersionedSettings`, `TestCheckVersion`,
    `TestCheckReplay`, `TestKeyForScopesOperationIDsToThePerson`,
    `TestNewOperationID`.
