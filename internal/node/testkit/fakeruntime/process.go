package fakeruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// RunProcess is the body of a runtime process: it hosts one session, makes it
// reachable on a loopback address that it writes to the info file beside the
// record, serves the adapter that drives the session and the fixture that scripts
// its turns, with the test clock the fixture sets. It returns when ctx ends, when
// the control connection ends, or when the native process is killed. ready is
// called once the session exists and the address is on disk.
func RunProcess(ctx context.Context, spec ProcessSpec, runner ActionRunner, ready func(ProcessInfo)) error {
	if err := spec.validate(); err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	clock := fakectl.NewClock(spec.ClockStartMS)
	files := ledger.NewDir(spec.LedgerDir)
	rt, err := New(Config{Now: clock.Now, Ledgers: files, Dir: spec.Dir, Seed: spec.Seed, Runner: runner, PID: os.Getpid(), FirstTurn: spec.FirstTurn.Plan()})
	if err != nil {
		return err
	}
	defer rt.Close()
	created, err := rt.Create(ctx, spec.Create)
	if err != nil {
		return err
	}
	sess := created.(*session)

	srv, err := fakectl.Listen(ctx)
	if err != nil {
		return err
	}
	defer srv.Close()
	info := ProcessInfo{Ref: sess.ref, Endpoint: sess.endpoint, Addr: srv.Addr(), PID: int64(os.Getpid()), StartKey: sess.startKey, CreatedSeq: sess.createdSeq}
	p := &processHost{rt: rt, sess: sess, srv: srv, ctx: ctx, stop: stop, info: info}
	// The session is not watched yet: its first turn starts when the adapter that
	// made it says it is in charge (MethodWatch), so that the turn's events have an
	// adapter to go to.
	p.serve()

	control, err := fakectl.Dial(ctx, spec.Control)
	if err != nil {
		return err
	}
	defer control.Close()
	control.HandleClock(clock.SetMS)
	control.HandleStep(files.SetStep)
	control.HandleKnob(p.knob)
	if err := control.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleRuntime, ID: sess.ref, PID: int64(os.Getpid())}); err != nil {
		return err
	}

	if err := writeInfo(spec.Dir, info); err != nil {
		return err
	}
	if ready != nil {
		ready(info)
	}
	select {
	case <-ctx.Done():
	case <-control.Done():
	}
	return nil
}

// writeInfo writes the info file in one atomic step, so that a reader never sees
// half of it.
func writeInfo(dir string, info ProcessInfo) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, info.Ref+".json.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, info.Ref+".json"))
}

// processHost serves one session to the adapter and the fixture.
type processHost struct {
	rt   *Runtime
	sess *session
	srv  *fakectl.Server
	ctx  context.Context
	stop context.CancelFunc
	info ProcessInfo
}

func linkError(err error) error {
	switch {
	case errors.Is(err, ErrBusy):
		return peer.NewError(CodeBusy, "a turn is running")
	case errors.Is(err, ErrNotAlive):
		return peer.NewError(CodeNotAlive, "the session is not alive")
	case errors.Is(err, ErrNoTurn):
		return peer.NewError(CodeNoTurn, "no turn is running")
	}
	return err
}

func (h *processHost) serve() {
	handle := func(method string, f func(ctx context.Context, params json.RawMessage) (any, error)) {
		h.srv.Handle(method, func(ctx context.Context, _ *fakectl.Conn, params json.RawMessage) (any, error) {
			res, err := f(ctx, params)
			return res, linkError(err)
		})
	}
	invalid := func(err error) error { return peer.NewError(peer.CodeInvalidParams, err.Error()) }
	handle(MethodDeliver, func(ctx context.Context, params json.RawMessage) (any, error) {
		v, err := DecodeLink(MethodDeliver, params)
		if err != nil {
			return nil, invalid(err)
		}
		d := v.(DeliverParams)
		return struct{}{}, h.sess.Deliver(ctx, d.Attempt, d.Text)
	})
	handle(MethodInterrupt, func(ctx context.Context, _ json.RawMessage) (any, error) {
		res, err := h.sess.Interrupt(ctx)
		if err != nil {
			return nil, err
		}
		if res == InterruptNotRunning {
			return InterruptReply{Result: "not_running"}, nil
		}
		return InterruptReply{Result: "stopped"}, nil
	})
	handle(MethodClose, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return struct{}{}, h.sess.Close(ctx)
	})
	handle(MethodReconcile, func(_ context.Context, params json.RawMessage) (any, error) {
		v, err := DecodeLink(MethodReconcile, params)
		if err != nil {
			return nil, invalid(err)
		}
		rec, err := h.sess.Reconcile(v.(ReconcileParams).Attempt)
		return ReconcileReply{Found: rec.Found, Turn: rec.Turn, TurnState: rec.TurnState, TurnRunning: rec.TurnRunning, WriterStopped: rec.WriterStopped}, err
	})
	handle(MethodState, func(context.Context, json.RawMessage) (any, error) {
		turn, active := h.sess.TurnActive()
		return StateReply{Ref: h.info.Ref, PID: h.info.PID, StartKey: h.info.StartKey, Alive: h.sess.Alive(), Active: active, Turn: turn}, nil
	})
	handle(MethodWatch, func(context.Context, json.RawMessage) (any, error) {
		h.sess.Watch(h) // the first call starts the first turn; later ones change nothing
		return struct{}{}, nil
	})
}

// knob serves the fixture's turns of the session.
func (h *processHost) knob(_ context.Context, name string, args json.RawMessage) (any, error) {
	invalid := func(err error) error { return peer.NewError(peer.CodeInvalidParams, err.Error()) }
	switch name {
	case KnobPlan:
		k, err := DecodePlanKnob(KnobPlan, args)
		if err != nil {
			return nil, invalid(err)
		}
		return nil, linkError(h.rt.SetPlan(h.sess.ref, k.Plan()))
	case KnobTurnStart:
		return nil, linkError(h.rt.StartTurn(h.sess.ref))
	case KnobTurnEnd:
		v, err := DecodeLink("knob."+KnobTurnEnd, args)
		if err != nil {
			return nil, invalid(err)
		}
		return nil, linkError(h.rt.EndTurn(h.sess.ref, v.(TurnEndKnob).Final))
	case KnobKill:
		if err := h.rt.KillNative(h.sess.ref); err != nil {
			return nil, linkError(err)
		}
		// A killed native process is gone: the process ends, and the connection
		// with it. The caller treats that as the answer.
		h.stop()
		return nil, nil
	case KnobIdle:
		return IdleReply{Idle: h.rt.Idle()}, nil
	case KnobSessionEnv:
		env, err := h.rt.SessionEnv(h.sess.ref)
		if err != nil {
			return nil, linkError(err)
		}
		if env == nil {
			env = map[string]string{}
		}
		return SessionEnvReply{Ref: h.sess.ref, Env: env}, nil
	}
	return nil, peer.NewError(peer.CodeMethodNotFound, "no such knob")
}

// The watcher of the session makes each event a request of every adapter
// connected, one adapter and one event at a time, and returns when the adapter has
// answered: it answers once it has handed the event to the daemon. The runtime is
// busy until then, so a fixture that asks whether it is idle does not see a gap
// between the runtime having finished and the daemon having been told. An adapter
// that is gone cannot answer, and there is nobody to tell.
func (h *processHost) emit(method string, params any) {
	for _, c := range h.srv.Conns() {
		if c.Hello().Role == fakectl.RoleAdapter {
			_ = c.Call(h.ctx, method, params, nil)
		}
	}
}

func (h *processHost) TurnStarted(e TurnStartedEvent) {
	h.emit(MethodTurnStarted, TurnStartedParams{Turn: e.Turn, Attempt: e.Attempt, AtMS: e.AtMS})
}

func (h *processHost) TurnEnded(e TurnEndedEvent) {
	h.emit(MethodTurnEnded, TurnEndedParams{Turn: e.Turn, Final: e.Final, AtMS: e.AtMS})
}

func (h *processHost) ToolResult(e ToolResultEvent) {
	h.emit(MethodToolResult, ToolResultParams{Lease: e.Lease, Text: e.Text, Frontend: e.Frontend})
}
