package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/hostfacts"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// machine is the facts of a machine a test can change while the node runs.
type machine struct{ facts hostfacts.Facts }

func (m *machine) read() hostfacts.Facts { return m.facts }

var imac = hostfacts.Facts{
	HostName: "imac", Platform: "darwin/arm64", System: "macOS 27.0.1", SystemBuild: "25A354",
}

// profileNode is a node under test that trusts one peer directly and has the
// machine and version of the test.
type profileNode struct {
	r    *rig
	e    *engine.Engine
	m    *machine
	peer peerNode
	net  *fakeNetwork
}

func newProfileNode(t *testing.T, facts hostfacts.Facts) *profileNode {
	t.Helper()
	noLeaks(t)
	m := &machine{facts: facts}
	net := &fakeNetwork{}
	r := trustRig(t, withNetwork(net), withProfileSource("0.17.0", m.read))
	e := r.start()
	peer := newPeerNode(t, 1)
	cert := peer.certificate(1)
	cred := peer.credentialWith(r.keys.IdentitySigner(), false)
	if err := loopErr(t, e, func(l engine.LoopSide) error { return l.InstallCredential(cred.Record, &cert) }); err != nil {
		t.Fatal(err)
	}
	return &profileNode{r: r, e: e, m: m, peer: peer, net: net}
}

// ask is a request of the peer for the node's profile, with the record verified
// as the peer would verify it.
func (n *profileNode) ask(t *testing.T) (publication.NodeProfile, record.Record) {
	t.Helper()
	rec, err := n.e.Profile(context.Background(), transport.RemoteOrigin(n.peer.node))
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	accepted := revocation.NewSet().NetworkKey(n.r.keys.Certificate(), n.r.keys.Certificate().Payload().Serial)
	v, err := record.Verify(publication.NodeProfileDescriptor(accepted, n.r.clk.Now()), rec)
	if err != nil {
		t.Fatalf("the profile does not verify: %v", err)
	}
	if v.Payload().Node != n.r.node {
		t.Fatalf("the profile is of %v", v.Payload().Node)
	}
	return v.Payload(), rec
}

func (n *profileNode) setName(t *testing.T, b byte, name string) {
	t.Helper()
	patch := `{"name":"` + name + `"}`
	if _, err := n.e.Execute(context.Background(), setNodeSettingsCommand(b, "name "+name, n.r.cfg.Node().Version, patch)); err != nil {
		t.Fatalf("node.set name: %v", err)
	}
}

func TestTheProfileStatesTheMachineTheVersionAndTheOwnersNameOrElseTheHostName(t *testing.T) {
	n := newProfileNode(t, imac)
	p, _ := n.ask(t)
	want := publication.NodeProfile{
		Node: n.r.node, IssuedAt: p.IssuedAt, Name: "imac", HostName: "imac", Platform: "darwin/arm64",
		System: "macOS 27.0.1", SystemBuild: "25A354", SideraVersion: "0.17.0",
	}
	if p != want {
		t.Fatalf("profile = %+v, want %+v", p, want)
	}
	n.setName(t, 0xa1, "build box")
	if p, _ = n.ask(t); p.Name != "build box" || p.HostName != "imac" {
		t.Fatalf("after the owner named the node: name %q, host name %q", p.Name, p.HostName)
	}
	n.setName(t, 0xa2, "")
	if p, _ = n.ask(t); p.Name != "imac" {
		t.Fatalf("after the owner took the name away: name %q, want the host name", p.Name)
	}
}

// A new record is made when what it says changes and not otherwise, and each is
// issued strictly after the one before even when the clock has not moved, because
// the nodes that hold them keep the later one and two of one second are never
// told apart.
func TestAProfileIsIssuedAgainOnlyWhenItsContentChanges(t *testing.T) {
	n := newProfileNode(t, imac)
	first, firstRec := n.ask(t)
	if _, again := n.ask(t); !sameRecord(t, again, firstRec) {
		t.Error("unchanged facts were issued again")
	}
	issued := first.IssuedAt
	for i, host := range []string{"imac-2", "imac-3", "imac-4"} {
		n.m.facts.HostName = host
		p, rec := n.ask(t)
		if p.HostName != host || p.Name != host {
			t.Fatalf("change %d: profile %+v", i, p)
		}
		if !p.IssuedAt.After(issued) {
			t.Fatalf("change %d: issued at %v, not after %v", i, p.IssuedAt, issued)
		}
		if _, again := n.ask(t); !sameRecord(t, again, rec) {
			t.Errorf("change %d: the new profile was issued again", i)
		}
		issued = p.IssuedAt
	}
	// A name change made through the owner's operation is issued at once, without
	// anyone asking.
	n.setName(t, 0xa3, "renamed")
	got, err := n.e.Peers(context.Background(), n.callerOf(t))
	if err != nil || got[0].Profile == nil || got[0].Profile.Name != "renamed" || !got[0].Profile.IssuedAt.After(issued) {
		t.Fatalf("Peers after the rename = %+v, %v", got[0].Profile, err)
	}
}

func (n *profileNode) callerOf(t *testing.T) engine.Caller {
	t.Helper()
	id := register(t, n.e, "own")
	return engine.ByAdapter("chat", adapterproto.EndpointRef(id.String()))
}

func sameRecord(t *testing.T, a, b record.Record) bool {
	t.Helper()
	x, err1 := a.Encode()
	y, err2 := b.Encode()
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	return string(x) == string(y)
}

// A machine that reports a value the profile rules refuse does not stop the node
// from publishing the rest.
func TestAValueTheMachineGivesThatBreaksTheRulesIsLeftOutOfTheProfile(t *testing.T) {
	facts := imac
	facts.HostName = "bad\x1b[2Jhost"
	facts.System = strings.Repeat("s", 129)
	facts.Problems = []string{"system: could not be read"}
	n := newProfileNode(t, facts)
	p, _ := n.ask(t)
	if p.HostName != "" || p.Name != "" || p.System != "" || p.Platform != "darwin/arm64" || p.SystemBuild != "25A354" || p.SideraVersion != "0.17.0" {
		t.Fatalf("profile = %+v", p)
	}
	logged := strings.Join(n.r.log.Messages(), "\n")
	for _, want := range []string{"fields left out of the node profile", "the machine did not give a fact"} {
		if !strings.Contains(logged, want) {
			t.Errorf("%q was not logged once the profile was issued", want)
		}
	}
}

// Only a node trusted directly may ask. The refusal is the engine's reason, which
// the network layer puts on the wire by name.
func TestOnlyANodeTrustedDirectlyMayAskForTheProfile(t *testing.T) {
	n := newProfileNode(t, imac)
	stranger := newPeerNode(t, 2)
	_, err := n.e.Profile(context.Background(), transport.RemoteOrigin(stranger.node))
	wantReason(t, err, engine.ReasonNotPermitted)
}

// profileOf is the profile a peer signed with the network key it has at serial,
// issued at the time given.
func (p peerNode) profileIssuedAt(serial uint64, issued time.Time, name string) record.Record {
	p.t.Helper()
	return p.profileOf(serial, p.node, issued, name)
}

// profileOf is profileIssuedAt for a profile that names the node given, which may
// be another than the signer, so it is signed without asking whether the signer may.
func (p peerNode) profileOf(serial uint64, node ident.NodeID, issued time.Time, name string) record.Record {
	p.t.Helper()
	prof := publication.NodeProfile{
		Node: node, IssuedAt: issued, Name: name, HostName: "inuc", Platform: "linux/amd64",
		System: "Ubuntu 24.04.1 LTS", SystemBuild: "6.8.0-45-generic", SideraVersion: "0.17.0",
	}
	payload, err := record.EncodePayload(publication.NodeProfileDescriptor(record.AcceptedNetworkKey{}, t0), prof)
	if err != nil {
		p.t.Fatal(err)
	}
	rec, err := record.Sign(publication.NodeProfileType, payload, p.networkSigner(serial))
	if err != nil {
		p.t.Fatal(err)
	}
	return rec
}

// fetch tells the engine a connection to the peer came up and waits for the
// worker that fetches its profile to be done with it.
func (n *profileNode) fetch(t *testing.T) *engine.PeerView {
	t.Helper()
	before := n.net.profilesAsked(n.peer.node)
	n.e.MemberConnected(context.Background(), n.peer.node)
	deadline := time.Now().Add(10 * time.Second)
	for n.net.profilesAsked(n.peer.node) == before {
		if time.Now().After(deadline) {
			t.Fatal("the engine did not fetch")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := n.e.Quiesce(context.Background(), engine.Expect{}); err != nil {
		t.Fatal(err)
	}
	return n.peerView(t)
}

func (n *profileNode) peerView(t *testing.T) *engine.PeerView {
	t.Helper()
	got, err := n.e.Peers(context.Background(), n.callerOf(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i].Node == n.peer.node {
			return &got[i]
		}
	}
	t.Fatal("the peer is not listed")
	return nil
}

func TestTheProfileOfAPeerIsFetchedWhenAConnectionComesUpAndKeptWhenNewer(t *testing.T) {
	n := newProfileNode(t, imac)
	older, newer := t0.Add(time.Minute), t0.Add(2*time.Minute)
	answer := func(issued time.Time, name string) {
		rec := n.peer.profileIssuedAt(1, issued, name)
		n.net.servesProfile(n.peer.node, func() (record.Record, error) { return rec, nil })
	}
	if n.peerView(t).Profile != nil {
		t.Fatal("a profile before any fetch")
	}
	answer(older, "inuc")
	v := n.fetch(t)
	if v.Profile == nil || v.Profile.Name != "inuc" || v.Profile.System != "Ubuntu 24.04.1 LTS" || !v.Profile.IssuedAt.Equal(older) || v.Profile.AsOf.IsZero() {
		t.Fatalf("profile after the fetch = %+v", v.Profile)
	}
	answer(newer, "inuc-renamed")
	if v = n.fetch(t); v.Profile.Name != "inuc-renamed" {
		t.Fatalf("a newer profile did not replace: %+v", v.Profile)
	}
	for _, c := range []struct {
		why    string
		issued time.Time
	}{{"an older profile", older}, {"one issued at the same time", newer}} {
		answer(c.issued, "stale")
		if v = n.fetch(t); v.Profile.Name != "inuc-renamed" {
			t.Fatalf("%s replaced the newer one: %+v", c.why, v.Profile)
		}
	}
}

// What a peer sends is kept only when it is the peer's own, signed with the key
// this node accepts for it. Anything else leaves what was held.
func TestAProfileThatIsNotThePeersOwnIsNotKept(t *testing.T) {
	n := newProfileNode(t, imac)
	other := newPeerNode(t, 2)
	issued := t0.Add(time.Minute)
	for _, c := range []struct {
		why string
		rec record.Record
	}{
		{"a profile signed with a key the peer does not have", n.peer.profileIssuedAt(2, issued, "wrong key")},
		{"a profile of another node signed by that node", other.profileIssuedAt(1, issued, "other")},
		{"a profile that names the peer but is signed by another node", func() record.Record {
			return other.profileOf(1, n.peer.node, issued, "forged")
		}()},
	} {
		n.net.servesProfile(n.peer.node, func() (record.Record, error) { return c.rec, nil })
		if v := n.fetch(t); v.Profile != nil {
			t.Fatalf("%s was kept: %+v", c.why, v.Profile)
		}
	}
	// A good one after the bad ones is kept: nothing was poisoned.
	good := n.peer.profileIssuedAt(1, issued, "inuc")
	n.net.servesProfile(n.peer.node, func() (record.Record, error) { return good, nil })
	if v := n.fetch(t); v.Profile == nil || v.Profile.Name != "inuc" {
		t.Fatalf("profile = %+v", v.Profile)
	}
}

// A peer that does not serve the protocol, or whose fetch fails, leaves no profile
// and peers still lists it.
func TestAPeerWithoutAProfileIsStillListed(t *testing.T) {
	n := newProfileNode(t, imac)
	v := n.fetch(t) // no entry in the fake network: not served
	if v.Profile != nil || v.Trust != engine.PeerTrustDirect {
		t.Fatalf("peer = %+v", v)
	}
	n.net.servesProfile(n.peer.node, func() (record.Record, error) { return record.Record{}, transport.ErrTimeout })
	if v = n.fetch(t); v.Profile != nil {
		t.Fatalf("a failed fetch left %+v", v.Profile)
	}
}

// A connection from a node that is not trusted directly is not asked, and a node
// removed from trust loses the profile held for it.
func TestTheProfileOfANodeNoLongerTrustedIsDropped(t *testing.T) {
	n := newProfileNode(t, imac)
	stranger := newPeerNode(t, 2)
	n.e.MemberConnected(context.Background(), stranger.node)
	if _, err := n.e.Quiesce(context.Background(), engine.Expect{}); err != nil {
		t.Fatal(err)
	}
	if got := n.net.profilesAsked(stranger.node); got != 0 {
		t.Errorf("a node not trusted was asked %d times", got)
	}
	rec := n.peer.profileIssuedAt(1, t0.Add(time.Minute), "inuc")
	n.net.servesProfile(n.peer.node, func() (record.Record, error) { return rec, nil })
	if v := n.fetch(t); v.Profile == nil {
		t.Fatal("no profile")
	}
	err := loopErr(t, n.e, func(l engine.LoopSide) error {
		_, err := l.RevokeNodeLocally(requestKey(0xa7), fingerprintOf("remove"), "trust.remove", n.peer.node)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := n.e.Peers(context.Background(), n.callerOf(t)); err != nil || len(got) != 1 {
		t.Fatalf("peers after the removal = %+v, %v", got, err)
	}
	// Trusted again through a new credential, the node shows no profile until a
	// connection fetches one: what was held is not brought back.
	if err := loopErr(t, n.e, func(l engine.LoopSide) error { return l.ClearLocallyRevoked(n.peer.node) }); err != nil {
		t.Fatal(err)
	}
	again := n.peer.credentialWithNonce(n.r.keys.IdentitySigner(), false, credential.Nonce{0x7e, 1})
	cert := n.peer.certificate(1)
	if err := loopErr(t, n.e, func(l engine.LoopSide) error { return l.InstallCredential(again.Record, &cert) }); err != nil {
		t.Fatal(err)
	}
	if v := n.peerView(t); v.Trust != engine.PeerTrustDirect || v.Profile != nil {
		t.Fatalf("the peer after it was trusted again = %+v", v)
	}
}

// The owner reads the profile of a peer in the views of trust and of the network
// as in peers: the one fetched, and none for a node nothing was fetched from.
func TestTheOwnersViewsCarryTheProfileOfAPeer(t *testing.T) {
	n := newProfileNode(t, imac)
	peerProfile := func() (trusted, known *engine.ProfileView) {
		t.Helper()
		tv, err := n.e.TrustView(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		nv, err := n.e.NetworkView(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(tv.Trusted) != 1 || tv.Trusted[0].Node != n.peer.node {
			t.Fatalf("trusted = %+v", tv.Trusted)
		}
		for _, p := range nv.Peers {
			if p.Node == n.peer.node {
				return tv.Trusted[0].Profile, p.Profile
			}
		}
		t.Fatalf("the peer is not in the network view: %+v", nv.Peers)
		return nil, nil
	}
	if trusted, known := peerProfile(); trusted != nil || known != nil {
		t.Fatalf("a profile before any fetch: %+v, %+v", trusted, known)
	}
	rec := n.peer.profileIssuedAt(1, t0.Add(time.Minute), "inuc")
	n.net.servesProfile(n.peer.node, func() (record.Record, error) { return rec, nil })
	n.fetch(t)
	trusted, known := peerProfile()
	for what, got := range map[string]*engine.ProfileView{"trust": trusted, "network": known} {
		if got == nil || got.Name != "inuc" || got.System != "Ubuntu 24.04.1 LTS" || got.AsOf.IsZero() {
			t.Errorf("profile in the %s view = %+v", what, got)
		}
	}
}

// waitProfilesAsked waits until the node was asked for the profile of the peer at
// least count times in all, and then until the engine has taken in every answer.
func (n *profileNode) waitProfilesAsked(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for n.net.profilesAsked(n.peer.node) < count {
		if time.Now().After(deadline) {
			t.Fatalf("the peer was asked %d times, want %d", n.net.profilesAsked(n.peer.node), count)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := n.e.Quiesce(context.Background(), engine.Expect{}); err != nil {
		t.Fatal(err)
	}
}

// A connection that comes up while a fetch from the node is under way is not lost
// with that fetch: the fetch under way can be on a connection that has died with
// the node it went to, and nothing else would ask again.
func TestAConnectionThatComesUpDuringAFetchIsAskedWhenThatFetchEnds(t *testing.T) {
	n := newProfileNode(t, imac)
	n.net.connected = map[ident.NodeID]bool{n.peer.node: true}
	good := n.peer.profileIssuedAt(1, t0.Add(time.Minute), "inuc")
	inFlight, release := make(chan struct{}), make(chan struct{})
	n.net.servesProfile(n.peer.node, func() (record.Record, error) {
		close(inFlight)
		<-release
		return record.Record{}, transport.ErrTimeout // the connection it was on died
	})
	n.e.MemberConnected(context.Background(), n.peer.node)
	<-inFlight
	n.net.servesProfile(n.peer.node, func() (record.Record, error) { return good, nil })
	n.e.MemberConnected(context.Background(), n.peer.node) // the new connection
	close(release)
	n.waitProfilesAsked(t, 2)
	if v := n.peerView(t); v.Profile == nil || v.Profile.Name != "inuc" {
		t.Fatalf("profile after the second fetch = %+v", v.Profile)
	}
	if got := n.net.profilesAsked(n.peer.node); got != 2 {
		t.Errorf("the peer was asked %d times, want 2", got)
	}
}

// A node that becomes trusted directly while a connection to it is up is asked at
// once, since the news of that connection came when it was not trusted and does not
// come again. A node whose connection is down waits for the connection.
func TestANodeTrustedDirectlyOnAConnectionThatIsUpIsAsked(t *testing.T) {
	n := newProfileNode(t, imac)
	late := newPeerNode(t, 2)
	down := newPeerNode(t, 3)
	rec := late.profileIssuedAt(1, t0.Add(time.Minute), "late")
	n.net.connected = map[ident.NodeID]bool{late.node: true}
	n.net.servesProfile(late.node, func() (record.Record, error) { return rec, nil })
	n.net.servesProfile(down.node, func() (record.Record, error) { return rec, nil })
	for _, p := range []peerNode{late, down} {
		cert := p.certificate(1)
		cred := p.credentialWith(n.r.keys.IdentitySigner(), false)
		if err := loopErr(t, n.e, func(l engine.LoopSide) error { return l.InstallCredential(cred.Record, &cert) }); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for n.net.profilesAsked(late.node) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the node trusted on a live connection was not asked")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := n.e.Quiesce(context.Background(), engine.Expect{}); err != nil {
		t.Fatal(err)
	}
	if got := n.net.profilesAsked(down.node); got != 0 {
		t.Errorf("a node with no connection was asked %d times", got)
	}
	if got := n.net.profilesAsked(late.node); got != 1 {
		t.Errorf("the node was asked %d times, want 1", got)
	}
	// Installing another credential changes trust again and must not ask for what
	// is already held.
	other := newPeerNode(t, 4)
	cert, cred := other.certificate(1), other.credentialWith(n.r.keys.IdentitySigner(), false)
	if err := loopErr(t, n.e, func(l engine.LoopSide) error { return l.InstallCredential(cred.Record, &cert) }); err != nil {
		t.Fatal(err)
	}
	if _, err := n.e.Quiesce(context.Background(), engine.Expect{}); err != nil {
		t.Fatal(err)
	}
	if got := n.net.profilesAsked(late.node); got != 1 {
		t.Errorf("a node whose profile is held was asked again: %d times", got)
	}
}

// settle advances the clock of the node by d and waits until the engine has run
// what fell due and taken in the answers of the fetches it started.
func (n *profileNode) settle(t *testing.T, d time.Duration) {
	t.Helper()
	n.r.clk.Advance(d)
	n.waitProfilesAsked(t, 0)
}

// A fetch that fails is asked again after a short wait, because the peer resets
// member streams until it has applied its side of the exchange and the connection,
// once up, is not followed by another that would ask. The profile of the retry is
// kept, and no more retries follow an answer.
func TestAFetchThatFailedIsAskedAgainOnTheClockAndTheProfileKept(t *testing.T) {
	n := newProfileNode(t, imac)
	n.net.setConnected(n.peer.node, true)
	good := n.peer.profileIssuedAt(1, t0.Add(time.Minute), "inuc")
	calls := 0
	n.net.servesProfile(n.peer.node, func() (record.Record, error) {
		calls++
		if calls == 1 {
			return record.Record{}, transport.ErrUnreachable
		}
		return good, nil
	})
	n.e.MemberConnected(context.Background(), n.peer.node)
	n.waitProfilesAsked(t, 1)
	if v := n.peerView(t); v.Profile != nil {
		t.Fatalf("a profile after a failed fetch: %+v", v.Profile)
	}
	n.settle(t, time.Second) // before the first wait ends
	if got := n.net.profilesAsked(n.peer.node); got != 1 {
		t.Fatalf("asked %d times before the wait ended", got)
	}
	n.settle(t, time.Second)
	n.waitProfilesAsked(t, 2)
	if v := n.peerView(t); v.Profile == nil || v.Profile.Name != "inuc" {
		t.Fatalf("profile after the retry = %+v", v.Profile)
	}
	n.settle(t, time.Hour)
	if got := n.net.profilesAsked(n.peer.node); got != 2 {
		t.Errorf("asked %d times after the answer, want 2", got)
	}
}

// The retries of a fetch that keeps failing are three, 2, 5 and 10 seconds after
// the failure before, and then it waits for the next connection, which starts the
// count again.
func TestTheRetriesOfAFetchAreBoundedAndANewConnectionStartsThemAgain(t *testing.T) {
	n := newProfileNode(t, imac)
	n.net.setConnected(n.peer.node, true)
	n.net.servesProfile(n.peer.node, func() (record.Record, error) { return record.Record{}, transport.ErrUnreachable })
	n.e.MemberConnected(context.Background(), n.peer.node)
	n.waitProfilesAsked(t, 1)
	for i, wait := range []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second} {
		n.settle(t, wait-time.Millisecond)
		if got := n.net.profilesAsked(n.peer.node); got != i+1 {
			t.Fatalf("asked %d times just before the wait of retry %d ended, want %d", got, i+1, i+1)
		}
		n.settle(t, time.Millisecond)
		n.waitProfilesAsked(t, i+2)
	}
	n.settle(t, time.Hour)
	if got := n.net.profilesAsked(n.peer.node); got != 4 {
		t.Fatalf("asked %d times, want the fetch and three retries", got)
	}
	n.e.MemberConnected(context.Background(), n.peer.node)
	n.waitProfilesAsked(t, 5)
	n.settle(t, 2*time.Second)
	n.waitProfilesAsked(t, 6)
}

// A retry is made only for a node that is still trusted directly and has a member
// connection up, and not for a node that does not serve the protocol: its answer
// will not change on this connection.
func TestAFetchIsNotAskedAgainOfANodeThatIsGoneUntrustedOrDoesNotServe(t *testing.T) {
	for _, c := range []struct {
		name string
		// serves is whether the peer answers the first fetch with an error, and not
		// with "not served"; before changes what the retry looks at, after the failure.
		serves bool
		before func(*testing.T, *profileNode)
	}{
		{"the connection ended", true, func(_ *testing.T, n *profileNode) { n.net.setConnected(n.peer.node, false) }},
		{"the node is no longer trusted", true, func(t *testing.T, n *profileNode) {
			err := loopErr(t, n.e, func(l engine.LoopSide) error {
				_, err := l.RevokeNodeLocally(requestKey(0xa7), fingerprintOf("remove"), "trust.remove", n.peer.node)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"the node does not serve the protocol", false, func(*testing.T, *profileNode) {}},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := newProfileNode(t, imac)
			n.net.setConnected(n.peer.node, true)
			if c.serves {
				n.net.servesProfile(n.peer.node, func() (record.Record, error) { return record.Record{}, transport.ErrUnreachable })
			}
			n.e.MemberConnected(context.Background(), n.peer.node)
			n.waitProfilesAsked(t, 1)
			c.before(t, n)
			n.settle(t, time.Hour)
			if got := n.net.profilesAsked(n.peer.node); got != 1 {
				t.Errorf("asked %d times, want 1", got)
			}
		})
	}
}
