package authz

import (
	"math/rand"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
)

func randomDeclaration(rng *rand.Rand) Declaration {
	d := Declaration{
		Paused:               rng.Intn(4) == 0,
		RefuseMultiRecipient: rng.Intn(3) == 0,
		RestrictSenders:      rng.Intn(3) == 0,
		HiddenFromDirectory:  rng.Intn(3) == 0,
	}
	for _, n := range poolNodes {
		for _, e := range poolEndpoints {
			if rng.Intn(3) == 0 {
				d.AcceptFrom = append(d.AcceptFrom, ident.Address{Node: n, Endpoint: e})
			}
		}
	}
	return d
}

// Denied trust is never an allow or a suspension: it is refused before the
// rules are read, whatever the policy and the request.
func TestDeniedTrustAlwaysRejects(t *testing.T) {
	grid := requestGrid()
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 200; i++ {
		p := randomPolicy(t, rng)
		for k := 0; k < 50; k++ {
			r := randomRequest(rng, grid)
			r.Trust = ConnectionTrust{State: TrustDenied}
			if got := Evaluate(p, r); got.Outcome != OutcomeReject || got.Reason != ReasonTrustDenied {
				t.Fatalf("denied trust gave %v/%v for %+v", got.Outcome, got.Reason, r)
			}
		}
	}
}

// randomRequest picks a grid request and randomizes what the grid leaves fixed:
// the check, the multi-recipient flag and the target's declaration.
func randomRequest(rng *rand.Rand, grid []Request) Request {
	r := grid[rng.Intn(len(grid))]
	r.Stage = pick(rng, []Stage{StageIntake, StageRecv})
	r.MultiRecipient = rng.Intn(2) == 0
	r.TargetDeclaration = randomDeclaration(rng)
	return r
}

// Suspended trust only turns an allow into a suspension. The request gets
// Suspend exactly when the same request from an inherited member would be
// allowed; otherwise both are refused for the same reason. A suspended sender
// is always inherited, whatever Direct says.
func TestSuspendedTrustSuspendsExactlyWhatWouldBeAllowed(t *testing.T) {
	grid := requestGrid()
	rng := rand.New(rand.NewSource(8))
	suspends := 0
	for i := 0; i < 200; i++ {
		p := randomPolicy(t, rng)
		for k := 0; k < 100; k++ {
			r := randomRequest(rng, grid)
			r.Trust = ConnectionTrust{State: TrustSuspended, Direct: rng.Intn(2) == 0}
			got := Evaluate(p, r)
			r.Trust = ConnectionTrust{State: TrustAllowed, Direct: false}
			plain := Evaluate(p, r)
			if plain.Outcome == OutcomeAllow {
				suspends++
				if got.Outcome != OutcomeSuspend || got.Reason != ReasonTrustSuspended || len(got.Obligations) != 0 {
					t.Fatalf("suspended gave %+v where an inherited member is allowed (%v): %+v", got, plain.Reason, r)
				}
			} else if got.Reason != plain.Reason {
				t.Fatalf("suspended gave %v where an inherited member gets %v: %+v", got.Reason, plain.Reason, r)
			}
		}
	}
	if suspends == 0 {
		t.Error("no request was ever suspended; the property test is not exercising it")
	}
}

// Endpoint self-declarations only narrow: any request they let through is also
// let through by the same policy with no declaration.
func TestDeclarationsOnlyNarrow(t *testing.T) {
	grid := requestGrid()
	rng := rand.New(rand.NewSource(11))
	rejected := 0
	for i := 0; i < 200; i++ {
		p := randomPolicy(t, rng)
		for k := 0; k < 100; k++ {
			r := randomRequest(rng, grid)
			declared := Evaluate(p, r)
			r.TargetDeclaration = Declaration{}
			plain := Evaluate(p, r)
			if declared.Outcome == OutcomeAllow && plain.Outcome != OutcomeAllow {
				t.Fatalf("a declaration widened %v to allow for %+v", plain.Reason, r)
			}
			if declared.Outcome == OutcomeAllow && plain.Reason != declared.Reason {
				t.Fatalf("a declaration changed an allow from %v to %v", plain.Reason, declared.Reason)
			}
			if plain.Outcome == OutcomeAllow && declared.Outcome != OutcomeAllow {
				rejected++
			}
		}
	}
	if rejected == 0 {
		t.Error("declarations never narrowed anything; the property test is not exercising them")
	}
}

// A reply never overrides an explicit deny, nor a paused declaration at intake.
func TestReplyNeverOverridesDenyOrPause(t *testing.T) {
	grid := requestGrid()
	rng := rand.New(rand.NewSource(13))
	for i := 0; i < 200; i++ {
		p := randomPolicy(t, rng)
		for k := 0; k < 100; k++ {
			r := grid[rng.Intn(len(grid))]
			if r.Action != ActionMessageSend {
				continue
			}
			r.Reply = false
			noReply := Evaluate(p, r)
			r.Reply = true
			withReply := Evaluate(p, r)
			if noReply.Reason == ReasonExplicitDeny && withReply.Reason != ReasonExplicitDeny {
				t.Fatalf("reply overrode a deny: %+v", r)
			}
			r.TargetDeclaration = Declaration{Paused: true}
			if got := Evaluate(p, r); got.Outcome == OutcomeAllow {
				t.Fatalf("reply overrode a paused declaration: %+v", r)
			}
		}
	}
}
