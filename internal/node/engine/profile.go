package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/hostfacts"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// The node profile is what a node says about itself so that people and agents
// can tell nodes apart: a name, the machine and the version of Sidera it runs. It
// is a signed record the node issues for itself, answers to the nodes it trusts
// directly, and fetches from them when a connection to each comes up and when a node
// becomes trusted directly on a connection that is already up. Nothing in
// it is a fact of addressing, trust or policy.

// profileState is what the loop holds of profiles. It is touched by the loop
// alone.
type profileState struct {
	// own is the profile this node issued last; nil until it has issued one.
	own *ownProfile
	// peers are the profiles fetched from the nodes this node trusts directly,
	// the newest of each.
	peers map[ident.NodeID]heldProfile
	// fetching are the nodes a worker is fetching a profile from now.
	fetching map[ident.NodeID]struct{}
	// again are the nodes a connection came up to while a fetch from them was under
	// way. The fetch under way may be on a connection that has since died, so one
	// more is made when it ends.
	again map[ident.NodeID]struct{}
	// retried is, for each node whose fetch failed since the last connection to it
	// came up, how many of profileRetryWaits the retries have used. A node with an
	// entry has a retry in the schedule (schedProfileRetry) or a retry's fetch under
	// way.
	retried map[ident.NodeID]int
}

func newProfileState() *profileState {
	return &profileState{
		peers: map[ident.NodeID]heldProfile{}, fetching: map[ident.NodeID]struct{}{}, again: map[ident.NodeID]struct{}{},
		retried: map[ident.NodeID]int{},
	}
}

// profileRetryWaits are the waits before the retries of a fetch that failed, on the
// injected clock, one for each retry; after the last retry fails the failure stands
// until the next connection to the node. The failure to expect is a fetch that came
// too soon: the node that read the certificate exchange first is told of the
// connection first, and the peer resets member streams until it has applied its own
// side of the exchange, which takes well under a second. Nothing else would ask
// again, because a connection stays up while it is used and the news of it comes
// once. The waits are the short ramp the engine uses for a reply somebody waits on
// (Params.ControlPullRamp): the profile is what a person looks at when listing the
// nodes, and a peer that still fails after 17 seconds in all is not about to answer.
var profileRetryWaits = [...]time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// profileRetryKey is the entry of the schedule for the next retry of a node's fetch.
func profileRetryKey(node ident.NodeID) schedKey { return schedKey{schedProfileRetry, node.String()} }

// endProfileRetries forgets the retries of a node's fetch: the fetch has an answer,
// a new connection starts it afresh, or the node is not to be asked any more.
func (e *Engine) endProfileRetries(node ident.NodeID) {
	delete(e.profiles.retried, node)
	e.sched.remove(profileRetryKey(node))
}

// ownProfile is the profile this node issued, signed.
type ownProfile struct {
	rec record.Record
	p   publication.NodeProfile
}

// heldProfile is a profile fetched from a peer: the record as the peer signed it
// (which can be sent on as it is), what it says, and when it was fetched.
type heldProfile struct {
	rec       record.Record
	v         record.Verified[publication.NodeProfile]
	fetchedAt time.Time
}

// ProfileView is a node profile as the engine shows it.
type ProfileView struct {
	Name          string
	HostName      string
	Platform      string
	System        string
	SystemBuild   string
	SideraVersion string
	// IssuedAt is when the node made the profile.
	IssuedAt time.Time
	// AsOf is when this node got the profile: for a peer, when it fetched it; for
	// this node itself, when it issued it.
	AsOf time.Time
}

func profileViewOf(p publication.NodeProfile, asOf time.Time) *ProfileView {
	return &ProfileView{
		Name: p.Name, HostName: p.HostName, Platform: p.Platform, System: p.System,
		SystemBuild: p.SystemBuild, SideraVersion: p.SideraVersion, IssuedAt: p.IssuedAt, AsOf: asOf,
	}
}

// profileCandidate is the profile this node would issue now, apart from its
// issue time: the owner's name or else the host name, what the machine says and
// the version of the program. A field that does not satisfy its rule is left out;
// dropped names them, and problems are the reasons the machine gave for a field it
// could not read, both for the log of an issue.
func (e *Engine) profileCandidate(now time.Time) (p publication.NodeProfile, dropped, problems []string, err error) {
	var facts hostfacts.Facts
	if e.d.HostFacts != nil {
		facts = e.d.HostFacts()
	}
	name := e.d.Config.Node().Settings.Name
	if name == "" {
		name = facts.HostName
	}
	p, dropped, err = publication.NewNodeProfile(publication.NodeProfile{
		Node: e.d.Node, IssuedAt: now, Name: name, HostName: facts.HostName, Platform: facts.Platform,
		System: facts.System, SystemBuild: facts.SystemBuild, SideraVersion: e.d.Version,
	})
	return p, dropped, facts.Problems, err
}

// sameProfileText reports whether two profiles say the same, issue time apart.
func sameProfileText(a, b publication.NodeProfile) bool {
	a.IssuedAt, b.IssuedAt = time.Time{}, time.Time{}
	return a == b
}

// refreshProfile returns the profile this node issues now, making a new one only
// when what it would say differs from the current one, so that asking is cheap and
// a record changes only when its content does. A new profile is issued strictly
// later than the one before, whatever the clock says, because two profiles of one
// second are never told apart by the nodes that hold them (publication.ProfileReplaces)
// and a change made within the second of the last one would be lost.
//
// When the node cannot sign, the profile issued before is returned with the
// reason, or nil when there is none. It runs in the loop.
func (e *Engine) refreshProfile(now time.Time) (*ownProfile, error) {
	cur := e.profiles.own
	if err := e.canSign(); err != nil {
		return cur, err
	}
	cand, dropped, problems, err := e.profileCandidate(now)
	if err != nil {
		return cur, fmt.Errorf("engine: the node profile: %w", err)
	}
	if cur != nil && sameProfileText(cur.p, cand) {
		return cur, nil
	}
	if cur != nil && cand.IssuedAt.Before(cur.p.IssuedAt.Add(time.Second)) {
		cand.IssuedAt = cur.p.IssuedAt.Add(time.Second)
	}
	rec, err := record.Seal(publication.NodeProfileDescriptor(e.acceptedKey(e.d.Node), now), cand, e.d.Keys.NetworkSigner())
	if err != nil {
		return cur, fmt.Errorf("engine: signing the node profile: %w", err)
	}
	e.profiles.own = &ownProfile{rec: rec, p: cand}
	// Once per issue and not per request: a request that finds nothing changed
	// returns above.
	e.log.Info("the node profile was issued", "issued_at", cand.IssuedAt)
	if len(dropped) > 0 {
		e.log.Info("fields left out of the node profile because they do not satisfy their rule", "fields", dropped)
	}
	for _, problem := range problems {
		e.log.Debug("the machine did not give a fact for the node profile", "problem", problem)
	}
	return e.profiles.own, nil
}

type cmdProfile struct {
	call[record.Record]
	from transport.Origin
}

func (c *cmdProfile) gate() gate     { return gateLoopback }
func (c *cmdProfile) fail(err error) { c.call.fail(err) }
func (c *cmdProfile) run(e *Engine, now time.Time) {
	// Only a node this node trusts directly may ask: the same set the peers view
	// lists. Membership alone is not enough, since a member can also be trusted by
	// inheritance only, and a profile is not for the nodes beyond the first link.
	if c.from.Local() || e.trustOf(c.from.Node(), now) != trust.AllowedDirect {
		c.done(record.Record{}, refuse(ReasonNotPermitted))
		return
	}
	own, err := e.refreshProfile(now)
	if own == nil {
		c.done(record.Record{}, err)
		return
	}
	if err != nil {
		// A node that may not sign still has the profile it signed before, which
		// says what it said then.
		e.log.Debug("the node profile issued before is served", "reason", err.Error())
	}
	c.done(own.rec, nil)
}

// Profile is the target side of a profile request: the signed profile this node
// issued for itself, to a node it trusts directly. A node that is trusted only by
// inheritance, or not at all, is refused.
func (e *Engine) Profile(ctx context.Context, from transport.Origin) (record.Record, error) {
	c := &cmdProfile{call: newCall[record.Record](), from: from}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

type cmdMemberConnected struct {
	call[struct{}]
	node ident.NodeID
}

func (c *cmdMemberConnected) gate() gate     { return gateLoopback }
func (c *cmdMemberConnected) fail(err error) { c.call.fail(err) }
func (c *cmdMemberConnected) run(e *Engine, now time.Time) {
	c.done(struct{}{}, nil)
	e.fetchProfile(c.node, now)
}

// MemberConnected tells the engine that a connection to a node became a member's.
// The engine fetches that node's profile when it trusts the node directly. It
// returns once the engine has taken the news; the fetch runs in a worker.
func (e *Engine) MemberConnected(ctx context.Context, node ident.NodeID) {
	c := &cmdMemberConnected{call: newCall[struct{}](), node: node}
	_, _ = roundTrip(ctx, e, submitInternal, c, c.call)
}

// fetchProfile starts the fetch of a node's profile when a connection to it comes
// up: not for a node that is not trusted directly and not without a network. While
// a fetch from the node is under way the connection is noted, and the fetch ends
// with one more (profileFetched). The connection also ends the retries of an
// earlier failed fetch, which start again if this one fails.
func (e *Engine) fetchProfile(node ident.NodeID, now time.Time) {
	e.endProfileRetries(node) // a new connection is asked afresh
	if e.net == nil || e.trustOf(node, now) != trust.AllowedDirect {
		return
	}
	if _, busy := e.profiles.fetching[node]; busy {
		e.profiles.again[node] = struct{}{}
		return
	}
	e.startProfileFetch(node)
}

// startProfileFetch hands the fetch of a node's profile to a worker. The caller
// has found the node trusted directly, the network present and no fetch under way.
func (e *Engine) startProfileFetch(node ident.NodeID) {
	e.profiles.fetching[node] = struct{}{}
	if !e.spawn(func(ctx context.Context) {
		c := &cmdProfileFetched{call: newCall[struct{}](), node: node}
		c.rec, c.served, c.err = e.net.FetchProfile(ctx, node)
		_, _ = roundTrip(ctx, e, submitInternal, c, c.call)
	}) {
		delete(e.profiles.fetching, node) // the engine has ended
	}
}

type cmdProfileFetched struct {
	call[struct{}]
	node   ident.NodeID
	rec    record.Record
	served bool
	err    error
}

func (c *cmdProfileFetched) gate() gate     { return gateLoopback }
func (c *cmdProfileFetched) fail(err error) { c.call.fail(err) }
func (c *cmdProfileFetched) run(e *Engine, now time.Time) {
	delete(e.profiles.fetching, c.node)
	e.profileFetched(now, c)
	if _, again := e.profiles.again[c.node]; again {
		delete(e.profiles.again, c.node)
		if e.net.Connected(c.node) {
			e.fetchProfile(c.node, now)
		}
	}
	c.done(struct{}{}, nil)
}

// profileFetched judges the answer of a fetch and keeps the profile when it is the
// node's own, signed by the key this node accepts for it, and newer than the one
// held. A failed fetch is asked again (profileFetchFailed); a node that does not
// serve the protocol is not, since the answer will not change on this connection.
func (e *Engine) profileFetched(now time.Time, c *cmdProfileFetched) {
	if c.err != nil {
		e.profileFetchFailed(now, c.node, c.err)
		return
	}
	e.endProfileRetries(c.node)
	switch {
	case !c.served:
		e.log.Debug("a node does not serve the profile protocol", "node", c.node.String())
		return
	case e.trustOf(c.node, now) != trust.AllowedDirect:
		return // trust ended while the answer was on its way
	}
	v, err := e.verifyProfile(now, c.rec, c.node)
	if err != nil {
		e.log.Info("a profile a node sent was refused", "node", c.node.String(), "error", err.Error())
		return
	}
	if held, ok := e.profiles.peers[c.node]; ok && !publication.ProfileReplaces(held.v, v) {
		e.log.Debug("a profile a node sent is not newer than the one held", "node", c.node.String())
		return
	}
	e.profiles.peers[c.node] = heldProfile{rec: c.rec, v: v, fetchedAt: now}
}

// profileFetchFailed logs a fetch that failed and schedules the next of
// profileRetryWaits, as long as the node is still trusted directly and a member
// connection to it is up. Past the last retry, or with the node gone, the failure
// stands until the next connection to the node, or the next change of the trust
// records that finds the node without a profile. The first failure is logged at
// info level, the retries' at debug level, and the end of the retries at info
// level, so that a streak is two lines and not one for each attempt.
func (e *Engine) profileFetchFailed(now time.Time, node ident.NodeID, err error) {
	used := e.profiles.retried[node]
	if e.trustOf(node, now) != trust.AllowedDirect || !e.net.Connected(node) {
		e.endProfileRetries(node)
		e.log.Info("the profile of a node could not be fetched", "node", node.String(), "error", err.Error())
		return
	}
	if used >= len(profileRetryWaits) {
		e.endProfileRetries(node)
		e.log.Info("the profile of a node could not be fetched after retries; the next connection asks again", "node", node.String(), "error", err.Error())
		return
	}
	if used == 0 {
		e.log.Info("the profile of a node could not be fetched; it is asked again shortly", "node", node.String(), "error", err.Error())
	} else {
		e.log.Debug("a retry of the profile of a node failed", "node", node.String(), "error", err.Error())
	}
	e.profiles.retried[node] = used + 1
	e.sched.set(profileRetryKey(node), now.Add(profileRetryWaits[used]))
}

// retryProfile is a retry of a failed fetch falling due: it asks again if the node
// is still trusted directly and a member connection is up, and ends the retries
// otherwise (a connection that comes up later asks by itself).
func (e *Engine) retryProfile(node ident.NodeID, now time.Time) {
	_, busy := e.profiles.fetching[node]
	switch {
	case busy:
		// Another fetch is under way; its outcome decides what comes next.
	case e.net != nil && e.net.Connected(node) && e.trustOf(node, now) == trust.AllowedDirect:
		e.startProfileFetch(node)
	default:
		e.endProfileRetries(node)
	}
}

// verifyProfile verifies a profile a node signed about itself. The record names
// its node, and that must be the node it was fetched from; the signature is then
// checked against the network key this node accepts for that node.
func (e *Engine) verifyProfile(now time.Time, rec record.Record, node ident.NodeID) (record.Verified[publication.NodeProfile], error) {
	named, err := publication.NodeProfileNode(rec)
	if err != nil {
		return record.Verified[publication.NodeProfile]{}, err
	}
	if named != node {
		return record.Verified[publication.NodeProfile]{}, errors.New("the profile is of another node")
	}
	return record.Verify(publication.NodeProfileDescriptor(e.acceptedKey(node), now), rec)
}

// pruneProfiles drops the profiles of the nodes this node no longer trusts
// directly, and the retries of fetching them. It runs whenever the trust records change.
func (e *Engine) pruneProfiles(now time.Time) {
	for node := range e.profiles.peers {
		if e.trustOf(node, now) != trust.AllowedDirect {
			delete(e.profiles.peers, node)
		}
	}
	for node := range e.profiles.retried {
		if e.trustOf(node, now) != trust.AllowedDirect {
			e.endProfileRetries(node)
		}
	}
}

// fetchMissingProfiles asks the nodes this node trusts directly that it holds no
// profile of, has no fetch under way to, and has a member connection up to. The
// news of that connection (MemberConnected) came once, when the node may not yet
// have been trusted directly, and does not come again. It runs whenever the trust
// records change.
func (e *Engine) fetchMissingProfiles(now time.Time) {
	if e.net == nil {
		return
	}
	for _, h := range e.trust.credentials {
		node := h.cred.Payload().A
		if node == e.d.Node {
			node = h.cred.Payload().B
		}
		_, held := e.profiles.peers[node]
		_, busy := e.profiles.fetching[node]
		if !held && !busy && e.net.Connected(node) && e.trustOf(node, now) == trust.AllowedDirect {
			e.startProfileFetch(node)
		}
	}
}

// profileOf is the profile to show for a node, nil when none is known: this
// node's own as issued now, or the one fetched from a peer.
func (e *Engine) profileOf(node ident.NodeID, now time.Time) *ProfileView {
	if node == e.d.Node {
		own, _ := e.refreshProfile(now)
		if own == nil {
			return nil
		}
		return profileViewOf(own.p, own.p.IssuedAt)
	}
	if held, ok := e.profiles.peers[node]; ok {
		return profileViewOf(held.v.Payload(), held.fetchedAt)
	}
	return nil
}
