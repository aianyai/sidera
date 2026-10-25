package daemon

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/logfile"
)

// daemonLog is the name of the daemon's own log under logs/.
const daemonLog = "daemon.log"

// deferredHandler keeps the lines logged before the log can be opened, and passes
// them on in order once it is: the first steps of the start run before logs/ is
// known to exist, and they belong in the log all the same. When the start fails
// before the log is attached the lines are dropped with it, and the error goes to
// the caller.
//
// A logger made from it with attributes or groups, before the log is attached or
// after, writes them on every line it logs: the handlers that With... returns share
// the one core and apply their own attributes and groups to the target, in the
// order they were added, once there is one.
type deferredHandler struct {
	core *deferredCore
	// ops are the attributes and groups added to this handler, in order.
	ops []handlerOp
	// resolved is the target with ops applied; it is made on first use after the
	// attach, under the core's lock.
	resolved slog.Handler
}

// deferredCore is what every handler derived from one deferredHandler shares.
type deferredCore struct {
	mu      sync.Mutex
	target  slog.Handler
	pending []pendingRecord
}

// handlerOp is one WithAttrs or WithGroup: a group when group is not empty.
type handlerOp struct {
	group string
	attrs []slog.Attr
}

// pendingRecord is a line kept for the attach, with the handler it was logged to.
type pendingRecord struct {
	h *deferredHandler
	r slog.Record
}

func newDeferredHandler() *deferredHandler { return &deferredHandler{core: &deferredCore{}} }

// forTarget is the handler of the target that h stands for: the target with the
// attributes and groups of h applied. The core's lock is held.
func (h *deferredHandler) forTarget() slog.Handler {
	if h.resolved == nil {
		out := h.core.target
		for _, op := range h.ops {
			if op.group != "" {
				out = out.WithGroup(op.group)
			} else {
				out = out.WithAttrs(op.attrs)
			}
		}
		h.resolved = out
	}
	return h.resolved
}

// Enabled is the target's answer once there is a target. Before it, the level of
// the log the daemon opens by default, so that a line the log would drop is not
// kept for it.
func (h *deferredHandler) Enabled(ctx context.Context, level slog.Level) bool {
	h.core.mu.Lock()
	defer h.core.mu.Unlock()
	if h.core.target != nil {
		return h.core.target.Enabled(ctx, level)
	}
	return level >= slog.LevelInfo
}

func (h *deferredHandler) Handle(ctx context.Context, r slog.Record) error {
	h.core.mu.Lock()
	defer h.core.mu.Unlock()
	if h.core.target != nil {
		return h.forTarget().Handle(ctx, r)
	}
	h.core.pending = append(h.core.pending, pendingRecord{h: h, r: r.Clone()})
	return nil
}

func (h *deferredHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.derive(handlerOp{attrs: append([]slog.Attr(nil), attrs...)})
}

func (h *deferredHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.derive(handlerOp{group: name})
}

func (h *deferredHandler) derive(op handlerOp) *deferredHandler {
	ops := make([]handlerOp, len(h.ops), len(h.ops)+1)
	copy(ops, h.ops)
	return &deferredHandler{core: h.core, ops: append(ops, op)}
}

// attach writes what was kept, as each line would have been written had the
// target been there, and sends what follows to target.
func (h *deferredHandler) attach(target slog.Handler) {
	c := h.core
	c.mu.Lock()
	defer c.mu.Unlock()
	c.target = target
	for _, p := range c.pending {
		if target.Enabled(context.Background(), p.r.Level) {
			_ = p.h.forTarget().Handle(context.Background(), p.r)
		}
	}
	c.pending = nil
}

// textHandler writes the lines of the log in the form of the standard library's
// text handler, with the time of the injected clock: the record's own time is read
// from the process clock by the logger, which tests cannot control.
func textHandler(w io.Writer, clk clock.Clock) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				a.Value = slog.TimeValue(clk.Now())
			}
			return a
		},
	})
}

// openLog opens the daemon's own log through the restricted handle and attaches it
// to the logger, which gives it the lines kept so far. The logs share the total of
// the configuration once it is loaded, and the node's default before; the handle
// applies the rule, and the daemon says only where the total and the number of
// enabled instances are read from.
func (d *Daemon) openLog() error {
	d.logs = logfile.New(d.fsys, logfile.Budget{Total: d.logTotal, Instances: d.enabledInstances})
	out, err := d.logs.Open(daemonLog)
	if err != nil {
		return err
	}
	d.logOut = out
	d.defer_.attach(textHandler(out, d.cfg.Clock))
	return nil
}

// logTotal is the number of bytes the logs may take together.
func (d *Daemon) logTotal() int64 {
	if c := d.loaded.Load(); c != nil {
		return c.Node().Settings.LogTotalBytes
	}
	return config.DefaultNodeSettings().LogTotalBytes
}

// enabledInstances is the number of enabled adapter instances: each has a log.
func (d *Daemon) enabledInstances() int {
	c := d.loaded.Load()
	if c == nil {
		return 0
	}
	n := 0
	for _, in := range c.Instances().Items {
		if in.Enabled {
			n++
		}
	}
	return n
}
