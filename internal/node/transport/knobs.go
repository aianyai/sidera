package transport

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock"
)

// Knob names one of the faults a [Loopback] can be made to show.
type Knob uint8

const (
	// KnobDropReceipt: the target processed an envelope, and its receipt is
	// thrown away, so the sender hears a timeout.
	KnobDropReceipt Knob = iota + 1
	// KnobUnreachable: a call to a node never reaches it.
	KnobUnreachable
	// KnobHoldReports: the status reports of a message are kept from the source,
	// in the receipt of its send and in every pull.
	KnobHoldReports
	// KnobDelay: a call waits on the injected clock before it goes on.
	KnobDelay
	// KnobMutate: a message that was sent is delivered once more with other
	// content.
	KnobMutate

	knobCount = int(KnobMutate) + 1
)

var knobNames = [...]string{
	KnobDropReceipt: "drop-receipt",
	KnobUnreachable: "unreachable",
	KnobHoldReports: "hold-reports",
	KnobDelay:       "delay",
	KnobMutate:      "mutate",
}

// String returns the name of the knob as the ledger of a test writes it.
func (k Knob) String() string {
	if k >= 1 && int(k) < knobCount {
		return knobNames[k]
	}
	return fmt.Sprintf("invalid(%d)", uint8(k))
}

// Hit is one time a knob changed what a call did.
type Hit struct {
	Knob Knob
	// Node is the node the call was for.
	Node ident.NodeID
	// Message is the message the knob acted on, for the knobs that are about one
	// message (hold-reports, mutate); empty for the others.
	Message delivery.MessageID
}

// Replay is what the target answered to the replay a [Knobs.Mutate] made.
type Replay struct {
	Receipt Receipt
	Err     error
}

// Knobs are the faults a [Loopback] can be made to show. They are for tests, are
// safe to set while calls run, and are all off in a new value. Every hit is
// counted ([Knobs.Hits]) and passed to the observer ([Knobs.Observe]), so that a
// test can show that the fault it set was the fault it got.
type Knobs struct {
	clk clock.Clock

	mu           sync.Mutex
	dropReceipts int
	unreachable  map[ident.NodeID]bool
	held         map[delivery.MessageID]bool
	delay        time.Duration
	waits        map[uint64]time.Time // calls waiting out a delay, by the time it is due
	nextWait     uint64
	mutate       map[delivery.MessageID]bool
	mutated      map[delivery.MessageID]Replay
	hits         [knobCount]int
	observer     func(Hit)
}

// NewKnobs returns knobs that are all off. clk is the clock a delay is waited
// on; it may be nil when no test sets a delay.
func NewKnobs(clk clock.Clock) *Knobs {
	return &Knobs{
		clk:         clk,
		unreachable: map[ident.NodeID]bool{},
		held:        map[delivery.MessageID]bool{},
		waits:       map[uint64]time.Time{},
		mutate:      map[delivery.MessageID]bool{},
		mutated:     map[delivery.MessageID]Replay{},
	}
}

// DropReceipt makes the next n sends that the target processed return
// [ErrTimeout] in place of the receipt. A send the target answered with an error
// has no receipt and is not counted.
func (k *Knobs) DropReceipt(n int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.dropReceipts = max(n, 0)
}

// Unreachable makes every call to node return [ErrUnreachable] without calling
// the target while on is true, and lets it through again when it is false.
func (k *Knobs) Unreachable(node ident.NodeID, on bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if on {
		k.unreachable[node] = true
	} else {
		delete(k.unreachable, node)
	}
}

// HoldReports keeps the reports of msg from the source until [Knobs.Release]:
// the receipt of the send of msg carries none, and a pull for msg is answered
// empty without asking the target. The target processes the envelope as usual.
func (k *Knobs) HoldReports(msg delivery.MessageID) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.held[msg] = true
}

// Release gives back the reports that [Knobs.HoldReports] kept for msg.
func (k *Knobs) Release(msg delivery.MessageID) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.held, msg)
}

// Delay makes every call wait d on the clock of the knobs before it goes on, until
// Delay(0). Calls run one after another on the engine's side, so a delayed call
// holds up the ones behind it. A call whose context ends while it waits returns
// the context's error. It panics when the knobs have no clock.
func (k *Knobs) Delay(d time.Duration) {
	if d > 0 && k.clk == nil {
		panic("transport: Delay needs knobs made with a clock")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.delay = max(d, 0)
}

// Mutate arms one replay of msg. After the target has taken the next send of
// msg, the same envelope from the same sender is delivered to it once more with
// other content: a changed body, or, for an envelope without a body, a changed
// thread. The sender sees only its own send. The target's answer to the replay
// is kept for [Knobs.Mutated]. A send that the target answered with an error
// does not spend the replay.
func (k *Knobs) Mutate(msg delivery.MessageID) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.mutate[msg] = true
	delete(k.mutated, msg)
}

// Mutated returns what the target answered to the replay of msg, and whether
// the replay has happened.
func (k *Knobs) Mutated(msg delivery.MessageID) (Replay, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	r, ok := k.mutated[msg]
	return r, ok
}

// Hits returns how many times the knob has changed what a call did.
func (k *Knobs) Hits(knob Knob) int {
	if knob < 1 || int(knob) >= knobCount {
		return 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.hits[knob]
}

// Observe sets the function that is told of every hit, in the goroutine of the
// call that was hit and with no lock held. It replaces any earlier observer.
func (k *Knobs) Observe(f func(Hit)) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.observer = f
}

// Waiting returns how many calls are waiting out a delay whose time has not
// come. A call whose delay is due is work again and is not counted, so a
// caller that asks whether the transport is quiet can tell a call that waits for
// the clock from one that has only to run.
func (k *Knobs) Waiting() int {
	if k.clk == nil {
		return 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.clk.Now()
	n := 0
	for _, due := range k.waits {
		if now.Before(due) {
			n++
		}
	}
	return n
}

func (k *Knobs) hit(h Hit) {
	k.mu.Lock()
	k.hits[h.Knob]++
	obs := k.observer
	k.mu.Unlock()
	if obs != nil {
		obs(h)
	}
}

// wait runs the delay of a call for node: it returns once the delay has passed,
// or with the context's error.
func (k *Knobs) wait(ctx context.Context, node ident.NodeID) error {
	k.mu.Lock()
	d := k.delay
	k.mu.Unlock()
	if d <= 0 {
		return nil
	}
	// The due time is read before the timer is made: if the clock moves in
	// between, the call is counted as work a moment early, which a quiescence
	// check reports as a failure, and never as quiet while it still waits.
	due := k.clk.Now().Add(d)
	t := k.clk.NewTimer(d)
	defer t.Stop()
	k.mu.Lock()
	id := k.nextWait
	k.nextWait++
	k.waits[id] = due
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		delete(k.waits, id)
		k.mu.Unlock()
	}()
	k.hit(Hit{Knob: KnobDelay, Node: node})
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// refuses reports whether a call to node is made unreachable, and counts it.
func (k *Knobs) refuses(node ident.NodeID) bool {
	k.mu.Lock()
	on := k.unreachable[node]
	k.mu.Unlock()
	if on {
		k.hit(Hit{Knob: KnobUnreachable, Node: node})
	}
	return on
}

// watchesMessages reports whether any knob is armed that needs the id of the
// message a send carries; the envelope is decoded only then.
func (k *Knobs) watchesMessages() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.held) > 0 || len(k.mutate) > 0
}

// holds reports whether the reports of msg are held, and counts it.
func (k *Knobs) holds(node ident.NodeID, msg delivery.MessageID) bool {
	k.mu.Lock()
	on := k.held[msg]
	k.mu.Unlock()
	if on {
		k.hit(Hit{Knob: KnobHoldReports, Node: node, Message: msg})
	}
	return on
}

// dropsReceipt spends one dropped receipt if any is left, and counts it.
func (k *Knobs) dropsReceipt(node ident.NodeID) bool {
	k.mu.Lock()
	drop := k.dropReceipts > 0
	if drop {
		k.dropReceipts--
	}
	k.mu.Unlock()
	if drop {
		k.hit(Hit{Knob: KnobDropReceipt, Node: node})
	}
	return drop
}

// takeMutate spends the replay armed for msg, if any.
func (k *Knobs) takeMutate(msg delivery.MessageID) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.mutate[msg] {
		return false
	}
	delete(k.mutate, msg)
	return true
}

func (k *Knobs) keepReplay(node ident.NodeID, msg delivery.MessageID, r Replay, made bool) {
	k.mu.Lock()
	k.mutated[msg] = r
	k.mu.Unlock()
	if made {
		k.hit(Hit{Knob: KnobMutate, Node: node, Message: msg})
	}
}
