package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
)

func endedAs(t testing.TB, w *engine.TestWait) (string, []byte) {
	t.Helper()
	end, page, ok := w.Outcome()
	if !ok {
		t.Fatal("the wait has not ended")
	}
	return end, page
}

func stillWaiting(t testing.TB, w *engine.TestWait, why string) {
	t.Helper()
	if end, _, ok := w.Outcome(); ok {
		t.Errorf("%s: the wait ended (%s)", why, end)
	}
}

// A wait ends at its deadline on the clock the engine was given, with an empty
// page, and not a moment before.
func TestAWaitEndsAtItsDeadlineOnTheInjectedClock(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ep := mustEndpoint(t, 1)
	w, err := engine.WaitFor(context.Background(), e, ep, t0.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	advance(t, r, e, 4*time.Second)
	stillWaiting(t, w, "a second before its deadline")
	advance(t, r, e, time.Second)
	if end, page := endedAs(t, w); end != "deadline" || len(page) != 0 {
		t.Errorf("ended as %s with a page of %d bytes, want the deadline and an empty page", end, len(page))
	}
}

// At most one recv of an endpoint waits: a newer one ends the older with an empty
// page, and only the newer is woken.
func TestANewWaitOfTheSameEndpointEndsTheOldOne(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ep, other := mustEndpoint(t, 1), mustEndpoint(t, 2)
	first, err := engine.WaitFor(context.Background(), e, ep, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	bystander, err := engine.WaitFor(context.Background(), e, other, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.WaitFor(context.Background(), e, ep, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if end, page := endedAs(t, first); end != "superseded" || len(page) != 0 {
		t.Errorf("the older wait ended as %s with %d bytes", end, len(page))
	}
	stillWaiting(t, second, "the newer wait")
	stillWaiting(t, bystander, "the wait of another endpoint")
	if woke, err := engine.Wake(context.Background(), e, ep, []byte("page")); err != nil || !woke {
		t.Fatalf("Wake = %v, %v", woke, err)
	}
	if end, page := endedAs(t, second); end != "woken" || string(page) != "page" {
		t.Errorf("the newer wait ended as %s with %q", end, page)
	}
}

// A woken wait leaves nothing behind: it is off the schedule, so its deadline
// does nothing later and the engine is quiet.
func TestAWokenWaitIsOffTheScheduleAndWakesOnlyOnce(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ep := mustEndpoint(t, 1)
	w, err := engine.WaitFor(context.Background(), e, ep, t0.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if woke, _ := engine.Wake(context.Background(), e, ep, []byte("x")); !woke {
		t.Fatal("no waiter to wake")
	}
	if woke, _ := engine.Wake(context.Background(), e, ep, []byte("y")); woke {
		t.Error("a second wake found a waiter")
	}
	if end, page := endedAs(t, w); end != "woken" || string(page) != "x" {
		t.Errorf("ended as %s with %q", end, page)
	}
	advance(t, r, e, time.Minute) // the deadline passes with nothing waiting
	if _, _, ended := w.Outcome(); ended {
		t.Error("the deadline of a woken wait ended it again")
	}
}

// A caller that gave up takes its wait off; a newer wait of the endpoint is not
// touched by it.
func TestAWaitTheCallerGaveUpIsDroppedAndANewerOneIsKept(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ep := mustEndpoint(t, 1)
	old, err := engine.WaitFor(context.Background(), e, ep, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Unwait(context.Background(), e, old); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if woke, _ := engine.Wake(context.Background(), e, ep, []byte("x")); woke {
		t.Error("a wait the caller gave up was woken")
	}
	gone, _ := engine.WaitFor(context.Background(), e, ep, t0.Add(time.Minute))
	newer, _ := engine.WaitFor(context.Background(), e, ep, t0.Add(time.Minute))
	if end, _ := endedAs(t, gone); end != "superseded" {
		t.Errorf("ended as %s", end)
	}
	if err := engine.Unwait(context.Background(), e, gone); err != nil { // already replaced: no effect on the newer
		t.Fatal(err)
	}
	settle(t, e)
	if woke, _ := engine.Wake(context.Background(), e, ep, []byte("z")); !woke {
		t.Error("giving up an old wait took the newer one off")
	}
	if end, _ := endedAs(t, newer); end != "woken" {
		t.Errorf("the newer wait ended as %s", end)
	}
}

// Shutdown ends every wait with an empty page before anything else stops, so a
// request that waits does not hold its connection open.
func TestShutdownEndsEveryWait(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	var ws []*engine.TestWait
	for i := byte(1); i <= 3; i++ {
		w, err := engine.WaitFor(context.Background(), e, mustEndpoint(t, i), t0.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		ws = append(ws, w)
	}
	e.BeginShutdown()
	waitUntil(t, func() bool {
		for _, w := range ws {
			if _, _, ended := peek(w); !ended {
				return false
			}
		}
		return true
	})
	for i, w := range ws {
		if end, page := endedAs(t, w); end != "shutdown" || len(page) != 0 {
			t.Errorf("wait %d ended as %s with %d bytes, want shutdown and an empty page", i, end, len(page))
		}
	}
}

// The command that ends the waits is posted without waiting, so it is dropped when
// the inbox is full. The waiting side watches the shutdown itself: a shutdown with a
// full inbox and a loop that is far behind still ends the wait, at once and with an
// empty page.
func TestShutdownEndsEveryWaitEvenWhenTheInboxIsFull(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withParams(engine.Params{InboxLen: 2}))
	e := r.start()
	ep := mustEndpoint(t, 1)
	w, err := engine.WaitFor(context.Background(), e, ep, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	release := engine.StallLoop(t, e)
	pending := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := e.Endpoints(context.Background()); pending <- err }()
		engine.WaitInbox(t, e, i+1)
	}

	ended := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		end, page, err := engine.AwaitWait(ctx, e, w)
		if err != nil || len(page) != 0 {
			end = "error: " + err.Error()
		}
		ended <- end
	}()
	e.BeginShutdown()
	if got := <-ended; got != "shutdown" {
		t.Errorf("the wait ended as %q while the loop was stalled with a full inbox, want shutdown", got)
	}
	release()
	for i := 0; i < 2; i++ {
		<-pending // answered, with a refusal: the engine is stopping
	}
}

// A page that is ready when the shutdown is seen is still handed out: the shutdown
// ends a wait that would otherwise go on, it does not take back what was woken.
func TestAPageThatIsReadyWinsOverTheShutdown(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ep := mustEndpoint(t, 1)
	w, err := engine.WaitFor(context.Background(), e, ep, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if woke, err := engine.Wake(context.Background(), e, ep, []byte("page")); err != nil || !woke {
		t.Fatalf("Wake = %v, %v", woke, err)
	}
	e.BeginShutdown()
	end, page, err := engine.AwaitWait(context.Background(), e, w)
	if err != nil || end != "woken" || string(page) != "page" {
		t.Errorf("AwaitWait = %q, %q, %v; want the page that was woken", end, page, err)
	}
}

// peek is Outcome that puts back what it took, for a poll.
func peek(w *engine.TestWait) (string, []byte, bool) {
	end, page, ok := w.Outcome()
	if ok {
		engine.PutBack(w, end, page)
	}
	return end, page, ok
}
