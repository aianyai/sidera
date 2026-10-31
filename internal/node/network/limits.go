package network

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
)

// unauthenticatedLimit is what a peer may hold until a certificate exchange
// has made it a member: a few connections and streams and a small memory share,
// so that strangers cannot spend what members need.
var unauthenticatedLimit = &rcmgr.BaseLimit{
	Streams:         32,
	StreamsInbound:  16,
	StreamsOutbound: 16,
	Conns:           8,
	ConnsInbound:    4,
	ConnsOutbound:   4,
	FD:              16,
	Memory:          8 << 20,
}

// limitIdleBeforeDrop is how long a peer without connections keeps its entry in
// the limiter. The resource manager collects the scope of an unused peer within
// a minute and then asks the limiter again, so after two minutes the entry can
// no longer be the one a live scope holds.
const limitIdleBeforeDrop = 2 * time.Minute

// peerLimit is the one Limit object the resource manager keeps for a peer. The
// manager asks the limiter once, when it creates the peer's scope, and then
// re-reads this object on every check, so the object is stable and its values
// move: the unauthenticated tier until the peer is a member, the default
// per-peer limits after.
type peerLimit struct {
	member atomic.Bool
	base   rcmgr.Limit // the member tier
}

func (l *peerLimit) tier() rcmgr.Limit {
	if l.member.Load() {
		return l.base
	}
	return unauthenticatedLimit
}

func (l *peerLimit) GetMemoryLimit() int64               { return l.tier().GetMemoryLimit() }
func (l *peerLimit) GetStreamLimit(d lnet.Direction) int { return l.tier().GetStreamLimit(d) }
func (l *peerLimit) GetStreamTotalLimit() int            { return l.tier().GetStreamTotalLimit() }
func (l *peerLimit) GetConnLimit(d lnet.Direction) int   { return l.tier().GetConnLimit(d) }
func (l *peerLimit) GetConnTotalLimit() int              { return l.tier().GetConnTotalLimit() }
func (l *peerLimit) GetFDLimit() int                     { return l.tier().GetFDLimit() }

var _ rcmgr.Limit = (*peerLimit)(nil)

type limitEntry struct {
	limit *peerLimit
	used  time.Time // the last time the resource manager asked or a tier was set
}

// peerLimiter is the default fixed limiter with the per-peer limits replaced by
// one stable peerLimit per peer, which starts in the unauthenticated tier.
type peerLimiter struct {
	rcmgr.Limiter
	now func() time.Time

	mu      sync.Mutex
	entries map[peer.ID]*limitEntry
}

func newPeerLimiter(now func() time.Time) *peerLimiter {
	return &peerLimiter{
		Limiter: rcmgr.NewFixedLimiter(rcmgr.DefaultLimits.AutoScale()),
		now:     now,
		entries: map[peer.ID]*limitEntry{},
	}
}

// entry returns the peer's entry, made on first use; the caller holds mu.
func (l *peerLimiter) entry(p peer.ID) *limitEntry {
	e, ok := l.entries[p]
	if !ok {
		e = &limitEntry{limit: &peerLimit{base: l.Limiter.GetPeerLimits(p)}}
		l.entries[p] = e
	}
	e.used = l.now()
	return e
}

// GetPeerLimits is the resource manager's question when it creates a peer scope.
func (l *peerLimiter) GetPeerLimits(p peer.ID) rcmgr.Limit {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.entry(p).limit
}

// setMember moves a peer to the member tier or back to the unauthenticated one.
func (l *peerLimiter) setMember(p peer.ID, member bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entry(p).limit.member.Store(member)
}

// reset returns a peer to the unauthenticated tier after its last connection
// ended. The entry stays: the manager keeps the scope, and with it this object,
// until it collects the scope, and a replacement object would never be asked.
func (l *peerLimiter) reset(p peer.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[p]; ok {
		e.limit.member.Store(false)
	}
}

// prune drops the entries of peers that have no connection and have been idle
// for limitIdleBeforeDrop, so that the table does not grow with every peer
// identifier a stranger ever presented.
func (l *peerLimiter) prune(connected func(peer.ID) bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for p, e := range l.entries {
		if now.Sub(e.used) >= limitIdleBeforeDrop && !connected(p) {
			delete(l.entries, p)
		}
	}
}

// newResourceManager makes the resource manager that asks the limiter for every
// peer's limits.
func newResourceManager(l *peerLimiter) (lnet.ResourceManager, error) {
	rm, err := rcmgr.NewResourceManager(l, rcmgr.WithMetricsDisabled())
	if err != nil {
		return nil, fmt.Errorf("network: resource manager: %w", err)
	}
	return rm, nil
}
