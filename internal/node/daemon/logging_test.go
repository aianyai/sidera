package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// noTime drops the time of a line, so that a test compares the rest of it.
func noTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

func targetAt(buf *bytes.Buffer, level slog.Level) slog.Handler {
	return slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level, ReplaceAttr: noTime})
}

// A logger that adds attributes or opens a group before the log is attached writes
// them on its lines once it is, in the order they were added, and a logger made
// after the attach writes them too: the attributes are part of what the line says.
func TestAttributesAndGroupsOfALoggerAreKeptAcrossTheAttach(t *testing.T) {
	h := newDeferredHandler()
	root := slog.New(h)
	early := root.With("instance", "chat").WithGroup("request").With("id", 7)
	early.Info("before", "k", "v")
	root.Info("plain before")

	var buf bytes.Buffer
	h.attach(targetAt(&buf, slog.LevelInfo))
	early.Info("after")
	late := root.With("late", true).WithGroup("g")
	late.Info("made after", "x", 1)
	root.Info("plain after")

	want := strings.Join([]string{
		`level=INFO msg=before instance=chat request.id=7 request.k=v`,
		`level=INFO msg="plain before"`,
		`level=INFO msg=after instance=chat request.id=7`,
		`level=INFO msg="made after" late=true g.x=1`,
		`level=INFO msg="plain after"`,
	}, "\n") + "\n"
	if got := buf.String(); got != want {
		t.Errorf("the lines are\n%s\nwant\n%s", got, want)
	}
}

// A group that is opened and an attribute that is added to an empty list or an
// empty name change nothing, as slog says.
func TestAnEmptyGroupAndNoAttributesChangeNothing(t *testing.T) {
	h := newDeferredHandler()
	if h.WithAttrs(nil) != slog.Handler(h) || h.WithGroup("") != slog.Handler(h) {
		t.Error("a handler was made for nothing")
	}
}

// Whether a level is enabled is the target's answer once there is a target: a
// debug line is not written by a log that is at info, and is by one at debug,
// which is what a person who turns the level of the log down is asking for. Before
// the attach nothing under info is kept, and a line that was kept is given to the
// target only if the target would have taken it.
func TestTheLevelIsTheTargetsOnceItIsAttachedAndInfoBefore(t *testing.T) {
	ctx := context.Background()
	h := newDeferredHandler()
	if h.Enabled(ctx, slog.LevelDebug) || !h.Enabled(ctx, slog.LevelInfo) || !h.Enabled(ctx, slog.LevelError) {
		t.Error("before the attach, only info and above are enabled")
	}
	log := slog.New(h)
	log.Debug("never kept")
	log.Info("kept")
	// A record of a level the target drops is not written when the lines are given
	// to it, even if it was kept: a handler used directly may not have asked.
	r := slog.NewRecord(time.Time{}, slog.LevelDebug, "forced", 0)
	_ = h.Handle(ctx, r)

	var buf bytes.Buffer
	h.attach(targetAt(&buf, slog.LevelWarn))
	if got := buf.String(); got != "" {
		t.Errorf("a target at warn was given %q", got)
	}
	if h.Enabled(ctx, slog.LevelInfo) || !h.Enabled(ctx, slog.LevelWarn) {
		t.Error("after the attach, the answer is the target's")
	}
	log.Info("dropped")
	log.Warn("written")
	if got := buf.String(); got != "level=WARN msg=written\n" {
		t.Errorf("the log holds %q", got)
	}

	var dbg bytes.Buffer
	h2 := newDeferredHandler()
	h2.attach(targetAt(&dbg, slog.LevelDebug))
	slog.New(h2).Debug("wanted")
	if got := dbg.String(); got != "level=DEBUG msg=wanted\n" {
		t.Errorf("a target at debug was given %q", got)
	}
}
