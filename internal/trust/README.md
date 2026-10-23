# trust

Who a node trusts: direct trust, trust inherited through one intermediate node,
the online vouchers that keep inherited trust honest, local revocation,
eviction progress and the rules for offline administrator directives. Read
[../README.md](../README.md) first for the conventions every contract package
follows, and [the topic document](../../docs/references/trust/README.md) for the
reasons behind the rules.

## Entry points

| Function or type                                  | Answers                                                                                       |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `Evaluate(View, target, now) Verdict`             | Is `target` trusted: `AllowedDirect`, `AllowedInherited`, `Suspended` or `Denied`, with a `Reason` and the `Paths` (credential digests) the trust rests on. |
| `Vouchers.Accept(Voucher, now) VoucherDecision`   | Keep a voucher only if it is not stamped beyond `now + timing.ClockTolerance` and its counter is greater than the stored one for that issuer and edge. |
| `ValidateGracePeriod`, `MinGracePeriod`, `DefaultGracePeriod` | The grace period floor (6 minutes) and default (10 minutes). The floor includes `timing.ClockTolerance`, which lives in [`internal/timing`](../timing/README.md). |
| `RevokeLocally(View, x) RevocationEffects`        | What revoking `x` locally does: set membership, stop vouching, revoke credentials.            |
| `ClearLocallyRevoked(View, x, now) ClearEffect`   | Whether clearing an entry leaves a new pairing necessary.                                     |
| `VouchableEdges(View, now)`                       | The direct edges the local node issues vouchers for.                                          |
| `IssuanceGate(View, IssuanceState, now) IssuanceVerdict` | Whether the node may issue vouchers after a start, a restore or a long loss of contact. It derives the direct parties and the grace period from the view. |
| `EvictionProgress(View, x, now) Eviction`         | Direct parties of `x` that still hold a credential with it, dependent paths, completeness.    |
| `NewEdgeAudience(View, a, b, now)`                | Nodes whose owners are told about a new edge.                                                 |
| `ApplyDirective(View, EdgeRevocationDirective)`   | Whether a verified offline directive concerns this node and what it revokes. The only place that decides this: it also refuses a directive whose claimed ends differ from the credential's. |

## Rules

- **Direct trust** needs a credential that is neither revoked nor expired, and a
  target outside the locally revoked set. It never needs vouchers and never
  needs the inheritance switches.
- **Inherited trust** has at most one intermediate `M` (two edges). It needs:
  both credentials usable; `M` and the target outside the locally revoked set;
  the local setting and the target's declared switch both accepting
  inheritance (a target with no known declaration counts as not accepting); and
  a voucher from `M` for each of the two edges that satisfies
  `issued_at <= now + ClockTolerance` and `now - issued_at <= local grace`.
  A target whose declaration is not known is denied with
  `ReasonDeclarationUnknown`, told apart from one that refuses inheritance
  (`ReasonInheritanceDisabled`).
  Any one valid path suffices. Inheritance is never computed beyond one
  intermediate.
- **Suspended** means: some path fails only because a voucher is missing, stale
  or stamped too far ahead of the verifier's clock. It recovers when a fresh
  voucher arrives. Every other failure is `Denied`.
- **Computed, never stored.** `Evaluate` is a pure function of the `View`; the
  order in which records reached the view does not matter.
- **Local revocation** puts the node in the locally revoked set. The set beats
  every path: the node is refused and is never an intermediate. If the node is a
  direct party the effects also stop vouching for the edge and revoke the
  credential, so nodes that reach it through the local node lose it once their
  grace period runs out. Clearing an entry restores nothing when the credential
  was revoked (`NewPairingRequired`); who may clear is decided elsewhere.
- **Vouchers.** `Vouchers.Accept` keeps only the latest voucher per issuer and
  edge, and rejects a counter that does not advance. It also rejects, at
  receipt, a voucher stamped beyond `now + timing.ClockTolerance` (`VoucherFromFuture`),
  before looking at the counter, and stores nothing of it. Otherwise an issuer
  whose clock runs fast would have its last voucher become valid later, once the
  receiver's clock reaches the stamp, after the issuer went offline, bringing an
  offline intermediate back to life. Nothing of a rejected voucher is kept; that
  is fine because vouchers come one hop over an authenticated channel and a
  returning issuer sends fresh ones. `Evaluate` still suspends a stored voucher
  that looks future-dated (`ReasonVoucherFuture`); that can only happen when the
  verifier's clock moved back after receipt, and it resolves itself as the clock
  advances.
- **Voucher issuance gate.** A node issues nothing until it has synced when it
  is recovering right after the daemon started (`Starting`), was restored from a
  backup, or was out of contact with every direct party for longer than its
  grace period (`OfflineFor`, measured by the caller; it covers a stopped
  process, a sleeping host and a lost network). A host that slept for a day must
  learn the revocations issued meanwhile before it vouches again, and a restart
  cannot be used to skip the gate. Syncing means a completed sync with every
  direct party that is neither synced nor `Unreachable`, then no pending offline
  directive. Unreachable parties (connection attempt failed in the current
  round) are not waited for: requiring every party online would let one
  long-offline machine stop an intermediate from ever vouching again. A
  directive held only by unreachable parties is missed for now, and the eviction
  view keeps showing the eviction as incomplete and names that node. The direct
  parties are the other ends of the usable local credentials in the view
  (deduplicated) and the grace period is `View.Settings.GracePeriod`, so callers
  do not re-derive either.
- **Directives only revoke.** `ApplyDirective` returns only revocation effects,
  and only for an edge the local node is an end of. It is where "does the
  directive concern this node's own trust" is decided, and callers add no check
  of their own on the directive's ends. When the credential is in the view, the
  directive's claimed ends must be the credential's two ends in either order,
  otherwise the disposition is `DirectiveMismatch` and nothing happens: only the
  two parties of a credential may revoke it. A credential the view has not seen
  is judged by the claimed ends alone.

## What the caller must guarantee

- Every record in the `View` has been verified: signatures, signer standing,
  domain separation and digests. This package never sees keys or encodings.
- Credential digests in the view are unique; `Vouchers` holds only the latest
  voucher per issuer and edge (use `Vouchers.Accept`, called with the time of receipt); `Settings.GracePeriod`
  passed `ValidateGracePeriod`.
- The view holds the credentials within two hops of the local node. Results are
  only as complete as the view: eviction progress cannot see parties beyond it.
- `now` is UTC and comes from the caller. Nothing here reads a clock.
- The caller carries out the effects it is handed (adding to the set, signing
  revocations, stopping issuance). The package decides; it does not act.
