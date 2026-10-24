package authz

import (
	"math/rand"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/trust"
)

// localSend builds a message.send request from endpoint 1 of node 1 (this node)
// to local endpoint 10, checked at intake; tests override fields.
func localSend() LocalRequest {
	return LocalRequest{
		Stage:  StageIntake,
		Sender: Sender{Node: node(1), Endpoint: ep(1)},
		Action: ActionMessageSend,
		Target: Target{Kind: TargetEndpoint, Endpoint: ep(10)},
	}
}

func withLocal(r LocalRequest, f func(*LocalRequest)) LocalRequest {
	f(&r)
	return r
}

func bothStages(r LocalRequest) []LocalRequest {
	return []LocalRequest{
		withLocal(r, func(r *LocalRequest) { r.Stage = StageIntake }),
		withLocal(r, func(r *LocalRequest) { r.Stage = StageRecv }),
	}
}

// EvaluateLocal reads no node rule: its verdict is the request's shape and the
// target's own declarations, whatever the rows below ask of it.
func TestEvaluateLocal(t *testing.T) {
	listed := Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{addr(1, 1)}}
	unlisted := Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{addr(1, 2)}}
	sees := func(r *LocalRequest) { r.Action = ActionSee }

	tests := []struct {
		name string
		req  LocalRequest
		// want is the reason at the intake check; wantRecv is the reason at the
		// recv check, equal to want unless stated.
		want     Reason
		wantRecv Reason
	}{
		// Allowed with nothing but the declarations to consult.
		{name: "a local sender needs no rule: a plain message is granted", req: localSend(), want: ReasonGranted},
		{name: "a local sender is granted every action the target kind takes",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.Action = ActionSessionClose }),
			want: ReasonGranted},
		{name: "a local session.create has only its shape judged (the preset is judged by LocalCreate)",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.Action = ActionSessionCreate
				r.Target = Target{Kind: TargetRuntime, Endpoint: ep(20)}
			}),
			want: ReasonGranted},
		{name: "a local node-level request from an endpoint is granted",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.Action = ActionRelayReserve
				r.Target = Target{Kind: TargetNode}
			}),
			want: ReasonGranted},

		// Declarations still restrict local senders.
		{name: "declaration paused: refused at intake, handed out at recv",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.TargetDeclaration = Declaration{Paused: true} }),
			want: ReasonEndpointPaused, wantRecv: ReasonGranted},
		{name: "declaration paused does not touch a control signal",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.Action = ActionEndpointStatus
				r.TargetDeclaration = Declaration{Paused: true}
			}),
			want: ReasonGranted},
		{name: "declaration refuse-multi-recipient: refused for a multi-recipient message at both checks",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.TargetDeclaration = Declaration{RefuseMultiRecipient: true}
				r.MultiRecipient = true
			}),
			want: ReasonEndpointRefusesMultiRecipient},
		{name: "declaration refuse-multi-recipient: a single-recipient message is granted",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.TargetDeclaration = Declaration{RefuseMultiRecipient: true} }),
			want: ReasonGranted},
		{name: "declaration multi-recipient: a multi-recipient message to an endpoint that does not refuse it is granted",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.MultiRecipient = true }),
			want: ReasonGranted},
		{name: "declaration accept-only-listed: an unlisted sender is refused at both checks",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.TargetDeclaration = unlisted }),
			want: ReasonSenderNotAccepted},
		{name: "declaration accept-only-listed: a listed sender is granted",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.TargetDeclaration = listed }),
			want: ReasonGranted},
		{name: "declaration accept-only-listed: an empty list refuses every sender",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.TargetDeclaration = Declaration{RestrictSenders: true} }),
			want: ReasonSenderNotAccepted},
		{name: "declaration accept-only-listed: the same endpoint of another node is not listed",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.TargetDeclaration = listed
				r.Sender.Node = node(2)
			}),
			want: ReasonSenderNotAccepted},
		{name: "declaration accept-only-listed: a verified reply lifts it",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.TargetDeclaration = unlisted
				r.Reply = true
			}),
			want: ReasonGranted},
		{name: "declaration paused: a verified reply does not lift it at intake",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.TargetDeclaration = Declaration{Paused: true}
				r.Reply = true
			}),
			want: ReasonEndpointPaused, wantRecv: ReasonGranted},
		{name: "declaration refuse-multi-recipient: a verified reply does not lift it",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.TargetDeclaration = Declaration{RefuseMultiRecipient: true}
				r.MultiRecipient = true
				r.Reply = true
			}),
			want: ReasonEndpointRefusesMultiRecipient},
		{name: "a verified reply is granted outright: there is no rule for it to stand in for",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.Reply = true }),
			want: ReasonGranted},
		{name: "declaration hidden: see on the endpoint is refused at both checks",
			req:  withLocal(localSend(), func(r *LocalRequest) { sees(r); r.TargetDeclaration = Declaration{HiddenFromDirectory: true} }),
			want: ReasonEndpointHidden},
		{name: "declaration hidden: a hidden endpoint can still be messaged",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.TargetDeclaration = Declaration{HiddenFromDirectory: true} }),
			want: ReasonGranted},
		{name: "declaration hidden: see on the node is not affected",
			req: withLocal(localSend(), func(r *LocalRequest) {
				sees(r)
				r.Target = Target{Kind: TargetNode}
				r.TargetDeclaration = Declaration{HiddenFromDirectory: true}
			}),
			want: ReasonGranted},
		{name: "declaration sender list: a status request ignores it (it applies to message.send)",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.Action = ActionEndpointStatus
				r.TargetDeclaration = unlisted
			}),
			want: ReasonGranted},

		// Shape errors.
		{name: "malformed: zero sender node", req: withLocal(localSend(), func(r *LocalRequest) { r.Sender.Node = ident.NodeID{} }), want: ReasonMalformedRequest},
		{name: "malformed: unknown action", req: withLocal(localSend(), func(r *LocalRequest) { r.Action = 99 }), want: ReasonMalformedRequest},
		{name: "malformed: unset action", req: withLocal(localSend(), func(r *LocalRequest) { r.Action = 0 }), want: ReasonMalformedRequest},
		{name: "malformed: action cannot target that kind",
			req: withLocal(localSend(), func(r *LocalRequest) { r.Target = Target{Kind: TargetNode} }), want: ReasonMalformedRequest},
		{name: "malformed: endpoint target without an endpoint",
			req: withLocal(localSend(), func(r *LocalRequest) { r.Target.Endpoint = ident.EndpointID{} }), want: ReasonMalformedRequest},
		{name: "malformed: node target with an endpoint",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.Action = ActionRelayReserve
				r.Target = Target{Kind: TargetNode, Endpoint: ep(1)}
			}),
			want: ReasonMalformedRequest},
		{name: "malformed: unset target kind", req: withLocal(localSend(), func(r *LocalRequest) { r.Target.Kind = 0 }), want: ReasonMalformedRequest},
		{name: "malformed: every local source is an endpoint, so a zero sender endpoint is refused, node-level request included",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.Action = ActionRelayReserve
				r.Sender.Endpoint = ident.EndpointID{}
				r.Target = Target{Kind: TargetNode}
			}),
			want: ReasonMalformedRequest},
		{name: "malformed: a zero sender endpoint is refused for an endpoint target too",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.Sender.Endpoint = ident.EndpointID{} }),
			want: ReasonMalformedRequest},
		{name: "malformed: the system endpoint is not a sender",
			req:  withLocal(localSend(), func(r *LocalRequest) { r.Sender.Endpoint = ident.SystemEndpoint() }),
			want: ReasonMalformedRequest},
		{name: "a shape error is reported before the declarations speak",
			req: withLocal(localSend(), func(r *LocalRequest) {
				r.Action = 0
				r.TargetDeclaration = Declaration{Paused: true}
			}),
			want: ReasonMalformedRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantRecv := tc.wantRecv
			if wantRecv == 0 {
				wantRecv = tc.want
			}
			want := map[Stage]Reason{StageIntake: tc.want, StageRecv: wantRecv}
			for _, r := range bothStages(tc.req) {
				got := EvaluateLocal(r)
				if w := want[r.Stage]; got.Reason != w || got.Outcome != w.Outcome() {
					t.Errorf("stage %d: got %v/%v, want %v/%v", r.Stage, got.Outcome, got.Reason, w.Outcome(), w)
				}
				if len(got.Obligations) != 0 {
					t.Errorf("stage %d: obligations %+v", r.Stage, got.Obligations)
				}
			}
		})
	}
}

// The stage says which of the two checks is being made; a caller that forgets it
// is refused rather than given one of them silently.
func TestEvaluateLocalRefusesAnUnsetOrUnknownStage(t *testing.T) {
	for _, st := range []Stage{0, 3, 255} {
		got := EvaluateLocal(withLocal(localSend(), func(r *LocalRequest) { r.Stage = st }))
		if got.Outcome != OutcomeReject || got.Reason != ReasonMalformedRequest {
			t.Errorf("stage %d: %v/%v, want reject/malformed_request", st, got.Outcome, got.Reason)
		}
	}
}

// Local sources are judged by endpoint declarations and resource limits only.
// A policy that refuses everything, or a deny that names the sender, is a node
// rule, and EvaluateLocal has no way to read one: the same facts that Evaluate
// refuses under such a policy are granted locally.
func TestEvaluateLocalIgnoresNodeRules(t *testing.T) {
	nothing := mustPolicy(t)
	denied := mustPolicy(t,
		Allow(Members(), AllLocalEndpoints(), ActionMessageSend, Conditions{}),
		Deny(AnyEndpointOf(node(1)), AllLocalEndpoints(), ActionMessageSend, Conditions{}))
	capped := mustPolicy(t, Allow(Members(), AllLocalEndpoints(), ActionMessageSend,
		Conditions{Rate: RateCap{Count: 1, Window: 60e9}, MaxBodyBytes: 10}))

	for name, p := range map[string]Policy{"an empty policy": nothing, "a deny naming the sender": denied, "a rate cap and a body cap": capped} {
		remote := send()
		remote.BodySize = 1000
		if got := Evaluate(p, remote); got.Outcome == OutcomeAllow && len(got.Obligations) == 0 {
			t.Fatalf("%s: the node policy does not constrain the request, so the comparison proves nothing: %+v", name, got)
		}
		for _, r := range bothStages(localSend()) {
			got := EvaluateLocal(r)
			if got.Outcome != OutcomeAllow || got.Reason != ReasonGranted || len(got.Obligations) != 0 {
				t.Errorf("%s, stage %d: local verdict %+v, want allow/granted without obligations", name, r.Stage, got)
			}
		}
	}
}

// A local source is never suspended, since it has no connection trust to lapse,
// and never carries a rate obligation, since only node rules create them.
func TestEvaluateLocalNeverSuspendsAndHasNoObligations(t *testing.T) {
	targets := []Target{{Kind: TargetNode}, {Kind: TargetEndpoint, Endpoint: ep(10)}, {Kind: TargetRuntime, Endpoint: ep(20)}}
	decls := []Declaration{{}, {Paused: true}, {RefuseMultiRecipient: true}, {RestrictSenders: true}, {HiddenFromDirectory: true}}
	visited := 0
	for _, tg := range targets {
		for _, a := range append([]Action{0, 99}, Actions...) {
			for _, st := range []Stage{0, StageIntake, StageRecv, 7} {
				for _, d := range decls {
					for _, reply := range []bool{false, true} {
						for _, multi := range []bool{false, true} {
							r := LocalRequest{Stage: st, Sender: Sender{Node: node(1), Endpoint: ep(1)},
								Action: a, Target: tg, TargetDeclaration: d, Reply: reply, MultiRecipient: multi}
							got := EvaluateLocal(r)
							visited++
							if got.Outcome == OutcomeSuspend || got.Reason == ReasonTrustSuspended || len(got.Obligations) != 0 {
								t.Fatalf("got %+v for %+v", got, r)
							}
							if got.Outcome != got.Reason.Outcome() {
								t.Fatalf("outcome %v does not match reason %v", got.Outcome, got.Reason)
							}
						}
					}
				}
			}
		}
	}
	if visited < 1000 {
		t.Errorf("only %d requests visited", visited)
	}
}

// The system endpoint only sends the node's own channel feedback, which is not
// judged by this package, so no request may come from it: an endpoint part is
// chosen by the sending node and a remote node could otherwise forge feedback.
func TestSystemEndpointAsSenderIsRefused(t *testing.T) {
	sys := ident.SystemEndpoint()
	for _, stage := range []Stage{StageIntake, StageRecv} {
		for _, reply := range []bool{false, true} {
			r := with(send(), func(r *Request) { r.Stage = stage; r.Reply = reply; r.Sender.Endpoint = sys })
			if got := Evaluate(DefaultPolicy(), r); got.Outcome != OutcomeReject || got.Reason != ReasonMalformedRequest {
				t.Errorf("Evaluate, stage %d, reply %v: %v/%v, want reject/malformed_request", stage, reply, got.Outcome, got.Reason)
			}
			lr := withLocal(localSend(), func(r *LocalRequest) { r.Stage = stage; r.Reply = reply; r.Sender.Endpoint = sys })
			if got := EvaluateLocal(lr); got.Outcome != OutcomeReject || got.Reason != ReasonMalformedRequest {
				t.Errorf("EvaluateLocal, stage %d, reply %v: %v/%v, want reject/malformed_request", stage, reply, got.Outcome, got.Reason)
			}
		}
	}
	t.Run("the refusal is the shape check's: denied trust still answers first, and suspended or inherited trust does not hide it", func(t *testing.T) {
		for _, tr := range []struct {
			name string
			set  func(*Request)
			want Reason
		}{
			{"denied", func(r *Request) { r.Trust = trust.Denied }, ReasonTrustDenied},
			{"suspended", func(r *Request) { r.Trust = suspended }, ReasonMalformedRequest},
			{"inherited", func(r *Request) { r.Trust = inherited }, ReasonMalformedRequest},
		} {
			r := with(send(), func(r *Request) { r.Sender.Endpoint = sys })
			tr.set(&r)
			if got := Evaluate(DefaultPolicy(), r); got.Reason != tr.want {
				t.Errorf("%s: %v, want %v", tr.name, got.Reason, tr.want)
			}
		}
	})
	t.Run("an explicit deny does not get to answer for it", func(t *testing.T) {
		p := mustPolicy(t, Deny(Members(), AllLocalEndpoints(), ActionMessageSend, Conditions{}))
		r := with(send(), func(r *Request) { r.Sender.Endpoint = sys })
		if got := Evaluate(p, r); got.Reason != ReasonMalformedRequest {
			t.Errorf("got %v, want malformed_request", got.Reason)
		}
	})
	t.Run("only the reserved value is refused: a neighbouring endpoint and a node-level sender pass", func(t *testing.T) {
		b := make([]byte, ident.EndpointIDLen)
		for i := range b {
			b[i] = 0xff
		}
		b[len(b)-1] = 0xfe
		neighbour, err := ident.EndpointIDFromBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		if neighbour.IsSystem() {
			t.Fatal("test value is the system endpoint")
		}
		if got := Evaluate(DefaultPolicy(), with(send(), func(r *Request) { r.Sender.Endpoint = neighbour })); got.Reason != ReasonGranted {
			t.Errorf("Evaluate neighbour: %v", got.Reason)
		}
		if got := EvaluateLocal(withLocal(localSend(), func(r *LocalRequest) { r.Sender.Endpoint = neighbour })); got.Reason != ReasonGranted {
			t.Errorf("EvaluateLocal neighbour: %v", got.Reason)
		}
		if got := Evaluate(DefaultPolicy(), relay()); got.Reason != ReasonGranted {
			t.Errorf("Evaluate node-level sender: %v", got.Reason)
		}
	})
}

// A session.create from a local source depends on the preset's own statement
// about local sources and on nothing else.
func TestLocalCreate(t *testing.T) {
	tests := []struct {
		name string
		in   PresetLocalAccess
		want Verdict
	}{
		{"a preset that declares itself available to local sources is allowed", PresetLocalAllow,
			Verdict{Outcome: OutcomeAllow, Reason: ReasonGranted}},
		{"a preset that declares itself closed to local sources is refused", PresetLocalDeny,
			Verdict{Outcome: OutcomeReject, Reason: ReasonPresetClosedToLocalSources}},
		{"the zero value is no statement: refused as malformed, never allowed", 0,
			Verdict{Outcome: OutcomeReject, Reason: ReasonMalformedRequest}},
		{"a value past the two statements is no statement either", PresetLocalDeny + 1,
			Verdict{Outcome: OutcomeReject, Reason: ReasonMalformedRequest}},
		{"the largest value is no statement either", PresetLocalAccess(255),
			Verdict{Outcome: OutcomeReject, Reason: ReasonMalformedRequest}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := LocalCreate(tc.in)
			if got.Outcome != tc.want.Outcome || got.Reason != tc.want.Reason || len(got.Obligations) != 0 {
				t.Errorf("LocalCreate(%d) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
	t.Run("the statements have distinct non-zero values and the new reason has a stable name", func(t *testing.T) {
		if PresetLocalAllow == 0 || PresetLocalDeny == 0 || PresetLocalAllow == PresetLocalDeny {
			t.Errorf("allow=%d deny=%d", PresetLocalAllow, PresetLocalDeny)
		}
		if ReasonPresetClosedToLocalSources.String() != "preset_closed_to_local_sources" {
			t.Errorf("name %q", ReasonPresetClosedToLocalSources.String())
		}
		if ReasonPresetClosedToLocalSources.Outcome() != OutcomeReject {
			t.Errorf("outcome %v", ReasonPresetClosedToLocalSources.Outcome())
		}
	})
}

// modelLocal is the hand-written model of what a local source is told: the shape
// rules, then the declarations of the target. It shares nothing with the code
// under test: it is a list of conditions in the order a person would check them.
func modelLocal(r LocalRequest) Reason {
	validPairs := map[Action][]TargetKind{
		ActionSee:              {TargetEndpoint, TargetRuntime, TargetNode},
		ActionMessageSend:      {TargetEndpoint},
		ActionSessionCreate:    {TargetRuntime},
		ActionSessionClose:     {TargetEndpoint},
		ActionSessionInterrupt: {TargetEndpoint},
		ActionEndpointStatus:   {TargetEndpoint, TargetRuntime},
		ActionRelayReserve:     {TargetNode},
		ActionRelayConnect:     {TargetNode},
	}
	kinds, known := validPairs[r.Action]
	pairOK := false
	for _, k := range kinds {
		pairOK = pairOK || k == r.Target.Kind
	}
	switch {
	case r.Sender.Node == (ident.NodeID{}):
		return ReasonMalformedRequest
	case !known || !pairOK:
		return ReasonMalformedRequest
	case r.Target.Kind == TargetNode && r.Target.Endpoint != (ident.EndpointID{}):
		return ReasonMalformedRequest
	case r.Target.Kind != TargetNode && r.Target.Endpoint == (ident.EndpointID{}):
		return ReasonMalformedRequest
	case r.Stage != StageIntake && r.Stage != StageRecv:
		return ReasonMalformedRequest
	case r.Sender.Endpoint == (ident.EndpointID{}):
		return ReasonMalformedRequest
	case r.Sender.Endpoint == ident.SystemEndpoint():
		return ReasonMalformedRequest
	}
	d := r.TargetDeclaration
	if r.Action == ActionMessageSend {
		if d.Paused && r.Stage == StageIntake {
			return ReasonEndpointPaused
		}
		if d.RefuseMultiRecipient && r.MultiRecipient {
			return ReasonEndpointRefusesMultiRecipient
		}
		if d.RestrictSenders && !r.Reply {
			listed := false
			for _, a := range d.AcceptFrom {
				if a.Node == r.Sender.Node && a.Endpoint == r.Sender.Endpoint {
					listed = true
				}
			}
			if !listed {
				return ReasonSenderNotAccepted
			}
		}
	}
	if r.Action == ActionSee && d.HiddenFromDirectory && r.Target.Kind != TargetNode {
		return ReasonEndpointHidden
	}
	return ReasonGranted
}

func randomLocalRequest(rng *rand.Rand) LocalRequest {
	nodes := []ident.NodeID{{}, node(1), node(2), node(3)}
	senderEps := []ident.EndpointID{{}, ep(1), ep(2), ident.SystemEndpoint()}
	targetEps := []ident.EndpointID{{}, ep(10), ep(11)}
	kinds := []TargetKind{0, TargetEndpoint, TargetRuntime, TargetNode}
	r := LocalRequest{
		Stage:  pick(rng, []Stage{0, StageIntake, StageIntake, StageRecv, StageRecv}),
		Sender: Sender{Node: pick(rng, nodes), Endpoint: pick(rng, senderEps)},
		Action: pick(rng, append([]Action{0, 99}, Actions...)),
		Target: Target{Kind: pick(rng, kinds), Endpoint: pick(rng, targetEps)},

		MultiRecipient:    rng.Intn(2) == 0,
		Reply:             rng.Intn(2) == 0,
		TargetDeclaration: randomDeclaration(rng),
	}
	// Bias toward requests that pass the shape check, so the declarations are
	// reached often enough to be judged.
	if rng.Intn(3) != 0 {
		r.Stage = pick(rng, []Stage{StageIntake, StageRecv})
		r.Sender.Node = pick(rng, nodes[1:])
		r.Sender.Endpoint = pick(rng, senderEps[1:3])
		// A small share of the biased requests are well formed except for their
		// sender's endpoint, so each endpoint-only refusal is reached often.
		switch rng.Intn(10) {
		case 0:
			r.Sender.Endpoint = ident.SystemEndpoint()
		case 1:
			r.Sender.Endpoint = ident.EndpointID{}
		}
		r.Action = pick(rng, Actions)
		for _, k := range []TargetKind{TargetEndpoint, TargetRuntime, TargetNode} {
			if r.Action.appliesTo(k) && rng.Intn(2) == 0 {
				r.Target.Kind = k
			}
		}
		if !r.Action.appliesTo(r.Target.Kind) {
			for _, k := range []TargetKind{TargetEndpoint, TargetRuntime, TargetNode} {
				if r.Action.appliesTo(k) {
					r.Target.Kind = k
				}
			}
		}
		r.Target.Endpoint = pick(rng, targetEps[1:])
		if r.Target.Kind == TargetNode {
			r.Target.Endpoint = ident.EndpointID{}
		}
	}
	return r
}

// EvaluateLocal agrees with the hand-written model on random requests, with
// every reason it can give actually reached.
func TestEvaluateLocalAgreesWithModel(t *testing.T) {
	rng := rand.New(rand.NewSource(46))
	reached := map[Reason]int{}
	stages := map[Stage]int{}
	grantedByReply := 0
	// Requests refused only because of their sender's endpoint: the same request
	// from an ordinary endpoint is not malformed.
	onlySystemSender, onlyZeroSenderEndpoint := 0, 0
	for i := 0; i < 200000; i++ {
		r := randomLocalRequest(rng)
		want := modelLocal(r)
		got := EvaluateLocal(r)
		if got.Reason != want || got.Outcome != want.Outcome() || len(got.Obligations) != 0 {
			t.Fatalf("got %+v, model says %v for %+v", got, want, r)
		}
		reached[want]++
		if want != ReasonMalformedRequest {
			stages[r.Stage]++
		} else if r.Sender.Endpoint == ident.SystemEndpoint() || r.Sender.Endpoint == (ident.EndpointID{}) {
			ordinary := withLocal(r, func(r *LocalRequest) { r.Sender.Endpoint = ep(1) })
			switch {
			case modelLocal(ordinary) == ReasonMalformedRequest:
			case r.Sender.Endpoint == ident.SystemEndpoint():
				onlySystemSender++
			default:
				onlyZeroSenderEndpoint++
			}
		}
		if want == ReasonGranted && r.Reply && r.TargetDeclaration.RestrictSenders && r.Action == ActionMessageSend {
			grantedByReply++
		}
	}
	for _, reason := range []Reason{ReasonGranted, ReasonMalformedRequest, ReasonEndpointPaused,
		ReasonEndpointRefusesMultiRecipient, ReasonSenderNotAccepted, ReasonEndpointHidden} {
		if reached[reason] < 1000 {
			t.Errorf("reason %v reached %d times, want at least 1000", reason, reached[reason])
		}
	}
	if len(reached) != 6 {
		t.Errorf("reasons reached: %v", reached)
	}
	if stages[StageIntake] < 1000 || stages[StageRecv] < 1000 {
		t.Errorf("well-formed requests per stage: %v", stages)
	}
	if onlySystemSender < 100 || onlyZeroSenderEndpoint < 100 {
		t.Errorf("refused only for the sender's endpoint: system endpoint %d, zero endpoint %d, want at least 100 each",
			onlySystemSender, onlyZeroSenderEndpoint)
	}
	if grantedByReply < 100 {
		t.Errorf("a reply lifted the sender list only %d times", grantedByReply)
	}
}

// Over an open node policy (every member may do everything, nothing capped) a
// remote request and a local one with the same facts get the same verdict: the
// shape check and the declarations are one piece of code, not two. The one
// fact on which they part is the sender's endpoint: a local source is always an
// endpoint, so a request without one is refused only by EvaluateLocal.
func TestEvaluateLocalAgreesWithEvaluateUnderAnOpenPolicy(t *testing.T) {
	rng := rand.New(rand.NewSource(47))
	open := DefaultPolicy()
	compared, shapeRejects, declined, noEndpoint := 0, 0, 0, 0
	for i := 0; i < 50000; i++ {
		l := randomLocalRequest(rng)
		if l.Sender.Endpoint.IsZero() {
			// Every local source is an endpoint, which Evaluate does not require of
			// a sender: this is the one request fact on which the two part.
			if got := EvaluateLocal(l); got.Reason != ReasonMalformedRequest {
				t.Fatalf("EvaluateLocal %v for a request with no sender endpoint: %+v", got.Reason, l)
			}
			noEndpoint++
			continue
		}
		r := Request{
			Now: t0, Trust: allowed, Stage: l.Stage, Sender: l.Sender, Action: l.Action, Target: l.Target,
			Preset: "p", MultiRecipient: l.MultiRecipient, Reply: l.Reply, TargetDeclaration: l.TargetDeclaration,
		}
		want, got := Evaluate(open, r), EvaluateLocal(l)
		compared++
		if want.Outcome != got.Outcome || want.Reason != got.Reason {
			t.Fatalf("Evaluate %v/%v, EvaluateLocal %v/%v for %+v", want.Outcome, want.Reason, got.Outcome, got.Reason, l)
		}
		switch got.Reason {
		case ReasonMalformedRequest:
			shapeRejects++
		case ReasonGranted:
		default:
			declined++
		}
	}
	if compared == 0 || shapeRejects < 1000 || declined < 1000 || noEndpoint < 100 {
		t.Errorf("compared %d, shape rejects %d, declined %d, without sender endpoint %d", compared, shapeRejects, declined, noEndpoint)
	}
}
