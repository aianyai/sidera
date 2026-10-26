package engine

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The probe of an instance: the adapter says which of its tool probes ran, and the
// node believes it only for the tokens it has seen presented. A probe token is the
// proof that a front end, started as the adapter's runtime would start it, reached
// this node; the adapter's report that it ran the tool is necessary and not enough.

// ProbeState is how far the probe of an instance has come.
type ProbeState uint8

// The states of a probe. The zero value is not a state.
const (
	// ProbePending: the adapter has not reported, or a token the node issued has
	// not been presented yet and has not expired.
	ProbePending ProbeState = iota + 1
	// ProbePassed: the adapter reported that each tool probe ran, and the node saw
	// each token presented. An adapter with no tool probe to run passes with its
	// report.
	ProbePassed
	// ProbeFailed: the adapter reported a tool probe that did not run, or a token
	// expired before it was presented.
	ProbeFailed
)

// String names the state as the owner's commands show it.
func (s ProbeState) String() string {
	switch s {
	case ProbePending:
		return "pending"
	case ProbePassed:
		return "passed"
	case ProbeFailed:
		return "failed"
	}
	return "invalid"
}

// ProbeReached is a front end of a preset whose token the node saw presented.
type ProbeReached struct {
	Preset   string
	Frontend adapterproto.Frontend
}

// ProbeView is the probe of one instance.
type ProbeView struct {
	State ProbeState
	// At is when the adapter reported the probe; zero while it has not.
	At time.Time
	// Reached are the front ends whose token was presented, in the order the
	// tokens were issued.
	Reached []ProbeReached
}

// ProbeTarget is what a presented probe token was issued for.
type ProbeTarget struct {
	Instance string
	Preset   string
	Frontend adapterproto.Frontend
}

type toolProbe struct {
	preset   string
	frontend adapterproto.Frontend
}

// probeView says how far the probe has come at now. A token that was not
// presented is pending until it expires and failed after; an adapter that reported
// a tool probe as anything but ran, or did not report one the node issued a token
// for, has failed it.
func (st *adapterState) probeView(now time.Time) ProbeView {
	v := ProbeView{State: ProbePending, At: st.probedAt}
	for _, t := range st.tokens {
		if t.used {
			v.Reached = append(v.Reached, ProbeReached{Preset: t.preset, Frontend: t.frontend})
		}
	}
	if !st.probed {
		return v
	}
	ran := map[toolProbe]bool{}
	for _, tp := range st.probe.ToolProbes {
		ran[toolProbe{tp.Preset, tp.Frontend}] = tp.Outcome == adapterproto.ProbeRan
	}
	failed, pending := false, false
	for _, t := range st.tokens {
		switch {
		case !ran[toolProbe{t.preset, t.frontend}]:
			failed = true
		case !t.used && now.Before(t.expires):
			pending = true
		case !t.used:
			failed = true
		}
	}
	switch {
	case failed:
		v.State = ProbeFailed
	case !pending:
		v.State = ProbePassed
	}
	return v
}

// declareIfPassed makes the runtime endpoint of the instance part of what the
// node declares once its probe has passed. It is not undone when the instance
// later becomes unusable: the declaration does not flap.
func (e *Engine) declareIfPassed(instance string, st *adapterState, now time.Time) {
	if st.declared || !st.admitted || !st.adm.Declares(adapterreg.KindRuntime) || st.probeView(now).State != ProbePassed {
		return
	}
	st.declared = true
	e.log.Info("the runtime endpoint of the instance is declared: its probe has passed", "instance", instance)
}

type cmdPresentProbeToken struct {
	call[ProbeTarget]
	token    []byte
	frontend adapterproto.Frontend
}

// A probe token is presented before the node serves: the Agent interface calls this
// in the mode that lets nothing else through.
func (c *cmdPresentProbeToken) gate() gate { return gateAlways }
func (c *cmdPresentProbeToken) run(e *Engine, now time.Time) {
	target, err := e.presentProbeToken(now, c.token, c.frontend)
	c.done(target, e.classify(err))
}

// PresentProbeToken is how a front end proves it reaches this node: the token the
// node issued for an (instance, preset, front end) is presented with the front end
// that presents it. A token that no instance holds is not authenticated; one that
// was presented before, or is older than ProbeTokenLifetime, is refused for that; a
// front end other than the token's is refused and the token stays good, so a
// misconfigured front end does not use up the probe. A token that is accepted is
// used up, and the probe of its instance may pass. The token is compared against
// every token held, in constant time, and is never logged.
func (e *Engine) PresentProbeToken(ctx context.Context, token adapterproto.Token, frontend adapterproto.Frontend) (ProbeTarget, error) {
	// The format of a token is the protocol's, and only a text in that form is one:
	// the decoder alone would skip a newline and accept spare bits in the last
	// character, so that a text that is not the token would be taken for it.
	if token.Validate() != nil {
		return ProbeTarget{}, refuse(ReasonUnauthenticated)
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(token))
	if err != nil {
		return ProbeTarget{}, refuse(ReasonUnauthenticated)
	}
	c := &cmdPresentProbeToken{call: newCall[ProbeTarget](), token: raw, frontend: frontend}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

func (e *Engine) presentProbeToken(now time.Time, raw []byte, frontend adapterproto.Frontend) (ProbeTarget, error) {
	var (
		held     *probeToken
		instance string
		st       *adapterState
	)
	for _, name := range sortedKeys(e.adapters) {
		s := e.adapters[name]
		for i := range s.tokens {
			// every token is compared: where the match is must not show in the time
			if subtle.ConstantTimeCompare(s.tokens[i].token, raw) == 1 {
				held, instance, st = &s.tokens[i], name, s
			}
		}
	}
	switch {
	case held == nil:
		return ProbeTarget{}, refuse(ReasonUnauthenticated)
	case held.used:
		return ProbeTarget{}, refuse(ReasonProbeTokenUsed)
	case !now.Before(held.expires):
		return ProbeTarget{}, refuse(ReasonProbeTokenExpired)
	case held.frontend != frontend:
		return ProbeTarget{}, &Refusal{Reason: ReasonInvalidArgument, Field: "frontend", Detail: DetailProbeFrontendMismatch}
	}
	held.used = true
	e.log.Info("a probe token was presented", "instance", instance, "preset", held.preset, "frontend", string(held.frontend))
	e.declareIfPassed(instance, st, now)
	return ProbeTarget{Instance: instance, Preset: held.preset, Frontend: held.frontend}, nil
}
