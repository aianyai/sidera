package trust

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// IssuanceReason says whether and why a node may issue vouchers.
type IssuanceReason int

const (
	// IssuanceOpen: nothing holds issuance back.
	IssuanceOpen IssuanceReason = iota + 1
	// IssuanceAwaitingSync: revocation and trust-graph sync with some direct
	// parties is not done.
	IssuanceAwaitingSync
	// IssuanceAwaitingDirectives: offline administrator directives are still
	// pending.
	IssuanceAwaitingDirectives
)

func (r IssuanceReason) String() string {
	switch r {
	case IssuanceOpen:
		return "open"
	case IssuanceAwaitingSync:
		return "awaiting-sync"
	case IssuanceAwaitingDirectives:
		return "awaiting-directives"
	}
	return "unknown"
}

// IssuanceState is what the voucher issuance gate reads about the node's own
// recovery. The caller fixes Starting, OfflineFor and RestoredFromBackup at the
// moment the node starts or regains contact and keeps them until the gate
// opens, so the answer does not change just because time passes afterwards.
// The direct parties and the grace period are not part of it: IssuanceGate
// reads them from the View.
type IssuanceState struct {
	// Starting says this is the recovery right after the daemon started.
	Starting bool
	// OfflineFor is the time since this node last had a usable connection to
	// any direct trust party, measured by the caller. It covers a stopped
	// process, a sleeping host and a lost network alike; zero when the node
	// never lost contact.
	OfflineFor time.Duration
	// RestoredFromBackup says the node's data came from a backup or a copy.
	RestoredFromBackup bool
	// Synced are the direct parties with which revocation and trust-graph sync
	// has completed since the recovery began.
	Synced NodeSet
	// Unreachable are the direct parties whose connection attempt failed in
	// the current sync round. The gate does not wait for them: requiring every
	// party to be online would let one long-offline machine stop an
	// intermediate from ever vouching again. A directive held only by
	// unreachable parties is missed for now, and the eviction view keeps
	// showing the eviction as incomplete and names that node.
	Unreachable NodeSet
	// PendingDirectives counts offline directives received but not yet
	// verified and carried out.
	PendingDirectives int
}

// IssuanceVerdict is the answer of IssuanceGate.
type IssuanceVerdict struct {
	Allowed bool
	Reason  IssuanceReason
	// WaitingFor lists the direct parties that are neither synced nor
	// unreachable, in order.
	WaitingFor []ident.NodeID
}

// RequiresSync reports whether the node must sync before issuing vouchers: it
// is recovering right after start, it was restored from a backup, or it was out
// of contact longer than the grace period. In every case it may hold an
// outdated view of revocations and would otherwise vouch for edges that were
// revoked while it was away, reviving trust that the rest of the network
// already withdrew. A restart does not escape the gate however short the stop
// was, and a host that slept for a day must learn the revocations issued
// meanwhile before it vouches again.
func (s IssuanceState) RequiresSync(grace time.Duration) bool {
	return s.Starting || s.RestoredFromBackup || s.OfflineFor > grace
}

// directParties returns the nodes at the other end of a usable local
// credential, deduplicated and ordered.
func (v View) directParties(now time.Time) []ident.NodeID {
	seen := NodeSet{}
	var out []ident.NodeID
	for _, c := range v.credentialsOf(v.Local) {
		party, _ := c.other(v.Local)
		if _, dup := seen[party]; dup || !v.usable(c, now) {
			continue
		}
		seen[party] = struct{}{}
		out = append(out, party)
	}
	sortNodes(out)
	return out
}

// IssuanceGate decides whether the node may issue vouchers now. A node that
// requires sync issues none until it has synced with every direct party it
// could reach and carried out all pending offline directives. The direct
// parties come from the usable local credentials in v and the grace period from
// v.Settings, so callers cannot disagree with the trust rules about either.
func IssuanceGate(v View, s IssuanceState, now time.Time) IssuanceVerdict {
	if !s.RequiresSync(v.Settings.GracePeriod) {
		return IssuanceVerdict{Allowed: true, Reason: IssuanceOpen}
	}
	var waiting []ident.NodeID
	for _, p := range v.directParties(now) {
		_, synced := s.Synced[p]
		_, unreachable := s.Unreachable[p]
		if !synced && !unreachable {
			waiting = append(waiting, p)
		}
	}
	if len(waiting) > 0 {
		return IssuanceVerdict{Reason: IssuanceAwaitingSync, WaitingFor: waiting}
	}
	if s.PendingDirectives > 0 {
		return IssuanceVerdict{Reason: IssuanceAwaitingDirectives}
	}
	return IssuanceVerdict{Allowed: true, Reason: IssuanceOpen}
}
