package adapterhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// declaring makes a scripted adapter declare, or not, that it reads the intent of
// shutdown.
func declaring(r *rig, declared bool) func(*scripted) {
	return func(a *scripted) {
		caps := r.opts.Capabilities()
		caps.ShutdownIntent = declared
		a.caps = &caps
	}
}

var shutdownIntents = []adapterproto.ShutdownIntent{adapterproto.ShutdownRestart, adapterproto.ShutdownStop, adapterproto.ShutdownRetire}

// The intent goes to an adapter that declared shutdown_intent, by either way the
// host ends an adapter, and an adapter that did not is sent the request it always
// got, so that one that decodes shutdown strictly never sees a member it does not
// know.
func TestShutdownCarriesTheIntentOnlyToAnAdapterThatDeclaredIt(t *testing.T) {
	for _, via := range []string{"Stop", "Shutdown"} {
		for _, intent := range shutdownIntents {
			for _, declared := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s %s declared=%v", via, intent, declared), func(t *testing.T) {
					r := newRig(t, rigOptions{})
					var a *scripted
					r.lau.launch = func(int, adapterhost.Instance) (adapterhost.Process, error) {
						pp, s := r.scriptedProcess(declaring(r, declared), false)
						a = s
						return pp, nil
					}
					r.start()
					r.ready("chat", 1)
					if via == "Stop" {
						r.host.Stop("chat", intent)
					} else if err := r.host.Shutdown(timeoutCtx(t), intent); err != nil {
						t.Fatal(err)
					}
					eventually(t, "shutdown to reach the adapter", func() bool { return a.count(adapterproto.MethodShutdown) == 1 })
					want := `{}`
					if declared {
						want = fmt.Sprintf(`{"intent":%q}`, intent)
					}
					if got := string(a.paramsOf(adapterproto.MethodShutdown)[0]); got != want {
						t.Errorf("shutdown carried %s, want %s", got, want)
					}
				})
			}
		}
	}
}

// The first request's intent stands: the end of the host over an instance that is
// being retired does not turn the retiring into a stop.
func TestTheFirstIntentOfAStopStands(t *testing.T) {
	r := newRig(t, rigOptions{})
	var a *scripted
	r.lau.launch = func(int, adapterhost.Instance) (adapterhost.Process, error) {
		pp, s := r.scriptedProcess(declaring(r, true), false)
		a = s
		return pp, nil
	}
	r.start()
	r.ready("chat", 1)
	r.host.Stop("chat", adapterproto.ShutdownRetire)
	if err := r.host.Shutdown(timeoutCtx(t), adapterproto.ShutdownStop); err != nil {
		t.Fatal(err)
	}
	if got := string(a.paramsOf(adapterproto.MethodShutdown)[0]); got != `{"intent":"retire"}` {
		t.Errorf("shutdown carried %s, want the intent of the first stop", got)
	}
}

// An error the adapter answers shutdown with is logged and nothing more: the
// adapter ends, and no one is asked again.
func TestAnErrorOfShutdownIsLoggedAndTheAdapterEnds(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.lau.launch = func(int, adapterhost.Instance) (adapterhost.Process, error) {
		pp, _ := r.scriptedProcess(func(a *scripted) {
			declaring(r, true)(a)
			a.handlers[adapterproto.MethodShutdown] = func(context.Context, json.RawMessage) (any, error) {
				return nil, peer.NewError(peer.CodeInternalError, "the server 4242 was left")
			}
		}, false)
		return pp, nil
	}
	r.start()
	r.ready("chat", 1)
	r.host.Stop("chat", adapterproto.ShutdownRetire)
	eventually(t, "the instance to have stopped", func() bool { return !r.host.Stopping("chat") })
	if !r.records.contains("the adapter did not take shutdown") || !r.records.contains("the server 4242 was left") {
		t.Errorf("the failure of shutdown was not logged: %v", r.records.msgs)
	}
	if n := r.lau.launches(); n != 1 {
		t.Errorf("%d launches, want the one adapter and no round", n)
	}
}

// An instance retired while no adapter is in service has nobody to be told, so the
// host starts the adapter once, for initialize and shutdown only. The instance is
// stopping until the round has ended. Retiring an instance whose adapter is in
// service, and stopping or restarting one that is not, start no round.
func TestRetiringAnInstanceWithNoAdapterInServiceRunsAReleaseRound(t *testing.T) {
	type tc struct {
		name      string
		intent    adapterproto.ShutdownIntent
		inBackoff bool // the first start failed: the instance waits to try again
		declared  bool
		rounds    int
	}
	cases := []tc{
		{"retired while backing off", adapterproto.ShutdownRetire, true, true, 1},
		{"retired while backing off, adapter without the intent", adapterproto.ShutdownRetire, true, false, 1},
		{"stopped while backing off", adapterproto.ShutdownStop, true, true, 0},
		{"restarted while backing off", adapterproto.ShutdownRestart, true, true, 0},
		{"retired in service", adapterproto.ShutdownRetire, false, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			inRound := make(chan struct{})
			holdRound := make(chan struct{})
			var round *scripted
			r.lau.launch = func(n int, _ adapterhost.Instance) (adapterhost.Process, error) {
				if n == 1 && c.inBackoff {
					return nil, errors.New("the program cannot be started")
				}
				pp, s := r.scriptedProcess(func(a *scripted) {
					declaring(r, c.declared)(a)
					if n == 2 || !c.inBackoff {
						a.handlers[adapterproto.MethodShutdown] = func(context.Context, json.RawMessage) (any, error) {
							if c.rounds > 0 {
								close(inRound)
								<-holdRound
							}
							return adapterproto.Empty{}, nil
						}
					}
				}, false)
				round = s
				return pp, nil
			}
			r.start()
			if c.inBackoff {
				r.waitLaunches(1)
				r.settle()
			} else {
				r.ready("chat", 1)
			}
			r.host.Stop("chat", c.intent)

			if c.rounds > 0 {
				<-inRound
				if !r.host.Stopping("chat") {
					t.Error("the instance is not stopping while the release round runs")
				}
				close(holdRound)
			}
			eventually(t, "the instance to have stopped", func() bool { return !r.host.Stopping("chat") })

			wantLaunches := 1 + c.rounds
			if !c.inBackoff {
				wantLaunches = 1
			}
			if n := r.lau.launches(); n != wantLaunches {
				t.Fatalf("%d launches, want %d", n, wantLaunches)
			}
			if c.rounds == 0 {
				return
			}
			if got, want := round.received(), []string{adapterproto.MethodInitialize, adapterproto.MethodShutdown}; !slices.Equal(got, want) {
				t.Errorf("the round sent the adapter %v, want %v", got, want)
			}
			want := `{}`
			if c.declared {
				want = `{"intent":"retire"}`
			}
			if got := string(round.paramsOf(adapterproto.MethodShutdown)[0]); got != want {
				t.Errorf("the round's shutdown carried %s, want %s", got, want)
			}
			if got := r.eng.of("admitted"); len(got) != 0 {
				t.Errorf("the engine was told of %d admissions for a round", len(got))
			}
		})
	}
}

// A release round that cannot start the adapter is logged and not tried again; the
// instance stops being stopping.
func TestAReleaseRoundThatFailsIsLoggedAndNotRepeated(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.lau.launch = func(int, adapterhost.Instance) (adapterhost.Process, error) {
		return nil, errors.New("the program cannot be started")
	}
	r.start()
	r.waitLaunches(1)
	r.settle()
	r.host.Stop("chat", adapterproto.ShutdownRetire)
	eventually(t, "the instance to have stopped", func() bool { return !r.host.Stopping("chat") })
	if n := r.lau.launches(); n != 2 {
		t.Errorf("%d launches, want the failed start and one round", n)
	}
	r.advance(10 * testParams().RestartBackoffMax)
	if n := r.lau.launches(); n != 2 {
		t.Errorf("the round was repeated: %d launches", n)
	}
	if !r.records.contains("could not be asked to release") {
		t.Errorf("the failure of the round was not logged: %v", r.records.msgs)
	}
}

// An instance retired while it waits for the adapter before it to end (it was
// restarted just before) is released once that adapter has ended: the round needs
// the instance lock the old process holds.
func TestAReleaseRoundWaitsForTheAdapterBeforeIt(t *testing.T) {
	r := newRig(t, rigOptions{})
	var first *pipeProcess
	var second *scripted
	r.lau.launch = func(n int, _ adapterhost.Instance) (adapterhost.Process, error) {
		pp, s := r.scriptedProcess(declaring(r, true), n == 1)
		if n == 1 {
			first = pp
		} else {
			second = s
		}
		return pp, nil
	}
	r.start()
	r.waitConnected("chat")
	r.settle()
	r.host.Stop("chat", adapterproto.ShutdownRestart)
	r.host.EnsureRunning("chat")
	r.host.Stop("chat", adapterproto.ShutdownRetire)
	<-first.serveDone
	eventually(t, "the shutdown wait to be armed", func() bool { return r.clk.Timers() >= 1 })
	r.settle()
	if n := r.lau.launches(); n != 1 {
		t.Fatalf("the round started while the old adapter was ending: %d launches", n)
	}
	r.clk.Advance(testParams().ShutdownWait)
	eventually(t, "the old adapter to be killed", func() bool { return first.killed() == 1 })
	first.release()
	eventually(t, "the instance to have stopped", func() bool { return !r.host.Stopping("chat") })
	if n := r.lau.launches(); n != 2 {
		t.Fatalf("%d launches, want the adapter and one round", n)
	}
	if got := string(second.paramsOf(adapterproto.MethodShutdown)[0]); got != `{"intent":"retire"}` {
		t.Errorf("the round's shutdown carried %s", got)
	}
}
