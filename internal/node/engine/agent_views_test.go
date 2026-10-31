package engine_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

func TestMeShowsTheCallersDeclarationAndDeclaredFrontendLimits(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	m := mustManifest(t)
	m.Budgets = []manifest.Budget{{Frontend: manifest.FrontendCLI, Unit: manifest.UnitTokens, Limit: 1234}}
	m.RecvWaitLimits = []manifest.RecvWaitLimit{{Frontend: adapterproto.FrontendCLI, MaxS: 17}}
	var err error
	r.admit, err = adapterreg.Admit(adapterproto.Capabilities{
		Runtime: &adapterproto.RuntimeCapabilities{Frontends: []adapterproto.Frontend{adapterproto.FrontendCLI}},
		Direct:  &adapterproto.DirectCapabilities{MaxEndpoints: 10},
	}, m)
	if err != nil {
		t.Fatal(err)
	}
	e := r.start()
	id := register(t, e, "own")
	caller := engine.ByAdapter("chat", adapterproto.EndpointRef(id.String()))
	accept := []ident.Address{r.addr(8)}
	if _, err := e.Declare(context.Background(), caller, engine.DeclarationPatch{Paused: ptr(true), AcceptFrom: &accept}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Me(context.Background(), caller)
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != (ident.Address{Node: r.node, Endpoint: id}) || got.Kind != adapterreg.KindDirect || got.DisplayName != registerParams("own").DisplayName {
		t.Errorf("Me identity = %+v, want the caller's registered endpoint", got)
	}
	if !got.Declaration.Paused || len(got.Declaration.AcceptFrom) != 1 || got.Declaration.AcceptFrom[0] != accept[0] {
		t.Errorf("Me declaration = %+v, want the caller's patch", got.Declaration)
	}
	if len(got.Frontends) != 1 || got.Frontends[adapterproto.FrontendCLI].RecvWaitMax != 17*time.Second || got.Frontends[adapterproto.FrontendCLI].Budget != (handoff.Budget{Unit: handoff.UnitTokens, Limit: 1234}) {
		t.Errorf("Me frontends = %+v, want only cli with its declared limits", got.Frontends)
	}
	// The response belongs to the caller; changing its lists cannot change the node.
	got.Declaration.AcceptFrom[0] = r.addr(9)
	again, err := e.Me(context.Background(), caller)
	if err != nil || again.Declaration.AcceptFrom[0] != accept[0] {
		t.Errorf("Me after editing its response = %+v, %v; the node's declaration changed", again, err)
	}
}

func TestPeersShowsThisNodeAndEveryNodeTrustedDirectlyWithItsConnection(t *testing.T) {
	noLeaks(t)
	first, second := newPeerNode(t, 1), newPeerNode(t, 2)
	if first.node.String() > second.node.String() {
		first, second = second, first
	}
	net := &fakeNetwork{connected: map[ident.NodeID]bool{second.node: true}}
	r := trustRig(t, withNetwork(net))
	e := r.start()
	id := register(t, e, "own")
	caller := engine.ByAdapter("chat", adapterproto.EndpointRef(id.String()))
	peers := func() []engine.PeerView {
		t.Helper()
		got, err := e.Peers(context.Background(), caller)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	self := engine.PeerView{Node: r.node, Trust: engine.PeerTrustSelf, Connected: true}
	if got := peers(); !slices.Equal(got, []engine.PeerView{self}) {
		t.Fatalf("Peers without a credential = %v, want only this node", got)
	}
	creds := map[ident.NodeID]store.CredentialRecord{}
	for _, p := range []peerNode{second, first} {
		creds[p.node] = p.credentialWith(r.keys.IdentitySigner(), false)
		if err := loopErr(t, e, func(l engine.LoopSide) error { return l.InstallCredential(creds[p.node].Record, nil) }); err != nil {
			t.Fatal(err)
		}
	}
	want := []engine.PeerView{self,
		{Node: first.node, Trust: engine.PeerTrustDirect},
		{Node: second.node, Trust: engine.PeerTrustDirect, Connected: true}}
	if got := peers(); !slices.Equal(got, want) {
		t.Fatalf("Peers with two credentials = %v, want %v", got, want)
	}
	if err := loopErr(t, e, func(l engine.LoopSide) error { return l.RevokeCredential(creds[first.node].Digest) }); err != nil {
		t.Fatal(err)
	}
	if got := peers(); !slices.Equal(got, []engine.PeerView{self, want[2]}) {
		t.Fatalf("Peers after one credential was revoked = %v, want this node and the other one", got)
	}
}

func TestAgentReadsResolveTheirCallerAtTheTimeOfTheRead(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	id := register(t, e, "own")
	caller := engine.ByAdapter("chat", adapterproto.EndpointRef(id.String()))
	reads := []struct {
		name string
		read func(engine.Caller) error
	}{
		{"Me", func(c engine.Caller) error { _, err := e.Me(context.Background(), c); return err }},
		{"Peers", func(c engine.Caller) error { _, err := e.Peers(context.Background(), c); return err }},
	}
	for _, read := range reads {
		t.Run(read.name+" refuses an absent caller", func(t *testing.T) {
			wantReason(t, read.read(engine.Caller{}), engine.ReasonUnauthenticated)
		})
	}
	if _, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: id}); err != nil {
		t.Fatal(err)
	}
	for _, read := range reads {
		t.Run(read.name+" refuses the caller after its endpoint closed", func(t *testing.T) {
			wantReason(t, read.read(caller), engine.ReasonEndpointUnknown)
		})
	}
}
