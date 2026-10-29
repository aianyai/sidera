package acceptance

import (
	"context"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// environment keeps script identities and client writers across daemon generations.
// It owns no daemon business state; every operation resolves the fixture's clients.
type environment struct {
	fixture    *Fixture
	ctx        context.Context
	generation *generation
	clockErr   error
	endpoints  map[string]directEndpoint
	agents     map[string]fakeagent.Agent
	node       ident.NodeID
}

type directEndpoint struct {
	instance string
	ref      adapterproto.EndpointRef
	address  string
}

var _ scenario.Env = (*environment)(nil)

func newEnvironment(ctx context.Context, f *Fixture) *environment {
	return &environment{fixture: f, ctx: ctx, generation: f.current, endpoints: map[string]directEndpoint{}, agents: map[string]fakeagent.Agent{}}
}
func envFrom(e scenario.Env) *environment   { return e.(*environment) }
func (e *environment) Ctx() context.Context { return e.ctx }
func (e *environment) check() error {
	if e.clockErr != nil {
		return e.clockErr
	}
	return e.ctx.Err()
}

// resetGeneration is called only by the backend after it has judged the previous
// generation's trace and actual exit, and started a distinct generation. Ordinary
// RPC failures cannot clear themselves through Quiesce or another operation.
func (e *environment) resetGeneration(ctx context.Context) error {
	if e.generation == nil || !e.generation.p.exited() || e.fixture.current == nil || e.fixture.current == e.generation {
		return fmt.Errorf("acceptance: environment reset requires a new daemon generation after actual exit")
	}
	e.ctx, e.generation, e.clockErr = ctx, e.fixture.current, nil
	return nil
}

func (e *environment) Advance(d time.Duration) { e.Set(e.fixture.Now().Add(d)) }
func (e *environment) Set(t time.Time) {
	if e.clockErr == nil {
		e.clockErr = e.fixture.SetTime(e.ctx, t)
	}
}
func (e *environment) Quiesce(opts ...scenario.QuiesceOption) error {
	if err := e.check(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.ctx, scenario.ResolveQuiesce(opts).Timeout)
	defer cancel()
	if err := e.fixture.waitProbes(ctx); err != nil {
		return err
	}
	return e.fixture.Quiesce(ctx)
}
func (e *environment) BeginStep(name string) error {
	if err := e.check(); err != nil {
		return err
	}
	return e.fixture.BeginStep(e.ctx, name)
}
func (e *environment) Adapter(instance string) (fakeadapter.Controller, error) {
	if err := e.check(); err != nil {
		return nil, err
	}
	return e.fixture.Adapter(e.ctx, instance)
}
func (e *environment) Runtime() (fakeruntime.Controller, error) {
	if err := e.check(); err != nil {
		return nil, err
	}
	if err := e.fixture.syncConnections(e.ctx); err != nil {
		return nil, err
	}
	return e.fixture.Runtime(), nil
}
func (e *environment) Ledger() (*ledger.Log, error) {
	if err := e.check(); err != nil {
		return nil, err
	}
	return e.fixture.Ledger()
}

func (e *environment) localNode() (ident.NodeID, error) {
	if err := e.check(); err != nil {
		return ident.NodeID{}, err
	}
	if e.node != (ident.NodeID{}) {
		return e.node, nil
	}
	raw, err := e.fixture.Manage(e.ctx, "status", manageapi.Request{})
	if err != nil {
		return ident.NodeID{}, err
	}
	var status struct {
		Node struct {
			ID string `json:"id"`
		} `json:"node"`
	}
	if err = resultBody(raw, &status); err != nil {
		return ident.NodeID{}, err
	}
	e.node, err = ident.ParseNodeID(status.Node.ID)
	return e.node, err
}

func (e *environment) Direct(name string) (string, error) {
	return e.directOn(e.fixture.profile.instance(), name)
}
func (e *environment) directOn(instance, name string) (string, error) {
	ctl, err := e.Adapter(instance)
	if err != nil {
		return "", err
	}
	if known, ok := e.endpoints[name]; ok && known.instance != instance {
		return "", fmt.Errorf("acceptance: endpoint %q already belongs to %q", name, known.instance)
	}
	res, err := ctl.Register(e.ctx, adapterproto.RegisterParams{Key: name, DisplayName: "Name of " + name})
	if err != nil {
		return "", directError(err)
	}
	node, err := e.localNode()
	if err != nil {
		return "", err
	}
	id, err := ident.ParseEndpointID(string(res.Endpoint))
	if err != nil {
		return "", err
	}
	address := ident.Address{Node: node, Endpoint: id}.String()
	e.endpoints[name] = directEndpoint{instance: instance, ref: res.Endpoint, address: address}
	return address, nil
}
func (e *environment) Addr(name string) string { return e.endpoint(name).address }
func (e *environment) endpoint(name string) directEndpoint {
	ep, ok := e.endpoints[name]
	if !ok {
		panic(fmt.Sprintf("acceptance: no endpoint registered as %q", name))
	}
	return ep
}
func (e *environment) Agent(name string) fakeagent.Agent {
	if a, ok := e.agents[name]; ok {
		return a
	}
	ep := e.endpoint(name)
	w, err := e.fixture.files.Open("agent/"+string(ep.ref), 0, e.fixture.Now)
	if err != nil {
		panic(err)
	}
	e.fixture.agentWriters = append(e.fixture.agentWriters, w)
	// Registration already resolved the node identity. Creating a recording
	// wrapper must not turn a retained RPC failure into a panic before its call.
	system := ident.Address{Node: e.node, Endpoint: ident.SystemEndpoint()}.String()
	a := fakeagent.Recording(&directAgent{env: e, endpoint: ep, routes: map[adapterproto.MessageID]adapterproto.Address{}, leases: map[adapterproto.LeaseRef][]adapterproto.PartRef{}}, w, string(ep.ref), fakeagent.RecordingOptions{ModelSaw: true, Feedback: func(from, _ string) (string, bool) { return "notice", from == system }})
	e.agents[name] = a
	return a
}
