package adapterhost

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// pass is one go at an adapter: launch it, take it on, keep it in service until
// it is lost, and end what is left of it. It returns an error only when the
// instance is being stopped or the host ended; every other outcome has been
// reported and is followed by a backoff. stable says whether the connection stayed
// in service for Params.StableAfter.
func (s *supervisor) pass() (stable bool, err error) {
	c, err := s.connect()
	if err != nil {
		if isEnd(err) {
			s.release()
			return false, err
		}
		s.log.Warn("the adapter was not taken on", "err", err)
		s.report(GoneStartFailed)
		s.release()
		return false, nil
	}
	return s.serve(c)
}

// instance builds what the launcher is told to start, from the configuration as
// it is now: an adapter installed or upgraded since the last pass is the one
// launched.
func (s *supervisor) instance() (Instance, error) {
	env := s.h.env
	in, ok := env.Source.Instances().Items[s.id]
	if !ok {
		return Instance{}, fmt.Errorf("the instance is not in the configuration")
	}
	for _, a := range env.Source.Adapters() {
		if a.ID == in.Adapter {
			return Instance{
				ID:       s.id,
				Adapter:  a,
				Dir:      filepath.Join(env.Home, filepath.FromSlash(config.AdapterVersionDir(a.ID, a.Version))),
				LockPath: LockPath(env.Home, s.id),
			}, nil
		}
	}
	return Instance{}, fmt.Errorf("the adapter %q is not installed", in.Adapter)
}

// connect launches the adapter and takes it on: initialize, admission,
// adapter.start. On success the connection is in service. On failure the process
// has been ended.
func (s *supervisor) connect() (*conn, error) {
	h := s.h
	inst, err := s.instance()
	if err != nil {
		return nil, err
	}
	logw := s.openLog()
	inst.Stderr = logw
	proc, err := h.launcher.Launch(h.ctx, inst)
	if err != nil {
		logw.Close()
		return nil, fmt.Errorf("launch: %w", err)
	}
	c := s.newConn(proc, logw)
	stream, stdin := proc.Stream()
	c.p, err = peer.New(stream, stdin, peer.Config{Params: h.params.Peer, Logger: s.log})
	if err != nil {
		s.finishConn(c, modeKill)
		return nil, err
	}
	c.register()
	c.p.Start()
	if err := s.handshake(c, inst); err != nil {
		s.finishConn(c, modeOf(err))
		return nil, err
	}
	return c, nil
}

// handshake is steps 3 to 5 of taking an adapter on.
func (s *supervisor) handshake(c *conn, inst Instance) error {
	h := s.h
	eng := h.env.Engine
	limit := h.params.HandshakeTimeout
	sent := adapterproto.InitializeParams{
		Protocol:     adapterproto.SupportedRange(),
		Daemon:       adapterproto.DaemonInfo{Version: h.version},
		Instance:     adapterproto.InstanceInfo{ID: s.id, LockPath: inst.LockPath},
		Capabilities: adapterproto.DaemonCapabilities{},
	}
	var init adapterproto.InitializeResult
	if err := s.rpc(c, adapterproto.MethodInitialize, sent, &init, limit); err != nil {
		var pe *peer.Error
		if errors.As(err, &pe) && pe.Code == adapterproto.CodeInstanceLocked {
			s.log.Warn("another process holds the instance lock; nobody is admitted", "lock_path", inst.LockPath)
		}
		return fmt.Errorf("initialize: %w", err)
	}
	m := inst.Adapter.Manifest
	if err := adapterreg.CheckHandshake(sent.Protocol, init, m); err != nil {
		return err
	}
	adm, err := adapterreg.Admit(init.Capabilities, m)
	if err != nil {
		return err
	}
	var start adapterproto.StartParams
	err = s.engine(func(ctx context.Context) (err error) {
		start, err = eng.AdapterAdmitted(ctx, s.id, adm, init)
		return err
	})
	if err != nil {
		return fmt.Errorf("the engine could not take the adapter on: %w", err)
	}
	// Only now are the adapter's requests let through, and what it said before
	// reaches the engine.
	c.admit(adm)
	var started adapterproto.StartResult
	if err := s.rpc(c, adapterproto.MethodAdapterStart, start, &started, limit); err != nil {
		return fmt.Errorf("adapter.start: %w", err)
	}
	err = s.engine(func(ctx context.Context) error { return eng.AdapterStarted(ctx, s.id, started) })
	if err != nil {
		return fmt.Errorf("the engine could not take adapter.start: %w", err)
	}
	s.mu.Lock()
	if !s.stopping {
		s.service = c
	}
	s.mu.Unlock()
	s.release()
	return nil
}

// probe is step 6. A probe the adapter fails is logged and nothing more: whether
// the adapter is usable is a fact to show the owner, and the pings judge whether
// it is alive. A failure of the engine is returned: it cannot use the connection.
func (s *supervisor) probe(c *conn) error {
	eng := s.h.env.Engine
	var reqs []adapterproto.ToolProbeRequest
	err := s.engine(func(ctx context.Context) (err error) {
		reqs, err = eng.ProbeTokens(ctx, s.id)
		return err
	})
	if err != nil {
		return err
	}
	if reqs == nil {
		reqs = []adapterproto.ToolProbeRequest{}
	}
	var res adapterproto.ProbeResult
	switch err := s.rpc(c, adapterproto.MethodAdapterProbe, adapterproto.ProbeParams{ToolProbes: reqs}, &res, s.h.params.HandshakeTimeout); {
	case isEnd(err):
		return err
	case err != nil:
		s.log.Warn("the probe of the adapter failed", "err", err)
		return nil
	}
	return s.engine(func(ctx context.Context) error { return eng.AdapterProbed(ctx, s.id, res) })
}

// How a connection in service came to its end.
type outcome uint8

const (
	// outPings: OfflineAfterPingFailures pings in a row went unanswered.
	outPings outcome = iota + 1
	// outOver: the stream ended or the process exited.
	outOver
	// outEnd: the instance is being stopped or the host ended; err says which.
	outEnd
)

// serve keeps a connection in service and, when it is lost, reports and ends it.
func (s *supervisor) serve(c *conn) (stable bool, err error) {
	stable, out, err := s.watch(c)
	switch out {
	case outPings:
		s.leaveService(c)
		// The forwarding ends before the engine is told: a notification that
		// reached the engine after the report would find the instance offline.
		// What is still buffered is dropped, and the process is ended after the
		// report, so that the report does not wait for it.
		s.closeConn(c)
		s.report(GonePingsFailed)
		s.finishConn(c, modeKill)
		return stable, nil
	case outOver:
		s.leaveService(c)
		// What the adapter said before the end reaches the engine first.
		if sl, _ := s.park(waits{over: c.fwdDone}, false); sl != slotOver {
			err = endOf(sl)
			s.finishConn(c, modeOf(err))
			return stable, err
		}
		s.report(GoneConnectionEnded)
		s.finishConn(c, modeKill)
		return stable, nil
	}
	s.finishConn(c, modeOf(err))
	return stable, err
}

// watch probes the adapter and then pings it until it is lost. Every timer it
// made is stopped when it returns, so that ending the connection waits on no
// timer of the pings.
func (s *supervisor) watch(c *conn) (stable bool, out outcome, err error) {
	p := s.h.params
	stableT := s.newTimer(p.StableAfter)
	stableC := stableT.C()
	var (
		intervalT  clock.Timer
		intervalC  <-chan time.Time
		limitT     clock.Timer
		limitC     <-chan time.Time
		pingRes    chan error
		pingCancel context.CancelFunc
		failures   int
	)
	defer func() {
		// A StableAfter that came due just as the connection ended still counts.
		if stableC != nil {
			select {
			case <-stableC:
				stable = true
			default:
			}
		}
		s.stopTimer(stableT)
		s.stopTimer(intervalT)
		s.stopTimer(limitT)
		if pingCancel != nil {
			pingCancel()
		}
	}()

	if perr := s.probe(c); perr != nil {
		if isEnd(perr) {
			return false, outEnd, perr
		}
		c.fail(perr)
	}
	armInterval := func() {
		intervalT = s.newTimer(p.PingInterval)
		intervalC = intervalT.C()
	}
	armInterval()

	for {
		sl, perr := s.park(waits{res: pingRes, limit: limitC, tick: intervalC, stable: stableC, over: c.p.Done()}, true)
		switch sl {
		case slotStable:
			stable, stableC = true, nil
		case slotTick:
			s.stopTimer(intervalT)
			intervalT, intervalC = nil, nil
			// The limit is armed before the ping is sent: a caller that has seen
			// the adapter receive the ping may move the clock past it.
			limitT = s.newTimer(p.PingTimeout)
			limitC = limitT.C()
			ctx, cancel := context.WithCancel(context.WithoutCancel(s.h.ctx))
			pingCancel = cancel
			pingRes = make(chan error, 1)
			res := pingRes
			s.spawn(func() { res <- c.call(ctx, adapterproto.MethodPing, adapterproto.Empty{}, &adapterproto.Empty{}) })
		case slotRes, slotLimit:
			// The ping is over: answered, failed, or too slow.
			if sl == slotLimit {
				pingCancel()
				s.wait(waits{res: pingRes})
				perr = errLimit
			}
			pingCancel()
			s.stopTimer(limitT)
			limitT, limitC, pingRes, pingCancel = nil, nil, nil, nil
			select {
			case <-c.p.Done():
				// The connection ended while the ping was out: that is the cause.
				continue
			default:
			}
			if perr == nil {
				failures = 0
				armInterval()
				continue
			}
			failures++
			s.log.Warn("a ping of the adapter failed", "failures", failures, "err", perr)
			if failures < OfflineAfterPingFailures {
				armInterval()
				continue
			}
			return stable, outPings, nil
		case slotOver:
			return stable, outOver, nil
		default:
			if sl == slotAbort {
				// Nothing is sent on abort: ending the ping's context would send
				// the adapter a cancellation.
				c.abortNow()
			}
			return stable, outEnd, endOf(sl)
		}
	}
}

func endOf(sl slot) error {
	if sl == slotStop {
		return errStopped
	}
	return errAborted
}

func (s *supervisor) leaveService(c *conn) {
	s.mu.Lock()
	if s.service == c {
		s.service = nil
	}
	s.mu.Unlock()
}

// How a connection is ended.
type mode uint8

const (
	// modeKill: the adapter is lost, wrong or unusable: kill it.
	modeKill mode = iota
	// modeShutdown: ask it to shut down, and kill it only if it does not end.
	modeShutdown
	// modeAbort: kill it now and send it nothing.
	modeAbort
)

func modeOf(err error) mode {
	switch {
	case errors.Is(err, errAborted):
		return modeAbort
	case errors.Is(err, errStopped):
		return modeShutdown
	}
	return modeKill
}

// finishConn ends a connection and everything of it. A process that Kill
// reports it cannot end is left at once, and one that was asked to end and does
// not is waited for at most Params.ShutdownWait and then left: the instance lock
// keeps a second writer out whatever either does.
func (s *supervisor) finishConn(c *conn, m mode) {
	s.leaveService(c)
	c.closing.Store(true)
	defer c.logw.Close()
	switch m {
	case modeAbort:
		_ = c.proc.Kill()
		s.closeConn(c)
	case modeShutdown:
		s.draining = true
		if c.admission.Load() != nil {
			var none adapterproto.Empty
			err := s.rpc(c, adapterproto.MethodShutdown, adapterproto.Empty{}, &none, s.h.params.HandshakeTimeout)
			if errors.Is(err, errAborted) {
				_ = c.proc.Kill()
				s.closeConn(c)
				return
			}
		}
		s.closeConn(c)
		switch err := s.waitExit(c); {
		case errors.Is(err, errAborted):
			_ = c.proc.Kill()
		case errors.Is(err, errLimit):
			_ = c.proc.Kill()
			_ = s.waitExit(c)
		}
	default:
		killErr := c.proc.Kill()
		s.closeConn(c)
		if killErr != nil {
			// A process that cannot be told to end is not waited for: there is nothing
			// the wait could see. The instance lock keeps a second writer out.
			s.log.Warn("the adapter process cannot be ended; it is left", "pid", c.proc.PID(), "err", killErr)
			return
		}
		_ = s.waitExit(c)
	}
}

// closeConn ends the goroutines of a connection and drops what is still buffered
// for the engine. The supervisor waits for the forwarder in a park: the forwarder
// may be in a call of the engine, and the engine may be asking this supervisor for
// its counts.
func (s *supervisor) closeConn(c *conn) {
	c.end()
	s.wait(waits{over: c.fwdDone})
	c.dropBuffered()
}

// waitExit waits for the process to end for at most Params.ShutdownWait.
func (s *supervisor) waitExit(c *conn) error {
	t := s.newTimer(s.h.params.ShutdownWait)
	defer s.stopTimer(t)
	switch sl, _ := s.park(waits{over: c.exited, limit: t.C()}, false); sl {
	case slotOver:
		return nil
	case slotLimit:
		return errLimit
	}
	return errAborted
}

// openLog opens the log the adapter's standard error goes to. A log that cannot
// be opened is a warning and not a reason to refuse the adapter: what it writes
// is then discarded.
func (s *supervisor) openLog() *logWriter {
	name := "adapter-" + s.id + ".log"
	l, err := s.h.env.Logs.Open(name)
	if err != nil {
		s.log.Warn("the log of the adapter cannot be opened; what it writes is discarded", "log", name, "err", err)
		return &logWriter{}
	}
	return &logWriter{dst: l}
}
