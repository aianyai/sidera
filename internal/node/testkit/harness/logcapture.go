package harness

import (
	"context"
	"log/slog"
	"sync"
)

// LogCapture is a slog handler that keeps the messages a process logs, so that a
// test can read the order of the startup steps and the like.
type LogCapture struct {
	mu   sync.Mutex
	msgs []string
}

var _ slog.Handler = (*LogCapture)(nil)

// Enabled reports true for every level.
func (l *LogCapture) Enabled(context.Context, slog.Level) bool { return true }

// Handle keeps the message of the record.
func (l *LogCapture) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, r.Message)
	return nil
}

// WithAttrs returns the handler itself: attributes are not kept.
func (l *LogCapture) WithAttrs([]slog.Attr) slog.Handler { return l }

// WithGroup returns the handler itself: groups are not kept.
func (l *LogCapture) WithGroup(string) slog.Handler { return l }

// Messages returns what was logged, in order.
func (l *LogCapture) Messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.msgs...)
}
