package adapterhost_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// unendable is a process that Kill reports it cannot end, and that does not end: the
// operating system refusing to signal it, or one stuck beyond a signal's reach.
type unendable struct{ *pipeProcess }

func (p unendable) Kill() error { _ = p.pipeProcess.Kill(); return errors.New("not permitted") }

// A process that Kill reports it cannot end is not waited for: there is nothing the
// wait could see, and a caller that steps the clock and waits for the host to be
// idle before each step must find it idle once the host has armed its backoff. A
// process that was asked to end and does not is a different case: it is waited for
// at most ShutdownWait, and the host counts as working while it ends, so that a
// process which ends in moments is not taken for one that was left.
func TestAProcessThatCannotBeEndedIsNotWaitedFor(t *testing.T) {
	p := testParams()
	r := newRig(t, rigOptions{})
	var adapters []*scripted
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, a := r.scriptedProcess(func(a *scripted) {
			if n == 1 {
				a.ping = func(ctx context.Context, _ int) error { <-ctx.Done(); return ctx.Err() }
			}
		}, n == 1)
		adapters = append(adapters, a)
		if n == 1 {
			return unendable{pp}, nil
		}
		return pp, nil
	}
	r.start()
	r.ready("chat", 1)
	for ping := 1; ping <= adapterhost.OfflineAfterPingFailures; ping++ {
		r.advance(p.PingInterval)
		waitPings(t, adapters[0], ping)
		r.advance(p.PingTimeout)
	}
	if got := r.goneCauses("chat"); !slices.Equal(got, []adapterhost.GoneCause{adapterhost.GonePingsFailed}) {
		t.Fatalf("the engine was told %v, want [pings_failed]", got)
	}
	// Idle with the backoff armed, and no wait for the process in front of it.
	r.settle()
	r.advance(p.RestartBackoffMin - time.Nanosecond)
	if n := r.lau.launches(); n != 1 {
		t.Fatalf("%d launches a nanosecond before the backoff ended, want 1", n)
	}
	r.advance(time.Nanosecond)
	r.waitLaunches(2)
}

// A request the host has sent and not had answered is in Calls from the moment the
// host waits for the answer: a caller that sees the host idle must find the
// request there, or it takes the host for quiet while the adapter is still
// working on it. Sampled in a tight loop from the launch on, in many passes,
// because a request that was counted a moment late would show only now and then.
func TestARequestIsCountedAsSentBeforeTheHostIsIdleWaitingForItsAnswer(t *testing.T) {
	for pass := range 40 {
		t.Run(fmt.Sprintf("pass %d", pass), func(t *testing.T) {
			r := newRig(t, rigOptions{})
			hold := make(chan struct{})
			r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
				pp, _ := r.scriptedProcess(func(ad *scripted) {
					ad.initialize = func(adapterproto.InitializeParams) (any, error) {
						<-hold
						return ad.initializeResult(), nil
					}
				}, false)
				return pp, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = r.host.Start(ctx, r.env(false)) }()
			for r.lau.launches() < 1 {
			}
			sawRequest := false
			for range 20000 {
				c := r.host.Counts("chat")
				sawRequest = sawRequest || len(c.Calls) > 0
				if !c.Working && len(c.Calls) == 0 {
					close(hold)
					t.Fatalf("the host was idle with no request out, while initialize was sent and not answered: %+v", c)
				}
			}
			close(hold)
			if !sawRequest {
				t.Fatal("initialize was never in Calls while the adapter held its answer")
			}
			r.waitConnected("chat")
		})
	}
}
