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
// directly, and fetches from them once a connection to each comes up. Nothing in
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
}

func newProfileState() *profileState {
	return &profileState{peers: map[ident.NodeID]heldProfile{}, fetching: map[ident.NodeID]struct{}{}}
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

// fetchProfile starts the fetch of a node's profile, once per connection that
// comes up: not for a node that is not trusted directly, not while a fetch from
// the node is under way, and not without a network.
func (e *Engine) fetchProfile(node ident.NodeID, now time.Time) {
	if e.net == nil || e.trustOf(node, now) != trust.AllowedDirect {
		return
	}
	if _, busy := e.profiles.fetching[node]; busy {
		return
	}
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
	c.done(struct{}{}, nil)
}

// profileFetched judges the answer of a fetch and keeps the profile when it is the
// node's own, signed by the key this node accepts for it, and newer than the one
// held. A failure is logged and nothing is retried: the next connection to the
// node fetches again.
func (e *Engine) profileFetched(now time.Time, c *cmdProfileFetched) {
	switch {
	case c.err != nil:
		e.log.Info("the profile of a node could not be fetched", "node", c.node.String(), "error", c.err.Error())
		return
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
// directly. It runs whenever the trust records change.
func (e *Engine) pruneProfiles(now time.Time) {
	for node := range e.profiles.peers {
		if e.trustOf(node, now) != trust.AllowedDirect {
			delete(e.profiles.peers, node)
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
