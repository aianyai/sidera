package daemon

import (
	"context"
	"fmt"
	"time"
)

// The longest the interfaces and the adapters have to stop in, on the real clock.
// In an orderly shutdown the requests being served have this long to end, and the
// adapters this long to stop before they are killed; after a fatal failure of the
// disk nothing more is written and there is no reason to wait as long.
const (
	shutdownGrace = 10 * time.Second
	fatalGrace    = 2 * time.Second
)

// supervise waits for the end of the node: a request to stop, or the end of the
// engine, which a fatal failure of the disk causes. It then runs the shutdown and
// settles how the daemon ended.
func (d *Daemon) supervise() {
	fatal := false
	select {
	case <-d.stopCh:
	case <-d.engine.Done():
		fatal = d.engine.Err() != nil
	}
	d.shutdown(fatal)
}

// shutdown takes the steps of the shutdown: the engine refuses what comes from
// outside; the interfaces stop within a grace; the adapters stop, the native
// sessions being separate processes that are not affected; the engine ends its
// loop; and the lock is let go, the log closed last. It is the one path of an
// orderly stop, a fatal one, and the unwinding of a start that failed, so the
// steps that have nothing to stop are logged and do nothing. It runs once.
func (d *Daemon) shutdown(fatal bool) {
	grace := shutdownGrace
	if fatal {
		grace = fatalGrace
	}
	if d.stopReason != "" {
		d.log.Info("shutdown requested", "reason", d.stopReason)
	}
	step := func(n int, name string, fn func(context.Context)) {
		d.log.Info(fmt.Sprintf("shutdown step %d begin", n), "name", name)
		ctx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		fn(ctx)
		d.log.Info(fmt.Sprintf("shutdown step %d end", n), "name", name)
	}
	warn := func(what string, err error) {
		if err != nil {
			d.log.Warn("shutdown: "+what, "error", err.Error())
		}
	}

	step(1, "refuse", func(context.Context) {
		if d.engine != nil {
			d.engine.BeginShutdown()
		}
	})
	step(2, "interfaces", func(ctx context.Context) {
		if d.manage != nil {
			warn("the management interface did not stop in time", d.manage.Shutdown(ctx))
			warn("the management interface ended", <-d.served)
		}
		if d.agentBegan && d.cfg.Attach.StopInterfaces != nil {
			warn("the Agent interface", d.cfg.Attach.StopInterfaces(ctx))
		}
	})
	step(3, "adapters", func(ctx context.Context) {
		if d.adaptersBegan && d.cfg.Attach.StopAdapters != nil {
			warn("the adapters", d.cfg.Attach.StopAdapters(ctx))
		}
	})
	step(4, "engine", func(ctx context.Context) {
		if d.engine != nil {
			warn("the engine did not stop in time", d.engine.Stop(ctx))
		}
	})

	// The last step: the log of it is the last line, then the log is closed and
	// the lock let go. The lock goes last: while it is held no other daemon starts
	// over state that this one is still writing.
	d.log.Info("shutdown step 5 begin", "name", "release")
	var err error
	if d.engine != nil {
		if ferr := d.engine.Err(); ferr != nil {
			err = &FatalError{Err: ferr}
		}
	}
	if d.logOut != nil {
		d.logOut.Close()
	}
	d.lock.release()
	d.err = err
	close(d.done)
}
