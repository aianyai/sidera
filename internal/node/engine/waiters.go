package engine

import (
	"context"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
)

// A recv that finds nothing queued and was asked to wait is registered as a
// waiter in the same command that found the queue empty, so that no message that
// arrives in between can be missed: the check and the registration have no other
// command between them, because only the loop runs commands. What wakes the
// waiter is any later command that makes a message of the endpoint ready to
// hand out; it hands the page out itself, in its own command, so there is no
// second window in which another reader could take the message before the
// waiter wakes. The deadline is an entry of the schedule, so it follows the
// injected clock and Quiesce sees it. At most one recv of an endpoint waits: a
// newer one ends the older with an empty page.
//
// The registry is the part of this the loop owns; the page that wakes a waiter
// is made where messages are handed out (pull.go).

// waitEnd is why a wait ended.
type waitEnd uint8

// The ways a wait ends. The zero value is not one.
const (
	// waitWoken: a command made a message ready and handed the page to the waiter.
	waitWoken waitEnd = iota + 1
	// waitDeadline: the time the waiter would wait passed; its page is empty.
	waitDeadline
	// waitSuperseded: a newer recv of the same endpoint ended it; its page is empty.
	waitSuperseded
	// waitShutdown: the node is shutting down; its page is empty.
	waitShutdown
)

func (w waitEnd) String() string {
	switch w {
	case waitWoken:
		return "woken"
	case waitDeadline:
		return "deadline"
	case waitSuperseded:
		return "superseded"
	case waitShutdown:
		return "shutdown"
	}
	return "invalid"
}

// waitOutcome is how a wait ended, and the page when it was woken.
type waitOutcome struct {
	End  waitEnd
	Page []byte
}

// waiter is a recv that waits for its endpoint to have a message. Its outcome is
// sent once, on a channel of capacity one, so the loop never waits for a caller
// that has gone.
type waiter struct {
	endpoint ident.EndpointID
	deadline time.Time
	out      chan waitOutcome
	// lease and budget are those of the recv that waits: the page it is woken with is
	// built with them. empty is the page it is answered with when it is woken by
	// nothing, made when it was registered so that the answer needs no randomness of
	// the loop's after the loop has done with it.
	lease  delivery.LeaseID
	budget handoff.Budget
	empty  []byte
}

func newWaiter(endpoint ident.EndpointID, deadline time.Time) *waiter {
	return &waiter{endpoint: endpoint, deadline: deadline, out: make(chan waitOutcome, 1)}
}

func (w *waiter) end(how waitEnd, page []byte) { w.out <- waitOutcome{End: how, Page: page} }

func waitKey(endpoint ident.EndpointID) schedKey { return schedKey{schedWait, endpoint.String()} }

// waitFor registers a waiter, ending the one the endpoint already has: a newer
// recv of the same endpoint wins, because the older one's caller is likelier to
// have given up (a client killed and retried) than to be a second reader, and a
// page handed to two waiters would show the agent the same message twice.
func (e *Engine) waitFor(w *waiter) {
	e.supersede(w.endpoint)
	e.waits[w.endpoint] = w
	e.sched.set(waitKey(w.endpoint), w.deadline)
}

// supersede ends the wait the endpoint has, if it has one, with an empty page.
func (e *Engine) supersede(endpoint ident.EndpointID) {
	if old := e.waits[endpoint]; old != nil {
		delete(e.waits, endpoint)
		e.sched.remove(waitKey(endpoint))
		old.end(waitSuperseded, nil)
	}
}

// wake hands a page to the waiter of the endpoint, if it has one, and takes the
// waiter off the schedule. It reports whether there was one.
func (e *Engine) wake(endpoint ident.EndpointID, page []byte) bool {
	w := e.waits[endpoint]
	if w == nil {
		return false
	}
	delete(e.waits, endpoint)
	e.sched.remove(waitKey(endpoint))
	w.end(waitWoken, page)
	return true
}

// unwait drops a waiter that its caller gave up, unless a newer one has taken
// its place.
func (e *Engine) unwait(w *waiter) {
	if e.waits[w.endpoint] == w {
		delete(e.waits, w.endpoint)
		e.sched.remove(waitKey(w.endpoint))
	}
}

// expireWait ends the wait of the endpoint whose deadline passed.
func (e *Engine) expireWait(endpoint ident.EndpointID) {
	if w := e.waits[endpoint]; w != nil {
		delete(e.waits, endpoint)
		w.end(waitDeadline, nil)
	}
}

// endWaits ends every wait: the node is shutting down. It must come before the
// interfaces stop serving, because a request that waits holds its connection
// open until its wait ends.
func (e *Engine) endWaits() {
	for ep, w := range e.waits {
		delete(e.waits, ep)
		e.sched.remove(waitKey(ep))
		w.end(waitShutdown, nil)
	}
}

// awaitWaiter is what the caller of a recv that waits does with its waiter: it
// waits for the outcome the loop sends, and it watches the shutdown itself. The
// loop ends every wait when the node begins to stop, but it learns of it through
// a command that may find the inbox full and be dropped; the waiting side does not
// depend on that, so a shutdown ends a wait whatever the inbox holds. A page that
// is ready when the shutdown is seen is still handed out. A context that ends
// first gives its error and leaves the waiter to unwait.
func (e *Engine) awaitWaiter(ctx context.Context, w *waiter) (waitOutcome, error) {
	select {
	case o := <-w.out:
		return o, nil
	case <-e.shutdown:
		select {
		case o := <-w.out:
			return o, nil
		default:
			return waitOutcome{End: waitShutdown}, nil
		}
	case <-ctx.Done():
		return waitOutcome{}, ctx.Err()
	}
}

// cmdEndWaits ends every wait. BeginShutdown posts it when the inbox has room, so
// that the waits end before the loop has done anything else; when it has none,
// awaitWaiter's own watch of the shutdown ends them, and finish ends what is left
// in the registry.
type cmdEndWaits struct{}

func (cmdEndWaits) gate() gate                 { return gateAlways }
func (cmdEndWaits) fail(error)                 {}
func (cmdEndWaits) run(e *Engine, _ time.Time) { e.endWaits() }

// cmdUnwait drops the wait of a caller that gave up.
type cmdUnwait struct{ w *waiter }

func (c cmdUnwait) gate() gate                 { return gateAlways }
func (c cmdUnwait) fail(error)                 {}
func (c cmdUnwait) run(e *Engine, _ time.Time) { e.unwait(c.w) }
