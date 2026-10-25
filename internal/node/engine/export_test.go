package engine

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/store"
)

// The hooks the tests of the engine use to reach what only the loop may touch.

type cmdStall struct {
	started chan struct{}
	release chan struct{}
}

func (c *cmdStall) gate() gate             { return gateAlways }
func (c *cmdStall) fail(error)             {}
func (c *cmdStall) run(*Engine, time.Time) { close(c.started); <-c.release }

// StallLoop makes the loop stop taking commands until the returned function is
// called, with the command that holds it already running.
func StallLoop(t testing.TB, e *Engine) (release func()) {
	t.Helper()
	c := &cmdStall{started: make(chan struct{}), release: make(chan struct{})}
	if err := e.enqueue(context.Background(), submitInternal, c); err != nil {
		t.Fatalf("StallLoop: %v", err)
	}
	select {
	case <-c.started:
	case <-time.After(10 * time.Second):
		t.Fatal("StallLoop: the loop did not take the command")
	}
	var once sync.Once
	release = func() { once.Do(func() { close(c.release) }) }
	t.Cleanup(release)
	return release
}

// WaitInbox returns when n commands wait in the inbox.
func WaitInbox(t testing.TB, e *Engine, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(e.inbox) != n {
		if time.Now().After(deadline) {
			t.Fatalf("WaitInbox: %d commands wait, want %d", len(e.inbox), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// WaitingForRunning is how many adapter requests wait for the running phase.
func WaitingForRunning(e *Engine) int { return int(e.waiting.Load()) }

type cmdProbeAdmit struct {
	call[struct{}]
	recipients int
}

func (c *cmdProbeAdmit) gate() gate { return gateAlways }
func (c *cmdProbeAdmit) run(e *Engine, _ time.Time) {
	c.done(struct{}{}, e.classify(e.admitNewWork(c.recipients)))
}

// AdmitNewWork runs the capacity check as a command of the loop, the way the
// intake of new work does, for recipients recipients.
func AdmitNewWork(ctx context.Context, e *Engine, recipients int) error {
	c := &cmdProbeAdmit{call: newCall[struct{}](), recipients: recipients}
	_, err := roundTrip(ctx, e, submitExternal, c, c.call)
	return err
}

// SpawnWorker starts a goroutine the engine owns and counts, as the workers of
// the workers added later are.
func SpawnWorker(e *Engine, _ string, fn func(ctx context.Context)) { e.spawn(fn) }

type cmdClearRollback struct{ call[struct{}] }

func (c *cmdClearRollback) gate() gate { return gateAlways }
func (c *cmdClearRollback) run(e *Engine, now time.Time) {
	e.clearRollback(now)
	c.done(struct{}{}, nil)
}

// ClearRollback is what clearing the pause of a wall clock set back does inside
// the loop; the management operation that persists it is added later.
func ClearRollback(ctx context.Context, e *Engine) error {
	c := &cmdClearRollback{call: newCall[struct{}]()}
	_, err := roundTrip(ctx, e, submitExternal, c, c.call)
	return err
}

type cmdViewVsDisk struct {
	call[[]string]
	sc *store.Scan
}

func (c *cmdViewVsDisk) gate() gate { return gateAlways }
func (c *cmdViewVsDisk) run(e *Engine, _ time.Time) {
	var diff []string
	fresh, err := buildView(e.d.Node, c.sc)
	if err != nil {
		c.done(nil, err)
		return
	}
	same := func(what string, a, b store.Record) {
		x, err1 := store.EncodeRecord(a)
		y, err2 := store.EncodeRecord(b)
		if err1 != nil || err2 != nil || !bytes.Equal(x, y) {
			diff = append(diff, what)
		}
	}
	for k, in := range fresh.in {
		if mine, ok := e.view.in[k]; !ok {
			diff = append(diff, "in "+k.String()+" is missing from the view")
		} else {
			same("in "+k.String(), mine, in)
		}
	}
	for k := range e.view.in {
		if _, ok := fresh.in[k]; !ok {
			diff = append(diff, "in "+k.String()+" is in the view only")
		}
	}
	for k, ctl := range fresh.ctl {
		if mine, ok := e.view.ctl[k]; !ok {
			diff = append(diff, "ctl "+k.String()+" is missing from the view")
		} else {
			same("ctl "+k.String(), mine, ctl)
		}
	}
	if e.view.unfinished != fresh.unfinished {
		diff = append(diff, fmt.Sprintf("unfinished %d, from the disk %d", e.view.unfinished, fresh.unfinished))
	}
	if !reflect.DeepEqual(e.view.bodyRefs, fresh.bodyRefs) {
		diff = append(diff, fmt.Sprintf("body references %v, from the disk %v", e.view.bodyRefs, fresh.bodyRefs))
	}
	if len(e.view.queued) != len(fresh.queued) {
		diff = append(diff, "the queues differ")
	}
	c.done(diff, nil)
}

// ViewDiffersFromDisk lists where the view of the loop differs from the view a
// fresh scan builds: records by their encoding, the count of unfinished
// deliveries, the references to bodies.
func ViewDiffersFromDisk(ctx context.Context, e *Engine, sc *store.Scan) ([]string, error) {
	c := &cmdViewVsDisk{call: newCall[[]string](), sc: sc}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}
