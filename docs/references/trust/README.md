# Trust

How a node decides whom to trust: direct trust, trust inherited through one
intermediate node, the online vouchers that bound inherited trust in time, the
local revocation set, eviction and offline administrator directives. The topic
covers decisions only; the signed formats of credentials and vouchers belong to
the record formats and reach this package as already-verified inputs.

## What

**Cluster.** A node has no global member list. Its cluster is the set of nodes it
can reach: its direct trust parties plus their direct trust parties. Each node
sees a different cluster.

**Direct trust.** Two nodes trust each other directly when they hold a mutual
trust credential signed by both. It counts while it is neither revoked (by
digest, by either party) nor expired. Direct trust needs no vouchers: two nodes
talking to each other already know the other is online.

**Inherited trust.** If A trusts B directly and B trusts C directly, A trusts C
through B. Inheritance extends one level only: a path has at most one
intermediate and two credentials. With A-B-C-D, A reaches C and not D. A path is
valid when:

1. both credentials are usable;
2. neither the intermediate nor the target is in A's locally revoked set;
3. A's own setting and C's declared switch both accept inheritance, so A and C
   always give the same answer about each other;
4. the intermediate holds a valid voucher for both edges.

Any single valid path is enough. Inherited trust is never stored: it is a pure
function of credentials, revocations, the locally revoked set, vouchers and local
settings, so a cache can always be discarded and recomputed.

**Vouchers.** Each node issues, every 3 minutes, a voucher for each of its
direct edges. A voucher carries the edge, the issue time and a counter that only
grows, but no expiry. The verifier computes validity itself:
`issued_at <= now + clock tolerance` and `now - issued_at <= its own grace
period`. The verifier keeps only the latest voucher per issuer and edge and
rejects a counter that does not advance. It also rejects at receipt a voucher
stamped beyond its clock plus the tolerance, and stores nothing of it. The grace period is at least issue
period (3 min) + one-hop push budget (1 min) + clock tolerance (2 min, shared: [timing](../timing/README.md)) = 6
minutes, default 10 minutes; smaller values are rejected.

**Verdicts.** `Evaluate` returns one of four outcomes with a typed reason:

- allowed directly, or allowed by inheritance, with the credential digests the
  trust rests on (every valid path);
- suspended: a path exists that fails only because a voucher is missing, stale or
  stamped too far ahead of the verifier's clock (possible only after the clock
  moved back since receipt). Recoverable, and authorization treats it
  differently from a denial;
- denied, with the most specific reason among the candidate paths. A target whose
  declaration is not known is denied with its own reason, separate from a target
  whose declaration refuses inheritance.

**Local revocation.** Revoking node X locally adds it to the locally revoked set.
The set expresses the owner's intent and no path overrides it: X is refused and
is never accepted as an intermediate. If X is a direct party, the node also stops
vouching for that edge at once and revokes the credential. Clearing the entry
restores nothing when the credential is revoked: the nodes must pair again.

**Retired nodes.** A node whose identity key published a verified
self-revocation statement is retired. The identity is void for good: the node
re-joins under a new identity and nothing migrates. The trust view carries the
set of retired nodes, and a retired node is never trusted, directly or by
inheritance, and is never the intermediate of an inherited path. Its verdict is
a denial with its own reason, separate from a revoked credential and from local
revocation. The local node issues no vouchers for an edge whose other end is
retired, and neither the issuance gate nor eviction waits for a retired party.
An empty set retires nobody.

**Eviction.** Evicting X means every direct party of X revokes its own
credential with X. Progress lists the parties that still hold an unrevoked
credential with X, the inherited paths that rest on X, and stays incomplete as
long as any party remains. A suspended path is not an eviction: it comes back
when its intermediate is online again unless the credential was revoked. A
retired X is complete at once. A credential with a retired node carries no
trust, so it is neither outstanding nor a dependency, and a node reachable only
through a retired party is complete as well.

**New-edge notification.** When an edge between A and B appears, the owners of A,
B and their direct parties (within the local view) are told. It informs and never
blocks.

**Voucher issuance gate.** A node issues no vouchers until it has synced
revocations and trust graph with its direct parties and carried out pending
offline directives, when it is recovering right after the daemon started, was
restored from a backup, or was out of contact with every direct party for longer
than its grace period. The caller measures the time out of contact; it covers a
stopped process, a sleeping host and a lost network. Direct parties whose
connection attempt failed in the current round are not waited for. The gate reads
the direct parties (usable local credentials, deduplicated) and the grace period
from the trust view.

**Offline directives.** A verified directive to revoke an edge applies only when
the edge involves the local node and, when the credential is known, only when
the directive's claimed ends are the credential's two ends. A directive can only
revoke; it can neither create trust nor loosen anything.

## Why

- **Vouchers instead of expiring inherited trust.** Inheritance depends on the
  intermediate being online. A voucher per edge, with validity computed by the
  verifier, bounds how long a revoked or vanished intermediate keeps granting
  access, without any node polling or voting. Putting an expiry in the voucher
  would let the issuer's clock decide the verifier's window; a fast issuer clock
  could then extend its reach.
- **Per edge, not per node.** Once a node stops vouching for one edge, nodes
  outside that edge lose the path through it within one grace period, even if the
  revocation has not reached them.
- **Counters.** A replayed old voucher keeps an old but still recent-looking
  timestamp. Rejecting non-advancing counters stops a replay from refreshing
  trust the issuer has stopped vouching for.
- **Future vouchers are rejected at receipt.** An issuer whose clock runs fast
  stamps its last voucher ahead. If the receiver kept it, the voucher would turn
  valid later, once the receiver's clock reached the stamp, after the issuer went
  offline, bringing an offline intermediate back to life. Rejecting it on receipt
  closes that, and nothing of it is kept, not even its counter: vouchers come one
  hop over an authenticated channel and a returning issuer sends fresh ones. The
  evaluation-time future check stays for a verifier whose clock moved back after
  receipt; it only suspends, and it resolves as the clock advances.
- **Unknown declaration is its own reason.** A missing declaration and a
  declaration that refuses inheritance are both denials, but the first can change
  as soon as the declaration arrives, so the caller needs to tell them apart.
- **Grace floor.** A window below issue period + push budget + clock tolerance
  could reject a voucher that is in flight or slightly ahead and suspend healthy
  paths, so the floor is derived from the three values rather than chosen.
- **Both inheritance switches are read by both sides.** If only the evaluating
  side's switch counted, A could reach C while C could not reach A, a one-way
  reachability that no owner would expect.
- **Local revocation beats paths.** Otherwise a node revoked by its former
  partner could come back through a third node the partner also trusts, and the
  owner's intent would be undone by a graph the owner does not control.
- **Retirement is its own input.** "A void identity carries no trust" is a trust
  rule and lives here. Folding it into revoked credentials would put credentials
  in that set that nobody revoked, and leaving it to callers would repeat the
  rule at every call site. Local revocation is the owner's choice and can be
  cleared; retirement is the key holder's, is permanent and cannot be.
- **Retirement beats local revocation in the reason.** Both refuse the node, but
  retirement is the fact that never changes, so it is the one worth naming.
- **A retired intermediate ranks lowest among path failures.** Nothing can revive
  that path, so a failure on another path, which could still change, says more
  about what to do. Its check also runs before the credential checks: the path
  is void whatever its credentials say. It is a denial, never a suspension, even
  with stale vouchers, because no fresh voucher can bring it back.
- **No vouchers, no waiting, no dependency for a retired node.** A voucher for
  its edge helps nobody, since nobody accepts trust through it. A void identity
  will never sync or revoke, so a gate or an eviction that waited for it would
  never finish. Eviction therefore counts only credentials that can still carry
  trust.
- **Revocation effects and directives are unchanged.** Revoking a retired node
  locally, or its credential by directive, is harmless tidying that only takes
  trust away, so those answers do not change.
- **Suspended is not denied.** An offline intermediate is a recoverable fault.
  Calling it denial would make senders drop messages that merely need to wait.
- **Eviction is complete only when every direct party revoked.** A credential can
  only be revoked by its two parties. A sleeping party can bring the evicted node
  back by inheritance when it wakes, so the tool must report "incomplete" and
  name the party instead of declaring success.
- **Issuance gate.** A node that was away may have missed revocations. If it
  resumed vouching at once, it would revive edges the rest of the network already
  withdrew. A host that slept for a day must learn the revocations issued
  meanwhile before vouching again, which is why the gate keys on time out of
  contact and not on the process having been stopped, and why a start always
  passes through it however short the stop.
- **The gate does not wait for unreachable parties.** Requiring every party
  online would let one long-offline machine stop an intermediate from ever
  vouching again. A directive held only by unreachable parties is missed for now;
  the eviction view keeps showing the eviction as incomplete and names that
  node, so the gap stays visible.
- **The gate derives its inputs from the view.** The direct parties and the grace
  period are facts the trust rules already hold, so a caller that passed its own
  copies could disagree with them.
- **A missing voucher suspends rather than denies.** It is equally recoverable:
  the voucher arrives within one issue period on a healthy link.
- **Directive effects can only revoke.** The effect type carries nothing but
  revocations, so a directive cannot create trust or loosen a restriction,
  whatever the caller does with it.
- **A directive must match the real credential.** Only the two parties of a
  credential may revoke it. Judging the directive's claimed ends alone would let
  a directive that names the local node revoke a credential of two other nodes,
  so the claimed ends are compared with the credential's when it is known. The
  decision lives in this one function so callers keep no second check.

## Where

- `internal/trust/trust.go`: constants, `Settings`, `View` (with `Retired`),
  record shapes.
- `internal/trust/evaluate.go`: `Evaluate`, `Verdict`, `Outcome`, `Reason`.
- `internal/trust/voucher.go`: `Vouchers.Accept`, `VoucherFromFuture`.
- `internal/trust/revocation.go`: `RevokeLocally`, `ClearLocallyRevoked`,
  `VouchableEdges`, `ApplyDirective`.
- `internal/trust/issuance.go`: `IssuanceGate`.
- `internal/trust/eviction.go`: `EvictionProgress`, `NewEdgeAudience`.
- Tests: `evaluate_test.go` (`TestEvaluateDirect`, `TestEvaluateInherited`,
  `TestEvaluateMultiplePaths`, `TestEvaluateVouchers`, `TestEvaluateScenarios`,
  `TestEvaluateIssuerClockSkew`, `TestEvaluateStaysAllowedWhileOnline`),
  `rules_test.go` (grace period, `TestVoucherAccept`, local revocation, clearing,
  `TestIssuanceGate`, eviction, audience, `TestApplyDirective`),
  `property_test.go` (random graphs and evaluation times, order independence,
  recomputation from raw records, revocation never grants trust, two sides
  agree, an empty retired set changes nothing) and `retired_test.go`
  (`TestEvaluateRetired`, `TestVouchableEdgesRetired`, `TestIssuanceGateRetired`,
  `TestEvictionProgressRetired`, `TestRetiredLeavesOtherDecisionsUnchanged`,
  and the skipped open questions for a retired local node, clearing and the
  new-edge audience).
