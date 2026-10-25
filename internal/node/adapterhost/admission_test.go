package adapterhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// An adapter is admitted only for what it declared and what its manifest allows:
// otherwise the engine is never asked to take it on, and the instance is reported
// as one that could not be started.
func TestAdmissionRefusesWhatTheAdapterDidNotDeclare(t *testing.T) {
	runtimeOnly := func(o *fakeadapter.ManifestOptions) {
		o.Direct = false
		o.MaxDirectEndpoints = 0
		o.Budgets = []manifest.Budget{{Frontend: manifest.FrontendCLI, Unit: manifest.UnitChars, Limit: 27000}}
	}
	for _, tc := range []struct {
		name     string
		manifest func(*fakeadapter.ManifestOptions)
		// initialize replaces the answer of the adapter; nil is the adapter's own.
		initialize func(adapterproto.InitializeParams, *scripted) (any, error)
	}{
		{
			name:     "declares neither runtime nor direct endpoints",
			manifest: runtimeOnly,
			initialize: func(_ adapterproto.InitializeParams, a *scripted) (any, error) {
				res := a.initializeResult()
				res.Capabilities = adapterproto.Capabilities{}
				return res, nil
			},
		},
		{
			// The default manifest has a budget for the direct front end.
			name:     "a manifest budget for a front end it did not declare",
			manifest: func(o *fakeadapter.ManifestOptions) {},
			initialize: func(_ adapterproto.InitializeParams, a *scripted) (any, error) {
				res := a.initializeResult()
				res.Capabilities = adapterproto.Capabilities{Runtime: res.Capabilities.Runtime}
				return res, nil
			},
		},
		{
			name:     "another id than the manifest's",
			manifest: runtimeOnly,
			initialize: func(_ adapterproto.InitializeParams, a *scripted) (any, error) {
				res := a.initializeResult()
				res.Adapter.ID = "someone-else"
				return res, nil
			},
		},
		{
			name:     "another version than the manifest's",
			manifest: runtimeOnly,
			initialize: func(_ adapterproto.InitializeParams, a *scripted) (any, error) {
				res := a.initializeResult()
				res.Adapter.Version = "9.9.9"
				return res, nil
			},
		},
		{
			name:     "a protocol range with no common version",
			manifest: runtimeOnly,
			initialize: func(ip adapterproto.InitializeParams, _ *scripted) (any, error) {
				_, err := adapterproto.Negotiate(ip.Protocol, adapterproto.Range{Min: 900, Max: 901})
				var nc *adapterproto.NoCommonVersionError
				if !errors.As(err, &nc) {
					return nil, errors.New("the ranges overlap")
				}
				e := adapterproto.NewUnsupportedVersionError(nc)
				return nil, (*peer.Error)(&e)
			},
		},
		{
			name:     "a protocol version outside the range the daemon sent",
			manifest: runtimeOnly,
			initialize: func(_ adapterproto.InitializeParams, a *scripted) (any, error) {
				res := a.initializeResult()
				res.ProtocolVersion = 900
				return res, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pp *pipeProcess
			r := newRig(t, rigOptions{manifest: tc.manifest})
			r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
				pp, _ = r.scriptedProcess(func(a *scripted) {
					if tc.initialize != nil {
						a.initialize = func(ip adapterproto.InitializeParams) (any, error) { return tc.initialize(ip, a) }
					}
				}, false)
				return pp, nil
			}
			r.start()
			r.settle()
			if got := r.eng.of("admitted"); len(got) != 0 {
				t.Fatalf("AdapterAdmitted was called %d times for an adapter that must be refused", len(got))
			}
			if got := r.goneCauses("chat"); !slices.Equal(got, []adapterhost.GoneCause{adapterhost.GoneStartFailed}) {
				t.Fatalf("the engine was told %v, want [start_failed]", got)
			}
			if r.host.Counts("chat").Connected {
				t.Fatal("a refused adapter is in service")
			}
			eventually(t, "the refused process to be killed", func() bool { return pp.killed() == 1 })
			if got := r.eng.kinds(); !slices.Equal(got, []string{"gone"}) {
				t.Fatalf("the engine saw %v, want only the report", got)
			}
		})
	}
}

// A request of the adapter that its declaration does not allow is answered with the
// code adapterreg gives, without the engine being called; before admission every
// request but ping is -32003.
func TestACallOfACapabilityTheAdapterDidNotDeclareIsRefusedWithTheCodeOfAdmissionCheck(t *testing.T) {
	registerParams := adapterproto.RegisterParams{Key: "k", DisplayName: "d"}
	observedParams := adapterproto.RecvObservedParams{Endpoint: "s-1", Lease: "0123456789abcdef0123456789abcdef", Frontend: adapterproto.FrontendCLI, Observed: "text"}
	anything := map[string]any{}

	t.Run("after admission", func(t *testing.T) {
		r := newRig(t, rigOptions{manifest: func(o *fakeadapter.ManifestOptions) {
			o.Direct = false
			o.MaxDirectEndpoints = 0
			o.AckOnBehalf = false
			o.Budgets = []manifest.Budget{{Frontend: manifest.FrontendCLI, Unit: manifest.UnitChars, Limit: 27000}}
		}})
		var a *scripted
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, ad := r.scriptedProcess(nil, false)
			a = ad
			return pp, nil
		}
		r.start()
		r.waitConnected("chat")
		for _, tc := range []struct {
			name   string
			method string
			params any
			want   int
		}{
			{"recv.observed without ack_on_behalf", adapterproto.MethodRecvObserved, observedParams, adapterproto.CodeCapabilityNotDeclared},
			{"endpoint.register of a runtime-only adapter", adapterproto.MethodEndpointRegister, registerParams, adapterproto.CodeCapabilityNotDeclared},
			{"endpoint.send of a runtime-only adapter", adapterproto.MethodEndpointSend, anything, adapterproto.CodeCapabilityNotDeclared},
			{"a method only the daemon may call: session.create", adapterproto.MethodSessionCreate, anything, peer.CodeMethodNotFound},
			{"a method only the daemon may call: prompt.deliver", adapterproto.MethodPromptDeliver, anything, peer.CodeMethodNotFound},
			{"a method only the daemon may call: adapter.start", adapterproto.MethodAdapterStart, anything, peer.CodeMethodNotFound},
			{"a method only the daemon may call: initialize", adapterproto.MethodInitialize, anything, peer.CodeMethodNotFound},
			{"a method nobody knows", "nothing.here", anything, peer.CodeMethodNotFound},
		} {
			var res json.RawMessage
			err := a.call(tc.method, tc.params, &res)
			var pe *peer.Error
			if !errors.As(err, &pe) || pe.Code != tc.want {
				t.Errorf("%s: answered %v, want code %d", tc.name, err, tc.want)
			}
		}
		for _, kind := range []string{"register", "unregister", "checkpoint", "send", "recv", "ack", "status", "observed"} {
			if got := r.eng.of(kind); len(got) != 0 {
				t.Errorf("the engine was called %d times for %s although the adapter did not declare it", len(got), kind)
			}
		}
	})

	t.Run("before admission", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.eng.admitGate = make(chan struct{})
		var a *scripted
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, ad := r.scriptedProcess(nil, false)
			a = ad
			return pp, nil
		}
		go func() { _ = r.host.Start(timeoutCtx(t), r.env(false)) }()
		r.waitEvents("admitted", 1)
		for _, tc := range []struct {
			name   string
			method string
			params any
		}{
			{"endpoint.register", adapterproto.MethodEndpointRegister, registerParams},
			{"checkpoint.put", adapterproto.MethodCheckpointPut, anything},
			{"recv.observed", adapterproto.MethodRecvObserved, observedParams},
			{"session.create", adapterproto.MethodSessionCreate, anything},
			{"initialize", adapterproto.MethodInitialize, anything},
		} {
			var res json.RawMessage
			err := a.call(tc.method, tc.params, &res)
			var pe *peer.Error
			if !errors.As(err, &pe) || pe.Code != peer.CodeNotInitialized {
				t.Errorf("%s before admission: answered %v, want -32003", tc.name, err)
			}
		}
		// ping always answers.
		var pong adapterproto.Empty
		if err := a.call(adapterproto.MethodPing, adapterproto.Empty{}, &pong); err != nil {
			t.Errorf("ping before admission: %v", err)
		}
		if got := r.eng.kinds(); !slices.Equal(got, []string{"admitted"}) {
			t.Errorf("the engine saw %v, want only the admission", got)
		}
		close(r.eng.admitGate)
		r.waitConnected("chat")
	})
}

// The adapter that answers 1011 holds no lock: nobody is admitted, and the next
// pass waits for the backoff and does not retry in a tight loop.
func TestAnInstanceLockedAnswerAdmitsNobodyAndTheNextPassWaitsForTheBackoff(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, _ := r.scriptedProcess(func(a *scripted) {
			a.initialize = func(adapterproto.InitializeParams) (any, error) {
				return nil, peer.NewError(adapterproto.CodeInstanceLocked, "another process holds the instance lock")
			}
		}, false)
		return pp, nil
	}
	r.start()
	r.settle()
	if got := r.eng.of("admitted"); len(got) != 0 {
		t.Fatalf("AdapterAdmitted was called for an adapter that did not hold the lock")
	}
	if got := r.goneCauses("chat"); !slices.Equal(got, []adapterhost.GoneCause{adapterhost.GoneStartFailed}) {
		t.Fatalf("the engine was told %v, want [start_failed]", got)
	}
	// It is logged as what it is.
	if !r.records.contains("holds the instance lock") {
		t.Fatalf("the refusal was not logged as another process holding the lock: %v", r.records.msgs)
	}
	p := testParams()
	// No retry without the backoff, however long the host runs.
	r.settle()
	if n := r.lau.launches(); n != 1 {
		t.Fatalf("%d launches without the clock moving, want 1", n)
	}
	r.advance(p.RestartBackoffMin - 1)
	if n := r.lau.launches(); n != 1 {
		t.Fatalf("%d launches before the backoff ended, want 1", n)
	}
	r.advance(1)
	r.waitLaunches(2)
	r.waitEvents("gone", 2)
	r.settle()
	if got := r.goneCauses("chat"); len(got) != 2 {
		t.Fatalf("the engine was told %v after the second pass, want two reports", got)
	}
	// The second wait is longer: the same doubling as for any failed pass.
	r.advance(2*p.RestartBackoffMin - 1)
	if n := r.lau.launches(); n != 2 {
		t.Fatalf("%d launches before the doubled backoff ended, want 2", n)
	}
	r.advance(1)
	r.waitLaunches(3)
}

// The connection is taken on in the order initialize, admission, adapter.start,
// probe, on the wire and in the calls to the engine.
func TestAConnectionIsTakenOnInTheOrderInitializeAdmitStartProbe(t *testing.T) {
	t.Run("the order, and what each step carries", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		probe := adapterproto.ToolProbeRequest{
			Preset: "default", Frontend: adapterproto.FrontendCLI,
			Token: adapterproto.Token(strings.Repeat("A", 43)), Env: map[string]string{"SIDERA_PROBE": "1"},
		}
		r.eng.probes = []adapterproto.ToolProbeRequest{probe}
		r.eng.startParams = func(_ string, a adapterreg.Admission) adapterproto.StartParams {
			p := defaultStartParams(a)
			p.Node = "the-node"
			return p
		}
		// The engine's calls and the host's lines go onto one timeline.
		r.eng.onEvent = func(kind string) { r.tl.add("engine " + kind) }
		r.start()
		r.waitConnected("chat")
		r.waitEvents("probed", 1)

		want := []string{
			"wire initialize", "engine admitted", "wire adapter.start", "engine started",
			"engine probeTokens", "wire adapter.probe", "engine probed",
		}
		if got := r.tl.snapshot(); !slices.Equal(got, want) {
			t.Fatalf("order of the wire and the engine's calls:\n got %v\nwant %v", got, want)
		}

		// The engine is told what the adapter said and what was admitted.
		adm := r.eng.of("admitted")[0]
		if adm.Info.Adapter.ID != r.opts.ID || adm.Info.ProtocolVersion != 1 || !adm.Admitted.Declares(adapterreg.KindRuntime) {
			t.Fatalf("AdapterAdmitted got %+v", adm)
		}
		if st := r.eng.of("started")[0].Started; st.Sessions == nil || len(st.Sessions) != 0 {
			t.Fatalf("AdapterStarted got %+v", st)
		}
		pr := r.eng.of("probed")[0].Probed
		if len(pr.Presets) != 1 || pr.Presets[0].Name != "default" || len(pr.ToolProbes) != 1 || pr.ToolProbes[0].Preset != "default" {
			t.Fatalf("AdapterProbed got %+v: the probe of the engine's token did not reach the adapter", pr)
		}
	})

	t.Run("the parameters of the three requests", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		var a *scripted
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, ad := r.scriptedProcess(nil, false)
			a = ad
			return pp, nil
		}
		probe := adapterproto.ToolProbeRequest{
			Preset: "default", Frontend: adapterproto.FrontendCLI,
			Token: adapterproto.Token(strings.Repeat("A", 43)), Env: map[string]string{},
		}
		r.eng.probes = []adapterproto.ToolProbeRequest{probe}
		r.start()
		r.waitEvents("probed", 1)

		var ip adapterproto.InitializeParams
		if err := adapterproto.Decode(a.paramsOf(adapterproto.MethodInitialize)[0], &ip); err != nil {
			t.Fatal(err)
		}
		if ip.Protocol != adapterproto.SupportedRange() {
			t.Errorf("initialize protocol = %+v, want the supported range", ip.Protocol)
		}
		if ip.Instance.ID != "chat" || ip.Instance.LockPath != adapterhost.LockPath(r.home, "chat") {
			t.Errorf("initialize instance = %+v", ip.Instance)
		}
		if ip.Daemon.Version == "" {
			t.Error("initialize carries no daemon version")
		}
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(a.paramsOf(adapterproto.MethodInitialize)[0], &raw)
		if string(raw["capabilities"]) != "{}" {
			t.Errorf("the daemon's capabilities = %s, want {}", raw["capabilities"])
		}
		var sp adapterproto.StartParams
		if err := adapterproto.Decode(a.paramsOf(adapterproto.MethodAdapterStart)[0], &sp); err != nil {
			t.Fatal(err)
		}
		if sp.Node != "node-1" || sp.RuntimeEndpoint != "rt-1" {
			t.Errorf("adapter.start carried %+v, not what the engine returned", sp)
		}
		var pp adapterproto.ProbeParams
		if err := adapterproto.Decode(a.paramsOf(adapterproto.MethodAdapterProbe)[0], &pp); err != nil {
			t.Fatal(err)
		}
		if len(pp.ToolProbes) != 1 || pp.ToolProbes[0].Token != probe.Token {
			t.Errorf("adapter.probe carried %+v, not what ProbeTokens returned", pp)
		}
	})

	// A probe the adapter fails is a fact about the adapter, not the end of the
	// connection: the pings judge whether it is alive.
	for _, tc := range []struct {
		name  string
		probe func() (any, error)
	}{
		{"an error", func() (any, error) { return nil, peer.NewError(peer.CodeInternalError, "no") }},
		{"a result that is not valid", func() (any, error) { return map[string]any{"auth": "bogus"}, nil }},
	} {
		t.Run("a failed probe does not end the connection: "+tc.name, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
				pp, _ := r.scriptedProcess(func(a *scripted) {
					a.handlers[adapterproto.MethodAdapterProbe] = func(context.Context, json.RawMessage) (any, error) { return tc.probe() }
				}, false)
				return pp, nil
			}
			r.start()
			r.waitEvents("probeTokens", 1)
			r.settle()
			if got := r.eng.of("probed"); len(got) != 0 {
				t.Fatalf("AdapterProbed was called after a probe that failed: %+v", got)
			}
			if !r.host.Counts("chat").Connected {
				t.Fatal("a failed probe ended the connection")
			}
			if got := r.goneCauses("chat"); len(got) != 0 {
				t.Fatalf("a failed probe was reported: %v", got)
			}
		})
	}
}
