package adapter

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban/kanbantest"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/lane"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// scripted is both handlers of a test: it records what it is given and answers
// each message from verdict, which is told how many calls there have been.
type scripted struct {
	verdict func(n int, m Message) Verdict

	mu       sync.Mutex
	messages []Message
	times    []time.Time
	cards    []string
}

func (s *scripted) flows(*Env) Handlers { return Handlers{Messages: s, Cards: s} }

func (s *scripted) HandleMessage(_ context.Context, m Message) (Verdict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, m)
	s.times = append(s.times, time.Now())
	return s.verdict(len(s.messages), m), nil
}

func (s *scripted) HandleCard(_ context.Context, c kanban.Card) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cards = append(s.cards, c.ID)
}

func (s *scripted) handled() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.messages)
}

func always(v Verdict) func(int, Message) Verdict { return func(int, Message) Verdict { return v } }

func message(id, body string) adapterproto.PageMessage {
	return adapterproto.PageMessage{ID: adapterproto.MessageID(id), From: "nodeb/codex", Body: body}
}

func page(msgs ...adapterproto.PageMessage) adapterproto.RecvPage {
	return adapterproto.RecvPage{Lease: lease, Messages: msgs}
}

// acked returns the ids of the messages the adapter acknowledged, one entry per
// acknowledged part.
func (d *daemon) acked() []string {
	var ids []string
	for _, a := range d.acks {
		for _, it := range a.Items {
			ids = append(ids, string(it.MessageID))
		}
	}
	return ids
}

// unacknowledged serves the messages again on every read until they are
// acknowledged, as a daemon does once a lease has run out.
func unacknowledged(d *daemon, msgs ...adapterproto.PageMessage) func(adapterproto.RecvParams) adapterproto.RecvPage {
	return func(adapterproto.RecvParams) adapterproto.RecvPage {
		out := []adapterproto.PageMessage{}
		for _, m := range msgs {
			if !slices.Contains(d.acked(), string(m.ID)) {
				out = append(out, m)
			}
		}
		return page(out...)
	}
}

// A message is acknowledged only after the handler reports the card written. A
// transient failure leaves it unacknowledged and receiving waits before it reads
// again; a failure that will not pass leaves it unacknowledged too, and the next
// message is still handled.
func TestAcknowledgeFollowsTheHandler(t *testing.T) {
	t.Run("transient failure pauses and the message is acknowledged once written", func(t *testing.T) {
		d := &daemon{directAccess: true}
		d.recvAnswer = unacknowledged(d, message("m1", "do it"))
		s := &scripted{verdict: func(n int, _ Message) Verdict {
			if n == 1 {
				return Retry
			}
			return Written
		}}
		h := start(t, options{d: d, flows: s.flows})
		ref := h.instanceRef()
		h.pending(ref)
		h.eventually("the acknowledgement", func() bool { return len(d.acks) > 0 })

		calls := s.handled()
		if len(calls) != 2 {
			t.Fatalf("the handler was called %d times when the message was acknowledged, want 2", len(calls))
		}
		if gap := s.times[1].Sub(s.times[0]); gap < fastTiming.retryMin {
			t.Errorf("receiving went on %v after a transient failure, want a pause of at least %v", gap, fastTiming.retryMin)
		}
		if d.acks[0].Endpoint != ref || d.acks[0].Items[0].MessageID != "m1" {
			t.Errorf("ack %+v, want message m1 on the instance endpoint", d.acks[0])
		}
	})

	t.Run("a message that cannot be written is left and the next one goes on", func(t *testing.T) {
		d := &daemon{directAccess: true}
		d.recvAnswer = unacknowledged(d, message("bad", "x"), message("good", "y"))
		s := &scripted{verdict: func(_ int, m Message) Verdict {
			if m.ID == "bad" {
				return Skip
			}
			return Written
		}}
		h := start(t, options{d: d, flows: s.flows})
		h.pending(h.instanceRef())
		h.eventually("the acknowledgement of the good message", func() bool { return len(d.acks) > 0 })
		if got := d.acked(); !slices.Equal(got, []string{"good"}) {
			t.Errorf("acknowledged %v, want only the message that was written", got)
		}
	})
}

// While the Hermes command line is unusable the adapter reads nothing, says so
// through availability, and takes up reading when Hermes is back.
func TestNoReadWhileHermesIsUnusable(t *testing.T) {
	d := &daemon{directAccess: true}
	d.recvAnswer = unacknowledged(d, message("m1", "do it"))
	s := &scripted{verdict: always(Written)}
	h := start(t, options{d: d, flows: s.flows, hermesDown: true})
	ref := h.instanceRef()
	h.pending(ref)
	h.eventually("the unavailable report", func() bool { return len(d.available) > 0 })

	time.Sleep(10 * fastTiming.receivePoll)
	d.mu.Lock()
	reads, report := len(d.recvs), d.available[0].Conditions
	d.mu.Unlock()
	if reads != 0 {
		t.Fatalf("%d endpoint.recv were sent while Hermes was unusable", reads)
	}
	if len(report) != 1 || report[0].Condition != adapterproto.ConditionDependencyMissing {
		t.Errorf("availability %+v, want one dependency_missing", report)
	}

	if err := os.Remove(filepath.Join(h.hermes, kanbantest.DownFile)); err != nil {
		t.Fatal(err)
	}
	h.eventually("the message to be acknowledged", func() bool { return len(d.acks) > 0 })
	d.mu.Lock()
	defer d.mu.Unlock()
	if last := d.available[len(d.available)-1]; len(last.Conditions) != 0 {
		t.Errorf("last availability %+v, want it cleared", last)
	}
}

// A Hermes that runs but is not one the adapter was built against is unusable like
// one that does not run: nothing is read from Sidera or from the board, and the
// daemon is told, until the release is back in range.
func TestNoDrivingAHermesOutOfRange(t *testing.T) {
	sample, err := os.ReadFile(filepath.Join("..", "kanban", "testdata", "list-ready.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := &daemon{directAccess: true}
	d.recvAnswer = unacknowledged(d, message("m1", "do it"))
	s := &scripted{verdict: always(Written)}
	h := start(t, options{d: d, flows: s.flows, ready: string(sample), tick: time.Hour, hermesVersion: "Hermes Agent v0.23.0 (2026.12.1)\n"})
	h.pending(h.instanceRef())
	h.eventually("the unavailable report", func() bool { return len(d.available) > 0 })

	time.Sleep(10 * fastTiming.receivePoll)
	d.mu.Lock()
	reads, report := len(d.recvs), d.available[0].Conditions
	d.mu.Unlock()
	s.mu.Lock()
	polled := len(s.cards)
	s.mu.Unlock()
	if reads != 0 || polled != 0 {
		t.Fatalf("%d endpoint.recv and %d cards polled while the release was out of range", reads, polled)
	}
	if len(report) != 1 || report[0].Condition != adapterproto.ConditionDependencyMissing || !strings.Contains(report[0].Detail, "0.23.0") {
		t.Errorf("availability %+v, want one dependency_missing naming the release", report)
	}

	if err := os.Remove(filepath.Join(h.hermes, kanbantest.VersionFile)); err != nil {
		t.Fatal(err)
	}
	h.eventually("the message to be acknowledged", func() bool { return len(d.acks) > 0 })
	until(t, "the board to be polled", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.cards) > 0
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	if last := d.available[len(d.available)-1]; len(last.Conditions) != 0 {
		t.Errorf("last availability %+v, want it cleared", last)
	}
}

// A message the adapter already processed, redelivered because the acknowledgement
// was lost, is acknowledged without the handler.
func TestProcessedMessageIsNotHandledAgain(t *testing.T) {
	d := &daemon{directAccess: true}
	reads := 0
	d.recvAnswer = func(adapterproto.RecvParams) adapterproto.RecvPage {
		if reads++; reads <= 2 {
			return page(message("m1", "do it"))
		}
		return page()
	}
	s := &scripted{verdict: always(Written)}
	h := start(t, options{d: d, flows: s.flows})
	h.pending(h.instanceRef())
	h.eventually("two acknowledgements", func() bool { return len(d.acks) >= 2 })
	if n := len(s.handled()); n != 1 {
		t.Errorf("the handler was called %d times for one message, want 1", n)
	}
}

// A message cut into parts reaches the handler once, whole, and every part is
// acknowledged with the lease of the page it came on.
func TestPartsReachTheHandlerWhole(t *testing.T) {
	d := &daemon{directAccess: true}
	leases := []adapterproto.LeaseRef{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	bodies := []string{"one ", "two ", "three"}
	part := func(i int) adapterproto.RecvPage {
		m := message("big", bodies[i-1])
		m.Part = &adapterproto.PagePart{Index: i, Total: 3, Label: "part " + string(rune('0'+i)) + "/3"}
		if i < 3 {
			m.Part.Next = &adapterproto.PageNext{Msg: "big", Part: i + 1}
		}
		return adapterproto.RecvPage{Lease: leases[i-1], Messages: []adapterproto.PageMessage{m}}
	}
	done := false
	d.recvAnswer = func(p adapterproto.RecvParams) adapterproto.RecvPage {
		switch {
		case p.Part > 0:
			return part(p.Part)
		case done:
			return page()
		}
		return part(1)
	}
	s := &scripted{verdict: always(Written)}
	h := start(t, options{d: d, flows: s.flows})
	h.pending(h.instanceRef())
	h.eventually("all three parts acknowledged", func() bool { done = len(d.acks) >= 3; return done })

	calls := s.handled()
	if len(calls) != 1 || calls[0].Body != "one two three" {
		t.Fatalf("handler saw %d messages, first %q; want one message, whole", len(calls), calls[0].Body)
	}
	for i, a := range d.acks[:3] {
		if a.Lease != leases[i] || a.Items[0].Part != i+1 {
			t.Errorf("ack %d: lease %s part %d, want lease %s part %d", i, a.Lease, a.Items[0].Part, leases[i], i+1)
		}
	}
}

// A conversation endpoint is registered once per key, declared hidden, retired when
// idle and reopened at the same address; after a restart the endpoints the daemon
// lists are taken over, not registered again.
func TestConversationEndpoints(t *testing.T) {
	ctx := context.Background()
	d := &daemon{directAccess: true}
	stateDir := t.TempDir()
	clock := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	src := lane.Source{Profile: "default", Platform: "feishu", ChatID: "oc_9"}
	target := config.Target{Profile: "default", Platform: "feishu", ChatID: "oc_9"}
	registrations := func(key string) (n int) {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, r := range d.registers {
			if r.Key == key {
				n++
			}
		}
		return n
	}

	h := start(t, options{d: d, stateDir: stateDir, now: func() time.Time { return clock }})
	instance := h.instanceRef()
	first, err := h.app.env.Place(ctx, src, target)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.app.env.Place(ctx, src, target)
	if err != nil || again != first {
		t.Fatalf("second placement %+v, %v; want the first, %+v", again, err, first)
	}
	if n := registrations(first.Key); n != 1 {
		t.Errorf("the key was registered %d times, want once", n)
	}
	d.mu.Lock()
	hidden := map[adapterproto.EndpointRef]bool{}
	for _, dec := range d.declares {
		hidden[dec.Endpoint] = dec.HiddenFromDirectory != nil && *dec.HiddenFromDirectory
	}
	d.mu.Unlock()
	if !hidden[first.Ref] || hidden[instance] {
		t.Errorf("hidden %v: want the conversation endpoint %s hidden and the instance endpoint %s visible", hidden, first.Ref, instance)
	}

	// Idle retirement closes the endpoint; the same key opens the same address.
	clock = clock.Add(2 * time.Hour)
	h.app.env.endpoints.retireIdle(ctx, time.Hour)
	d.mu.Lock()
	closed := slices.ContainsFunc(d.unregisters, func(u adapterproto.UnregisterParams) bool { return u.Endpoint == first.Ref })
	d.mu.Unlock()
	if !closed {
		t.Fatal("the idle endpoint was not closed")
	}
	reopened, err := h.app.env.Place(ctx, src, target)
	if err != nil || reopened.Ref != first.Ref {
		t.Fatalf("reopened %+v, %v; want the address %s again", reopened, err, first.Ref)
	}
	h.stop()

	// After a restart the daemon lists the open endpoints; one the state lacks
	// (registered just before the process ended) is taken over as well.
	other := lane.Source{Profile: "default", Platform: "feishu", ChatID: "oc_x"}
	otherKey := lane.EndpointKey(other)
	before := registrations(first.Key)
	h2 := start(t, options{d: d, stateDir: stateDir, now: func() time.Time { return clock }, listed: []adapterproto.DirectAttach{
		{Endpoint: first.Ref, Key: first.Key}, {Endpoint: "ep-listed", Key: otherKey},
	}})
	h2.instanceRef()
	got, err := h2.app.env.Place(ctx, src, target)
	if err != nil || got.Ref != first.Ref {
		t.Fatalf("after restart %+v, %v; want %s", got, err, first.Ref)
	}
	adopted, err := h2.app.env.Place(ctx, other, target)
	if err != nil || adopted.Ref != "ep-listed" {
		t.Fatalf("the listed endpoint: %+v, %v; want ep-listed", adopted, err)
	}
	if registrations(first.Key) != before || registrations(otherKey) != 0 {
		t.Errorf("a listed endpoint was registered again")
	}
}

// Without the daemon's capability the adapter attaches nothing and starts no work,
// and the probe says why.
func TestProbeFailsWithoutTheDaemonCapability(t *testing.T) {
	d := &daemon{directAccess: false}
	h := start(t, options{d: d})
	var res adapterproto.ProbeResult
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.peer.Call(ctx, adapterproto.MethodAdapterProbe, adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{}}, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Dependencies) != 1 || res.Dependencies[0].State != adapterproto.DependencyMissing ||
		!strings.Contains(res.Dependencies[0].Detail, "update the node") {
		t.Errorf("dependencies %+v, want one missing daemon with the reason", res.Dependencies)
	}
	time.Sleep(10 * fastTiming.receivePoll)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.registers)+len(d.recvs) != 0 {
		t.Errorf("the adapter did %d registrations and %d reads without the capability", len(d.registers), len(d.recvs))
	}
}

// The board poll hands the handler the ready cards meant for the lane, and only those.
func TestPollHandsOverLaneCards(t *testing.T) {
	sample, err := os.ReadFile(filepath.Join("..", "kanban", "testdata", "list-ready.json"))
	if err != nil {
		t.Fatal(err)
	}
	const sidera = `"assignee": "sidera"`
	ready := strings.Replace(string(sample), sidera, `"assignee": "Sidera:codex"`, 1)
	ready = strings.Replace(ready, sidera, `"assignee": "someone-else"`, 1)
	s := &scripted{verdict: always(Written)}
	start(t, options{d: &daemon{directAccess: true}, flows: s.flows, ready: ready})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.cards)
		s.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cards) == 0 || s.cards[0] != "t_4fa05d4f" || slices.Contains(s.cards, "t_b811595e") {
		t.Errorf("handed over %v, want only t_4fa05d4f", s.cards)
	}
}
