package adapterhost_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// While the host waits for a process to end after it asked it to (Kill, or the end of
// its stream), it counts as working: the wait is "ending a process", and a caller
// that moved the clock and finds the host idle takes it that the host has gone on
// to arm its next timer. That holds for a process that ends within moments. A
// process that cannot be ended is waited for until the clock ends the wait, so a
// caller that steps the clock and waits for the host to be idle before each step
// waits for ever at the step that would end the wait.
func TestShouldTheHostBeIdleWhileItWaitsForAProcessItCannotEnd(t *testing.T) {
	t.Skip("open question, no change of the host's counts is made without a decision: how Counts.Working " +
		"treats the wait for a process that was asked to end and has not. Idle for the whole wait breaks " +
		"every caller that reads idle as 'the host went on' for a process that ends in moments (the host's own " +
		"requests_test.go loses launches that way); working for the whole wait leaves a stuck process " +
		"(an unkillable hung adapter holding the instance lock) to be waited for by a caller that cannot step " +
		"the clock until the host is idle. Options: working for a short wait on the real clock and idle after it; " +
		"a separate count of processes being ended that a quiet check can tell apart; or leave it and let the " +
		"fixture of a hung process end the wait itself")
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
