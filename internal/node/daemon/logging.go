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
type deferredHandler struct {
	mu      sync.Mutex
	target  slog.Handler
	pending []slog.Record
}

func (h *deferredHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *deferredHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.target != nil {
		return h.target.Handle(ctx, r)
	}
	h.pending = append(h.pending, r.Clone())
	return nil
}

// WithAttrs and WithGroup are not used on this handler: the daemon logs through
// the logger made from it and adds no attributes to it.
func (h *deferredHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *deferredHandler) WithGroup(string) slog.Handler      { return h }

// attach writes what was kept and sends what follows to target.
func (h *deferredHandler) attach(target slog.Handler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.pending {
		_ = target.Handle(context.Background(), r)
	}
	h.pending, h.target = nil, target
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
