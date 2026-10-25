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
// to the logger, which gives it the lines kept so far. The threshold at which the
// log is rotated follows the configuration once it is loaded, and the node's
// default before.
func (d *Daemon) openLog() error {
	d.logs = logfile.New(d.fsys)
	out, err := d.logs.Open(daemonLog, d.logThreshold)
	if err != nil {
		return err
	}
	d.logOut = out
	d.defer_.attach(textHandler(out, d.cfg.Clock))
	return nil
}

// logThreshold is the size at which a log is rotated: the total the logs may take
// shared among the daemon's and the enabled instances'.
func (d *Daemon) logThreshold() int64 {
	total := config.DefaultNodeSettings().LogTotalBytes
	instances := 0
	if c := d.loaded.Load(); c != nil {
		total = c.Node().Settings.LogTotalBytes
		for _, in := range c.Instances().Items {
			if in.Enabled {
				instances++
			}
		}
	}
	return logfile.Threshold(total, instances)
}
