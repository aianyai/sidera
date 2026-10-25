package adapterhost_test

import (
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// An error of the engine in a method that is not the answer to a request means the
// connection cannot be used: it is ended, reported by what it was, and the instance
// starts again after the backoff.
func TestAnErrorOfTheEngineEndsTheConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		want adapterhost.GoneCause
	}{
		{"the engine cannot take the adapter on", "admitted", adapterhost.GoneStartFailed},
		{"the engine cannot take adapter.start", "started", adapterhost.GoneStartFailed},
		{"the engine cannot issue the probe tokens", "probeTokens", adapterhost.GoneConnectionEnded},
		{"the engine cannot take the probe", "probed", adapterhost.GoneConnectionEnded},
		{"the engine cannot take a notification", "notified", adapterhost.GoneConnectionEnded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			r.eng.errs[tc.kind] = errors.New("the engine is not able")
			var a *scripted
			r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
				pp, ad := r.scriptedProcess(nil, false)
				a = ad
				return pp, nil
			}
			r.start()
			if tc.kind == "notified" {
				r.ready("chat", 1)
				a.notify(adapterproto.MethodTurnStarted, turnStarted("s-1", 1))
			}
			r.waitEvents("gone", 1)
			if got := r.goneCauses("chat"); got[0] != tc.want {
				t.Fatalf("reported %v, want %v", got[0], tc.want)
			}
			r.waitDisconnected("chat")
			// The backoff timer exists once the host is idle again.
			r.settle()
			r.advance(testParams().RestartBackoffMin)
			r.waitLaunches(2)
		})
	}
}

// Once the instance is being stopped, what the adapter asks is answered as the
// host shutting down.
func TestRequestsAfterShutdownBeganAreAnsweredAsShuttingDown(t *testing.T) {
	r := newRig(t, rigOptions{})
	var pp *pipeProcess
	var a *scripted
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, a = r.scriptedProcess(nil, true)
		return pp, nil
	}
	r.start()
	r.ready("chat", 1)
	r.host.Stop("chat")
	var res json.RawMessage
	err := a.call(adapterproto.MethodEndpointRegister, adapterproto.RegisterParams{Key: "k", DisplayName: "d"}, &res)
	var pe *peer.Error
	// The stream may be closed already: then there is no answer to read. Whichever
	// answer is read is -32002, and the engine is never asked.
	if errors.As(err, &pe) && pe.Code != peer.CodeShuttingDown {
		t.Fatalf("answered %v, want -32002", err)
	}
	if got := r.eng.of("register"); len(got) != 0 {
		t.Fatalf("the engine was asked %d times after shutdown began", len(got))
	}
	pp.release()
	eventually(t, "the supervision to end", func() bool { return !slices.Contains(r.eng.kinds(), "register") && zeroCounts(r.host.Counts("chat")) })
}

// Every call of the handshake is limited by HandshakeTimeout on the host's clock: a
// call that is not answered in time ends the pass, kills the process and is
// reported.
func TestAHandshakeCallThatIsNotAnsweredEndsThePassAtTheHandshakeTimeout(t *testing.T) {
	r := newRig(t, rigOptions{})
	var ppv atomic.Pointer[pipeProcess]
	var av atomic.Pointer[scripted]
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, a := r.scriptedProcess(func(a *scripted) {
			a.initialize = func(adapterproto.InitializeParams) (any, error) { <-hold; return nil, errors.New("too late") }
		}, false)
		ppv.Store(pp)
		av.Store(a)
		return pp, nil
	}
	started := make(chan error, 1)
	go func() { started <- r.host.Start(timeoutCtx(t), r.env(false)) }()
	eventually(t, "initialize to reach the adapter", func() bool { a := av.Load(); return a != nil && a.count(adapterproto.MethodInitialize) == 1 })
	if c := r.host.Counts("chat"); len(c.Calls) != 1 || c.Calls[0] != adapterproto.MethodInitialize {
		t.Fatalf("Calls = %v while initialize is out, want [initialize]", c.Calls)
	}
	limit := testParams().HandshakeTimeout
	r.advance(limit - 1)
	if got := r.eng.of("gone"); len(got) != 0 || ppv.Load().killed() != 0 {
		t.Fatalf("the pass ended a nanosecond before the limit: %d reports, %d kills", len(got), ppv.Load().killed())
	}
	r.advance(1)
	r.waitEvents("gone", 1)
	if got := r.goneCauses("chat"); !slices.Equal(got, []adapterhost.GoneCause{adapterhost.GoneStartFailed}) {
		t.Fatalf("reported %v, want [start_failed]", got)
	}
	eventually(t, "the process to be killed", func() bool { return ppv.Load().killed() >= 1 })
	if err := <-started; err != nil {
		t.Fatalf("Start = %v", err)
	}
}
