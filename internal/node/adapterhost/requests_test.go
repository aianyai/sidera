package adapterhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// callCode returns the code of the error a request of the adapter was answered
// with, 0 when it succeeded.
func callCode(t *testing.T, a *scripted, method string, params any, res any) int {
	t.Helper()
	err := a.call(method, params, res)
	if err == nil {
		return 0
	}
	var pe *peer.Error
	if !errors.As(err, &pe) {
		t.Fatalf("%s: %v is not an answer of the host", method, err)
	}
	return pe.Code
}

func connectedScripted(t *testing.T, r *rig) *scripted {
	t.Helper()
	var a *scripted
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, ad := r.scriptedProcess(nil, false)
		a = ad
		return pp, nil
	}
	r.start()
	r.ready("chat", 1)
	return a
}

// The requests of the adapter reach the engine, and what the engine answers reaches
// the adapter: a *peer.Error as it is, any other error as an internal error.
func TestRequestsOfTheAdapterReachTheEngineAndItsErrorsReachTheAdapter(t *testing.T) {
	t.Run("the engine's answers", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		a := connectedScripted(t, r)

		var reg adapterproto.RegisterResult
		if code := callCode(t, a, adapterproto.MethodEndpointRegister, adapterproto.RegisterParams{Key: "room", DisplayName: "A room"}, &reg); code != 0 || reg.Endpoint != "ep-room" {
			t.Fatalf("endpoint.register = %+v (code %d)", reg, code)
		}
		if got := r.eng.of("register"); len(got) != 1 || got[0].Instance != "chat" || got[0].Params.(adapterproto.RegisterParams).Key != "room" {
			t.Fatalf("the engine saw %+v", got)
		}
		var none adapterproto.Empty
		if code := callCode(t, a, adapterproto.MethodEndpointUnregister, adapterproto.UnregisterParams{Endpoint: "ep-room"}, &none); code != 0 {
			t.Fatalf("endpoint.unregister: code %d", code)
		}
		if got := r.eng.of("unregister"); len(got) != 1 || got[0].Params.(adapterproto.UnregisterParams).Endpoint != "ep-room" {
			t.Fatalf("the engine saw %+v", got)
		}
		state := json.RawMessage(`{"phase":"created"}`)
		if code := callCode(t, a, adapterproto.MethodCheckpointPut, adapterproto.CheckpointPutParams{Scope: adapterproto.ScopeSession, ID: "s-1", State: state}, &none); code != 0 {
			t.Fatalf("checkpoint.put: code %d", code)
		}
		if got := r.eng.of("checkpoint"); len(got) != 1 || string(got[0].Params.(adapterproto.CheckpointPutParams).State) != string(state) {
			t.Fatalf("the engine saw %+v", got)
		}
	})

	t.Run("errors", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.eng.onRegister = func(p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
			switch p.Key {
			case "limit":
				return adapterproto.RegisterResult{}, peer.NewError(adapterproto.CodeEndpointLimit, "the limit is reached")
			case "wrapped":
				return adapterproto.RegisterResult{}, fmt.Errorf("while registering: %w", peer.NewError(adapterproto.CodeUnknownEndpoint, "no such endpoint"))
			case "secret":
				return adapterproto.RegisterResult{}, errors.New("the password is hunter2")
			case "invalid":
				// A result the protocol forbids is never sent.
				return adapterproto.RegisterResult{Endpoint: ""}, nil
			}
			return adapterproto.RegisterResult{Endpoint: "ep"}, nil
		}
		a := connectedScripted(t, r)

		for _, tc := range []struct {
			name, key string
			want      int
		}{
			{"a *peer.Error is answered as it is", "limit", adapterproto.CodeEndpointLimit},
			{"a wrapped *peer.Error is answered as it is", "wrapped", adapterproto.CodeUnknownEndpoint},
			{"any other error is an internal error", "secret", peer.CodeInternalError},
			{"a result that is not a protocol result is an internal error", "invalid", peer.CodeInternalError},
		} {
			var res json.RawMessage
			err := a.call(adapterproto.MethodEndpointRegister, adapterproto.RegisterParams{Key: tc.key, DisplayName: "d"}, &res)
			var pe *peer.Error
			if !errors.As(err, &pe) || pe.Code != tc.want {
				t.Errorf("%s: answered %v, want code %d", tc.name, err, tc.want)
				continue
			}
			if strings.Contains(pe.Message, "hunter2") || strings.Contains(pe.Message, "password") {
				t.Errorf("%s: the text of the engine's error reached the adapter: %q", tc.name, pe.Message)
			}
		}

		// Parameters that do not decode are -32602, whatever follows.
		for _, tc := range []struct {
			name   string
			method string
			params any
			want   int
		}{
			{"an unknown member", adapterproto.MethodEndpointRegister, map[string]any{"key": "k", "display_name": "d", "bogus": 1}, peer.CodeInvalidParams},
			{"a missing member", adapterproto.MethodEndpointRegister, map[string]any{"key": "k"}, peer.CodeInvalidParams},
			{"a state that is not an object", adapterproto.MethodCheckpointPut, map[string]any{"scope": "session", "id": "s-1", "state": []int{1}}, peer.CodeInvalidParams},
			{"a state beyond its limit", adapterproto.MethodCheckpointPut, map[string]any{"scope": "session", "id": "s-1", "state": map[string]string{"blob": strings.Repeat("x", adapterproto.MaxCheckpointBytes+1)}}, adapterproto.CodeCheckpointTooLarge},
		} {
			before := len(r.eng.snapshot())
			var res json.RawMessage
			if code := callCode(t, a, tc.method, tc.params, &res); code != tc.want {
				t.Errorf("%s: code %d, want %d", tc.name, code, tc.want)
			}
			if got := len(r.eng.snapshot()); got != before {
				t.Errorf("%s: the engine was called for parameters that do not decode", tc.name)
			}
		}
	})

	// The requests that need the optional parts of the engine.
	endpointCalls := func(a *scripted) map[string]int {
		codes := map[string]int{}
		var res json.RawMessage
		codes["send"] = callCode(t, a, adapterproto.MethodEndpointSend, adapterproto.SendParams{Endpoint: "ep-1", To: []adapterproto.Address{"bob/chat"}, ContentType: "text/plain", Body: "hi"}, &res)
		codes["recv"] = callCode(t, a, adapterproto.MethodEndpointRecv, adapterproto.RecvParams{Endpoint: "ep-1"}, &res)
		codes["ack"] = callCode(t, a, adapterproto.MethodEndpointAck, adapterproto.AckParams{Endpoint: "ep-1", Lease: "0123456789abcdef0123456789abcdef", Items: []adapterproto.PartRef{{MessageID: "m-2", Part: 1}}}, &res)
		codes["status"] = callCode(t, a, adapterproto.MethodEndpointMessageStatus, adapterproto.MessageStatusParams{Endpoint: "ep-1", MessageID: "m-1"}, &res)
		codes["observed"] = callCode(t, a, adapterproto.MethodRecvObserved, adapterproto.RecvObservedParams{Endpoint: "s-1", Lease: "0123456789abcdef0123456789abcdef", Frontend: adapterproto.FrontendCLI, Observed: "text"}, &res)
		return codes
	}
	t.Run("endpoint calls and observed calls without an engine that offers them are unknown methods", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		var a *scripted
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, ad := r.scriptedProcess(nil, false)
			a = ad
			return pp, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
		defer cancel()
		if err := r.host.Start(ctx, r.env(true)); err != nil {
			t.Fatal(err)
		}
		r.ready("chat", 1)
		for name, code := range endpointCalls(a) {
			if code != peer.CodeMethodNotFound {
				t.Errorf("%s: code %d, want -32601", name, code)
			}
		}
		for _, kind := range []string{"send", "recv", "ack", "status", "observed"} {
			if got := r.eng.of(kind); len(got) != 0 {
				t.Errorf("%s reached an engine that does not offer it", kind)
			}
		}
	})
	t.Run("with an engine that offers them they are forwarded", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		a := connectedScripted(t, r)
		for name, code := range endpointCalls(a) {
			// The double's observed answer is a refusal of the acknowledgement,
			// which is a result and not an error.
			if code != 0 {
				t.Errorf("%s: code %d, want a result", name, code)
			}
		}
		for _, kind := range []string{"send", "recv", "ack", "status", "observed"} {
			if got := r.eng.of(kind); len(got) != 1 || got[0].Instance != "chat" {
				t.Errorf("%s: the engine saw %+v", kind, got)
			}
		}
		var sent adapterproto.SendResult
		if err := a.call(adapterproto.MethodEndpointSend, adapterproto.SendParams{Endpoint: "ep-1", To: []adapterproto.Address{"bob/chat"}, ContentType: "text/plain", Body: "hi"}, &sent); err != nil || sent.MessageID != "m-1" {
			t.Errorf("endpoint.send = %+v, %v: the result of the engine did not reach the adapter", sent, err)
		}
	})
}

// The calls the engine makes on the adapter are forwarded, their parameters and
// results are validated, and a call to an adapter that is not in service sends
// nothing.
func TestCallsToTheAdapterAreForwardedValidatedAndRefusedWhenItIsNotInService(t *testing.T) {
	attempt1 := adapterproto.AttemptID("00000000-0000-4000-8000-000000000001")
	attempt2 := adapterproto.AttemptID("00000000-0000-4000-8000-000000000002")
	create := adapterproto.SessionCreateParams{
		Attempt: attempt1, RuntimeEndpoint: "rt-1", SessionEndpoint: "s-1", Preset: "default",
		FirstPrompt: adapterproto.FirstPrompt{Attempt: attempt2, Text: "hello"},
		ToolAccess:  adapterproto.ToolAccess{Env: map[string]string{}},
	}

	t.Run("not in service: nothing is sent", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		k := r.knobsOf("chat")
		if err := k.PauseAt(fakeadapter.PauseStartBeforeReply); err != nil {
			t.Fatal(err)
		}
		started := make(chan error, 1)
		go func() { started <- r.host.Start(timeoutCtx(t), r.env(false)) }()
		if err := k.WaitPaused(timeoutCtx(t), fakeadapter.PauseStartBeforeReply); err != nil {
			t.Fatal(err)
		}
		// The handshake is not over: adapter.start has not been answered.
		before := r.tl.written()
		if _, err := r.host.CreateSession(timeoutCtx(t), "chat", create); !errors.Is(err, adapterhost.ErrNotConnected) {
			t.Errorf("CreateSession before adapter.start was answered: %v, want ErrNotConnected", err)
		}
		if _, err := r.host.CreateSession(timeoutCtx(t), "nobody", create); !errors.Is(err, adapterhost.ErrNotConnected) {
			t.Errorf("CreateSession for an instance that is not supervised: %v, want ErrNotConnected", err)
		}
		r.host.NotifyPending("chat", adapterproto.PendingParams{Endpoint: "ep-1", Count: 1})
		if after := r.tl.written(); !slices.Equal(before, after) {
			t.Errorf("a call to an adapter that is not in service wrote %v", after[len(before):])
		}
		k.Release(fakeadapter.PauseStartBeforeReply)
		if err := <-started; err != nil {
			t.Fatal(err)
		}
		r.ready("chat", 1)
	})

	t.Run("the five calls and the notification", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.start()
		r.ready("chat", 1)
		ctx := timeoutCtx(t)
		res, err := r.host.CreateSession(ctx, "chat", create)
		if err != nil || res.Outcome != adapterproto.CreateCreated {
			t.Fatalf("CreateSession = %+v, %v", res, err)
		}
		pd, err := r.host.DeliverPrompt(ctx, "chat", adapterproto.PromptDeliverParams{Endpoint: "s-1", Attempt: "00000000-0000-4000-8000-000000000003", Text: "more"})
		if err != nil || pd.Validate() != nil || pd.Stage == adapterproto.PromptRefused {
			t.Fatalf("DeliverPrompt = %+v, %v", pd, err)
		}
		rc, err := r.host.Reconcile(ctx, "chat", adapterproto.ReconcileParams{Endpoint: "s-1", Attempt: attempt2})
		if err != nil || rc.Verdict != adapterproto.VerdictFound {
			t.Fatalf("Reconcile = %+v, %v", rc, err)
		}
		it, err := r.host.Interrupt(ctx, "chat", adapterproto.InterruptParams{Endpoint: "s-1"})
		if err != nil || it.Validate() != nil {
			t.Fatalf("Interrupt = %+v, %v", it, err)
		}
		cl, err := r.host.Close(ctx, "chat", adapterproto.CloseParams{Endpoint: "s-1"})
		if err != nil || cl.Outcome != adapterproto.CloseClosed {
			t.Fatalf("Close = %+v, %v", cl, err)
		}
		r.host.NotifyPending("chat", adapterproto.PendingParams{Endpoint: "ep-1", Count: 2})
		want := []string{
			adapterproto.MethodInitialize, adapterproto.MethodAdapterStart, adapterproto.MethodAdapterProbe,
			adapterproto.MethodSessionCreate, adapterproto.MethodPromptDeliver, adapterproto.MethodPromptReconcile,
			adapterproto.MethodSessionInterrupt, adapterproto.MethodSessionClose, adapterproto.MethodEndpointPending,
		}
		eventually(t, "the notification to be written", func() bool { return slices.Equal(r.tl.written(), want) })
	})

	t.Run("parameters that are not valid are refused and not sent", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.start()
		r.ready("chat", 1)
		before := r.tl.written()
		bad := create
		bad.Preset = "Not A Preset"
		if _, err := r.host.CreateSession(timeoutCtx(t), "chat", bad); err == nil {
			t.Error("CreateSession with an invalid preset succeeded")
		} else {
			var pe *peer.Error
			if errors.As(err, &pe) {
				t.Errorf("an invalid parameter was answered by the adapter: %v", err)
			}
		}
		if _, err := r.host.DeliverPrompt(timeoutCtx(t), "chat", adapterproto.PromptDeliverParams{Endpoint: "s-1", Attempt: attempt1, Text: ""}); err == nil {
			t.Error("DeliverPrompt with an empty text succeeded")
		}
		r.host.NotifyPending("chat", adapterproto.PendingParams{Endpoint: "ep-1", Count: 0})
		if after := r.tl.written(); !slices.Equal(before, after) {
			t.Errorf("invalid parameters were sent: %v", after[len(before):])
		}
	})

	t.Run("an error of the adapter is returned as it is, and a result that is not valid is an unknown outcome", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, _ := r.scriptedProcess(func(a *scripted) {
				a.handlers[adapterproto.MethodPromptDeliver] = func(_ context.Context, raw json.RawMessage) (any, error) {
					var p adapterproto.PromptDeliverParams
					_ = json.Unmarshal(raw, &p)
					if p.Text == "conflict" {
						return nil, peer.NewError(adapterproto.CodeAttemptConflict, "other parameters")
					}
					return map[string]any{"stage": "nonsense"}, nil
				}
			}, false)
			return pp, nil
		}
		r.start()
		r.ready("chat", 1)
		_, err := r.host.DeliverPrompt(timeoutCtx(t), "chat", adapterproto.PromptDeliverParams{Endpoint: "s-1", Attempt: attempt1, Text: "conflict"})
		var pe *peer.Error
		if !errors.As(err, &pe) || pe.Code != adapterproto.CodeAttemptConflict || !peer.NoEffect(err) {
			t.Errorf("an error of the adapter came back as %v, want its own *peer.Error", err)
		}
		_, err = r.host.DeliverPrompt(timeoutCtx(t), "chat", adapterproto.PromptDeliverParams{Endpoint: "s-1", Attempt: attempt2, Text: "other"})
		if err == nil {
			t.Fatal("a result that is not valid was accepted")
		}
		if errors.As(err, &pe) || !peer.ResultUnknown(err) {
			t.Errorf("a result that is not valid came back as %v, want an error that is not the adapter's and whose outcome is unknown", err)
		}
	})

	t.Run("not in service after the connection ended and after Shutdown", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.start()
		r.ready("chat", 1)
		if err := r.lau.proc(0).Kill(); err != nil {
			t.Fatal(err)
		}
		r.waitDisconnected("chat")
		before := r.tl.written()
		if _, err := r.host.Close(timeoutCtx(t), "chat", adapterproto.CloseParams{Endpoint: "s-1"}); !errors.Is(err, adapterhost.ErrNotConnected) {
			t.Errorf("a call after the connection ended: %v, want ErrNotConnected", err)
		}
		if after := r.tl.written(); !slices.Equal(before, after) {
			t.Errorf("a call after the connection ended wrote %v", after[len(before):])
		}
		// A new connection, then Shutdown.
		r.advance(time.Second)
		r.waitLaunches(2)
		r.ready("chat", 2)
		if err := r.host.Shutdown(timeoutCtx(t)); err != nil {
			t.Fatal(err)
		}
		if _, err := r.host.Close(timeoutCtx(t), "chat", adapterproto.CloseParams{Endpoint: "s-1"}); !errors.Is(err, adapterhost.ErrNotConnected) {
			t.Errorf("a call after Shutdown: %v, want ErrNotConnected", err)
		}
	})
}

// Counts: what the engine finished with, what waits, the requests being answered,
// the requests sent that have no answer, and whether the host is working.
func TestCountsFollowNotificationsRequestsCallsAndTheTimersOfTheHost(t *testing.T) {
	t.Run("notifications", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.eng.notifyGate = make(chan struct{})
		a := connectedScripted(t, r)
		for i := range 3 {
			a.notify(adapterproto.MethodTurnStarted, turnStarted("s-1", i))
		}
		eventually(t, "three notifications to be buffered", func() bool { return r.host.Counts("chat").Buffered == 3 })
		if c := r.host.Counts("chat"); c.Handled != 0 {
			t.Fatalf("counts = %+v, want nothing handled while the engine holds the first", c)
		}
		close(r.eng.notifyGate)
		eventually(t, "three notifications to be handled", func() bool {
			c := r.host.Counts("chat")
			return c.Handled == 3 && c.Buffered == 0
		})
	})

	t.Run("requests of the adapter, and a ping is not one", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.eng.registerGate = make(chan struct{})
		a := connectedScripted(t, r)
		done := make(chan error, 1)
		go func() {
			var res adapterproto.RegisterResult
			done <- a.call(adapterproto.MethodEndpointRegister, adapterproto.RegisterParams{Key: "k", DisplayName: "d"}, &res)
		}()
		eventually(t, "the request to be being answered", func() bool { return r.host.Counts("chat").Requests == 1 })
		var pong adapterproto.Empty
		if err := a.call(adapterproto.MethodPing, adapterproto.Empty{}, &pong); err != nil {
			t.Fatal(err)
		}
		if c := r.host.Counts("chat"); c.Requests != 1 {
			t.Fatalf("Requests = %d after a ping, want 1", c.Requests)
		}
		close(r.eng.registerGate)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		eventually(t, "no request to be being answered", func() bool { return r.host.Counts("chat").Requests == 0 })
	})

	t.Run("calls with no answer, as a multiset, with the fake's held requests", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.start()
		r.ready("chat", 1)
		k := r.knobsOf("chat")
		ctx := timeoutCtx(t)
		if _, err := r.host.CreateSession(ctx, "chat", adapterproto.SessionCreateParams{
			Attempt: "00000000-0000-4000-8000-000000000001", RuntimeEndpoint: "rt-1", SessionEndpoint: "s-1", Preset: "default",
			FirstPrompt: adapterproto.FirstPrompt{Attempt: "00000000-0000-4000-8000-000000000002", Text: "hello"},
			ToolAccess:  adapterproto.ToolAccess{Env: map[string]string{}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := k.SetPrompt(fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold}); err != nil {
			t.Fatal(err)
		}
		if err := k.SetClose(fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold}); err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 3)
		for i := range 2 {
			go func() {
				_, err := r.host.DeliverPrompt(ctx, "chat", adapterproto.PromptDeliverParams{Endpoint: "s-1", Attempt: adapterproto.AttemptID(fmt.Sprintf("00000000-0000-4000-8000-00000000001%d", i)), Text: "more"})
				results <- err
			}()
		}
		go func() {
			_, err := r.host.Close(ctx, "chat", adapterproto.CloseParams{Endpoint: "s-1"})
			results <- err
		}()
		eventually(t, "three requests to be held", func() bool { return len(k.Held()) == 3 })
		want := []string{adapterproto.MethodPromptDeliver, adapterproto.MethodPromptDeliver, adapterproto.MethodSessionClose}
		held := k.Held()
		slices.Sort(held)
		if got := r.host.Counts("chat").Calls; !slices.Equal(got, held) || !slices.Equal(got, want) {
			t.Fatalf("Calls = %v, held by the adapter = %v, want %v", got, held, want)
		}
		k.ReleaseHeld()
		for range 3 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		eventually(t, "no call to be without an answer", func() bool { return len(r.host.Counts("chat").Calls) == 0 })
	})

	t.Run("a ping that has no answer yet is not among the calls", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		var a *scripted
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, ad := r.scriptedProcess(func(ad *scripted) {
				ad.ping = func(ctx context.Context, _ int) error { <-ctx.Done(); return ctx.Err() }
			}, false)
			a = ad
			return pp, nil
		}
		r.start()
		r.ready("chat", 1)
		r.advance(testParams().PingInterval)
		waitPings(t, a, 1)
		if c := r.host.Counts("chat"); len(c.Calls) != 0 {
			t.Fatalf("Calls = %v while only a ping is out, want none", c.Calls)
		}
	})

	// After the clock moved, a caller that sees Working false knows the host acted on
	// everything the move made due: here, the ping that fell due is out and its time
	// limit is armed. Run many times, with the adapter lost each time, because a host
	// that answered false while the move was still waiting would show only now and then.
	t.Run("Working is false only once the host acted on what the clock made due", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		var scripts []*scripted
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, ad := r.scriptedProcess(func(ad *scripted) {
				ad.ping = func(ctx context.Context, _ int) error { <-ctx.Done(); return ctx.Err() }
			}, false)
			scripts = append(scripts, ad)
			return pp, nil
		}
		r.start()
		p := testParams()
		for i := 1; i <= 40; i++ {
			r.ready("chat", i)
			if r.host.Counts("chat").Working {
				t.Fatalf("pass %d: Working is true for a host parked with nothing due", i)
			}
			// Timers of the host in service: the stable timer and the ping interval.
			if n := r.clk.Timers(); n != 2 {
				t.Fatalf("pass %d: %d timers armed in service, want 2", i, n)
			}
			r.clk.Advance(p.PingInterval)
			eventually(t, "the host to be idle", func() bool { return !r.host.Counts("chat").Working })
			// The ping is out: the interval timer is gone and the ping's limit is
			// armed, besides the stable timer.
			if n := r.clk.Timers(); n != 2 {
				t.Fatalf("pass %d: %d timers armed once Working was false, want 2 (stable, ping limit): the host had not acted on the move", i, n)
			}
			// Lose the adapter, and wait for the next pass.
			if err := r.lau.proc(i - 1).Kill(); err != nil {
				t.Fatal(err)
			}
			r.waitDisconnected("chat")
			eventually(t, "the end to be reported", func() bool { return len(r.goneCauses("chat")) == i })
			r.settle()
			r.clk.Advance(p.RestartBackoffMax)
			r.waitLaunches(i + 1)
		}
	})
}
