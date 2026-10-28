package engine

import (
	"context"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// MeView describes the caller's own endpoint and the declared front ends of its
// adapter. Its declaration and front-end map are copies the caller owns.
type MeView struct {
	Address     ident.Address
	Kind        adapterreg.EndpointKind
	DisplayName string
	Declaration authz.Declaration
	Frontends   map[adapterproto.Frontend]FrontendView
}

// FrontendView gives the output budget and the longest receive wait of one
// declared agent front end, including defaults for limits it did not declare.
type FrontendView struct {
	RecvWaitMax time.Duration
	Budget      handoff.Budget
}

type cmdMe struct {
	call[MeView]
	caller Caller
}

func (c *cmdMe) gate() gate { return gateRead }
func (c *cmdMe) run(e *Engine, _ time.Time) {
	st, err := e.view.resolve(c.caller)
	if err != nil {
		c.done(MeView{}, err)
		return
	}
	v := MeView{
		Address: ident.Address{Node: e.d.Node, Endpoint: st.rec.ID}, Kind: st.rec.Kind,
		DisplayName: st.rec.DisplayName, Declaration: cloneDeclaration(st.decl),
		Frontends: make(map[adapterproto.Frontend]FrontendView),
	}
	if adm, ok := e.admission(st.rec.Instance); ok {
		for _, f := range []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP} {
			if adm.DeclaresFrontend(f) {
				v.Frontends[f] = FrontendView{RecvWaitMax: adm.RecvWait(f), Budget: adm.Budget(manifest.Frontend(f))}
			}
		}
	}
	c.done(v, nil)
}

// Me returns the endpoint resolved for this caller in the read command, with
// only the front ends that its adapter has declared on its current connection.
func (e *Engine) Me(ctx context.Context, caller Caller) (MeView, error) {
	c := &cmdMe{call: newCall[MeView](), caller: caller}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

// PeerView names one visible node. Self identifies the node serving this caller.
type PeerView struct {
	Node ident.NodeID
	Self bool
}

type cmdPeers struct {
	call[[]PeerView]
	caller Caller
}

func (c *cmdPeers) gate() gate { return gateRead }
func (c *cmdPeers) run(e *Engine, _ time.Time) {
	if _, err := e.view.resolve(c.caller); err != nil {
		c.done(nil, err)
		return
	}
	c.done([]PeerView{{Node: e.d.Node, Self: true}}, nil)
}

// Peers returns the nodes visible to this caller. Until a network transport is
// connected, the node itself is the only one known.
func (e *Engine) Peers(ctx context.Context, caller Caller) ([]PeerView, error) {
	c := &cmdPeers{call: newCall[[]PeerView](), caller: caller}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}
