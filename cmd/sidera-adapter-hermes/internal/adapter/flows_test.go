package adapter

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban/kanbantest"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

const (
	messageCard = `{"sidera":1,"to":"codex","mode":"message","task":"review the diff"}`
	sessionCard = `{"sidera":1,"to":"claude","mode":"new_session","preset":"default","task":"hello"}`
)

// clock is the adapter's time in a test, moved by the test.
type clock struct{ ns atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.ns.Store(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *clock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *clock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// recordingInbound stands for the inbound half: it keeps what it is given.
type recordingInbound struct {
	mu  sync.Mutex
	got []Message
}

func (r *recordingInbound) HandleInbound(_ context.Context, m Message) (Verdict, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, m)
	return Written, nil
}
func (r *recordingInbound) HandleReplyCard(context.Context, kanban.Card) {}
func (r *recordingInbound) messages() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}
func (r *recordingInbound) flows() Flows { return LaneFlows(func(*Env) Inbound { return r }) }

// addCard puts a ready lane card on the fake board.
func addCard(t *testing.T, h *harness, id, body string) {
	t.Helper()
	kanbantest.Update(t, h.hermes, func(b *kanbantest.Board) {
		b.Cards[id] = &kanbantest.Card{ID: id, Title: "t " + id, Body: body, Assignee: "sidera", Status: "ready", CreatedBy: "hermes"}
	})
}

func boardCard(h *harness, id string) kanbantest.Card {
	return *kanbantest.Read(h.hermes).Cards[id]
}

// until waits for cond, which must not take the daemon's lock.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func cardState(h *harness, id string) state.Card {
	if c, ok := h.app.env.State.View().Cards[id]; ok {
		return *c
	}
	return state.Card{}
}

func commandCount(h *harness, cmd string) int {
	n := 0
	for _, c := range kanbantest.Commands(h.hermes) {
		if c.Cmd == cmd {
			n++
		}
	}
	return n
}

// serve makes the daemon hand out the message on the endpoint of key once cond
// holds. With redeliver it hands it out again after it was acknowledged, as a node
// does whose acknowledgement was lost.
func (d *daemon) serve(key string, m adapterproto.PageMessage, cond func() bool, redeliver bool) {
	d.recvAnswer = func(p adapterproto.RecvParams) adapterproto.RecvPage {
		ref, ok := d.refs[key]
		if !ok || p.Endpoint != ref || !cond() || (!redeliver && slices.Contains(d.acked(), string(m.ID))) {
			return page()
		}
		return page(m)
	}
}

func sentOnce(d *daemon) func() bool { return func() bool { return len(d.sends) > 0 } }

// A card from a source with no subscription is subscribed to the home target before
// it is completed: the subscription starts at the card's current events, so one made
// after the completion would wake nobody. The reply that completes it is written
// once, however often the node hands it out.
func TestReplyCompletesTheCardOnce(t *testing.T) {
	d := &daemon{directAccess: true}
	d.serve("orphan", message("m1", "looks good"), sentOnce(d), true)
	h := start(t, options{d: d, lane: true, flows: LaneFlows(nil)})
	addCard(t, h, "t_1", messageCard)

	until(t, "the card to be done", func() bool { return boardCard(h, "t_1").Status == "done" })
	h.eventually("a redelivery to be acknowledged", func() bool { return len(d.acks) >= 2 })

	cmds := kanbantest.Commands(h.hermes)
	index := func(name string) int {
		return slices.IndexFunc(cmds, func(c kanbantest.Call) bool { return c.Cmd == name })
	}
	if sub, done := index("notify-subscribe"), index("complete"); sub < 0 || done < 0 || sub > done {
		t.Errorf("notify-subscribe at %d, complete at %d; want the subscription first", sub, done)
	}
	if n := commandCount(h, "complete"); n != 1 {
		t.Errorf("the card was completed %d times, want once", n)
	}
	card := boardCard(h, "t_1")
	if len(card.Subs) != 1 || card.Subs[0].Platform != "feishu" || card.Subs[0].ChatID != "oc_1" {
		t.Errorf("subscriptions %+v, want the home target", card.Subs)
	}
	if card.Result != "looks good" || len(card.Runs) != 1 || card.Runs[0].Metadata["sidera_message_id"] != "m1" ||
		!strings.HasPrefix(card.Runs[0].Summary, "Sidera reply from codex") {
		t.Errorf("result %q runs %+v, want the reply with its message id and a summary written by the adapter", card.Result, card.Runs)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.sends[0]; s.IdempotencyKey != "t_1:1" || s.To[0] != "nodeb/codex" || s.AcceptBeforeS != 0 || s.Body != "review the diff" {
		t.Errorf("first send %+v, want the task to the alias with key t_1:1 and no deadline", s)
	}
}

// A reply for a card that Hermes already finished is a message of the conversation
// and goes to the inbound half, never to complete; unless it is the very reply that
// finished it, whose acknowledgement was lost.
func TestReplyForAFinishedCard(t *testing.T) {
	for _, tc := range []struct {
		name        string
		byThisReply bool
		wantInbound int
		wantAcked   bool
	}{
		{"finished by someone else", false, 1, true},
		{"finished by this reply", true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &daemon{directAccess: true}
			var release atomic.Bool
			d.serve("orphan", message("m1", "late"), func() bool { return release.Load() }, false)
			in := &recordingInbound{}
			h := start(t, options{d: d, lane: true, flows: in.flows()})
			addCard(t, h, "t_1", messageCard)
			until(t, "the card to be sent", func() bool { return cardState(h, "t_1").Phase == state.PhaseSent })

			kanbantest.Update(t, h.hermes, func(b *kanbantest.Board) {
				c := b.Cards["t_1"]
				c.Status = "done"
				if tc.byThisReply {
					c.Runs = append(c.Runs, kanbantest.Run{Outcome: "completed", Summary: "x", Metadata: map[string]any{"sidera_message_id": "m1"}})
				}
			})
			release.Store(true)
			h.eventually("the acknowledgement", func() bool { return len(d.acks) > 0 })
			if n := len(in.messages()); n != tc.wantInbound {
				t.Errorf("the inbound half got %d messages, want %d", n, tc.wantInbound)
			}
			if n := commandCount(h, "complete"); n != 0 {
				t.Errorf("complete ran %d times on a finished card", n)
			}
		})
	}
}

// An attempt resent within the window is the same message to the node, so no second
// remote session is created; one that began longer ago than the window is not sent
// again and its card is blocked as transient, for Hermes to decide.
func TestResendWindow(t *testing.T) {
	clk := newClock()
	d := &daemon{directAccess: true}
	// The node keeps every message and its answer never arrives.
	d.sendError = func(int, adapterproto.SendParams) (error, bool) {
		return peer.NewError(peer.CodeInternalError, "lost answer"), true
	}
	stateDir, hermesDir := t.TempDir(), t.TempDir()
	h := start(t, options{d: d, lane: true, flows: LaneFlows(nil), now: clk.now, stateDir: stateDir, hermesDir: hermesDir})
	addCard(t, h, "t_1", sessionCard)
	h.eventually("a resend", func() bool { return len(d.sends) >= 2 })
	d.mu.Lock()
	sends, creations := len(d.sends), d.creations
	same := d.sends[0].IdempotencyKey == d.sends[1].IdempotencyKey && d.sends[0].IdempotencyKey == "t_1:1"
	ref := d.refs["orphan"]
	d.mu.Unlock()
	if !same || creations != 1 {
		t.Errorf("resend: same key %v, %d creations; want one key, t_1:1, and one creation", same, creations)
	}
	h.stop()

	clk.advance(adapterproto.SentKeyWindow + time.Minute)
	d.mu.Lock()
	before := len(d.sends)
	d.mu.Unlock()
	_ = sends
	h2 := start(t, options{d: d, lane: true, flows: LaneFlows(nil), now: clk.now, stateDir: stateDir, hermesDir: hermesDir,
		listed: []adapterproto.DirectAttach{{Endpoint: ref, Key: "orphan"}}})
	until(t, "the card to be blocked", func() bool { return boardCard(h2, "t_1").Status == "blocked" })
	if kind := boardCard(h2, "t_1").BlockKind; kind != "transient" {
		t.Errorf("block kind %q, want transient", kind)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.sends) != before {
		t.Errorf("%d sends after the window, want the %d before: the attempt must not be sent again", len(d.sends), before)
	}
}

// A card that timed out and was blocked still takes the reply that comes late:
// Hermes completes a blocked card directly.
func TestReplyToABlockedCard(t *testing.T) {
	clk := newClock()
	d := &daemon{directAccess: true}
	var release atomic.Bool
	d.serve("orphan", message("m1", "sorry, late"), func() bool { return release.Load() }, false)
	h := start(t, options{d: d, lane: true, flows: LaneFlows(nil), now: clk.now})
	addCard(t, h, "t_1", messageCard)
	until(t, "the card to be sent", func() bool { return cardState(h, "t_1").Phase == state.PhaseSent })

	clk.advance(2 * time.Hour) // the configured wait is an hour
	until(t, "the card to be blocked", func() bool { return boardCard(h, "t_1").Status == "blocked" })
	if kind := boardCard(h, "t_1").BlockKind; kind != "needs_input" {
		t.Errorf("block kind %q, want needs_input", kind)
	}
	release.Store(true)
	until(t, "the card to be done", func() bool { return boardCard(h, "t_1").Status == "done" })
	if got := boardCard(h, "t_1").Result; got != "sorry, late" {
		t.Errorf("result %q, want the late reply", got)
	}
}

// A reply too long for a command line is attached under its message id, once, even
// when the write is repeated; a failure of Hermes that may pass leaves the message
// unacknowledged, and one that will not goes to the inbound half.
func TestLongReplyAndFailuresToComplete(t *testing.T) {
	long := strings.Repeat("0123456789", 2000)
	t.Run("transient failure retries and attaches once", func(t *testing.T) {
		d := &daemon{directAccess: true}
		var release atomic.Bool
		d.serve("orphan", message("m1", long), func() bool { return release.Load() }, false)
		h := start(t, options{d: d, lane: true, flows: LaneFlows(nil)})
		addCard(t, h, "t_1", messageCard)
		until(t, "the card to be sent", func() bool { return cardState(h, "t_1").Phase == state.PhaseSent })

		kanbantest.Fail(t, h.hermes, "complete", "transient")
		release.Store(true)
		until(t, "two tries to complete", func() bool { return commandCount(h, "complete") >= 2 })
		d.mu.Lock()
		acked := len(d.acks)
		d.mu.Unlock()
		if acked != 0 {
			t.Fatalf("the message was acknowledged while its card could not be completed")
		}
		kanbantest.Fail(t, h.hermes, "complete", "")
		until(t, "the card to be done", func() bool { return boardCard(h, "t_1").Status == "done" })
		h.eventually("the acknowledgement", func() bool { return len(d.acks) > 0 })

		card := boardCard(h, "t_1")
		if len(card.Attachments) != 1 || card.Attachments[0].Filename != "m1" || card.Attachments[0].Content != long {
			t.Errorf("attachments %d, want one named m1 holding the reply", len(card.Attachments))
		}
		if strings.Contains(card.Result, long[:100]) || !strings.Contains(card.Runs[0].Summary, "attachment m1") {
			t.Errorf("result %.60q summary %q, want a pointer to the attachment", card.Result, card.Runs[0].Summary)
		}
	})
	t.Run("permanent failure goes to the inbound half", func(t *testing.T) {
		d := &daemon{directAccess: true}
		var release atomic.Bool
		d.serve("orphan", message("m1", "answer"), func() bool { return release.Load() }, false)
		in := &recordingInbound{}
		h := start(t, options{d: d, lane: true, flows: in.flows()})
		addCard(t, h, "t_1", messageCard)
		until(t, "the card to be sent", func() bool { return cardState(h, "t_1").Phase == state.PhaseSent })

		kanbantest.Fail(t, h.hermes, "complete", "permanent")
		release.Store(true)
		until(t, "the message to reach the inbound half", func() bool { return len(in.messages()) == 1 })
		if got := cardState(h, "t_1").Phase; got != state.PhaseFinished {
			t.Errorf("phase %q, want the card finished here as Hermes will not take it", got)
		}
	})
}

// A new session: the signal carries the preset and the task, the control reply names
// the session, its replies complete the card, and the session is closed from the
// endpoint that created it once it has been idle for the close window.
func TestNewSessionLifecycle(t *testing.T) {
	clk := newClock()
	d := &daemon{directAccess: true}
	d.serve("orphan", adapterproto.PageMessage{ID: "m1", From: "nodeb/sess-1", Body: "done"}, sentOnce(d), false)
	h := start(t, options{d: d, lane: true, flows: LaneFlows(nil), now: clk.now})
	addCard(t, h, "t_1", sessionCard)

	until(t, "the card to be done", func() bool { return boardCard(h, "t_1").Status == "done" })
	d.mu.Lock()
	first := d.sends[0]
	d.mu.Unlock()
	if first.Control == nil || first.Control.Signal != adapterproto.SignalSessionCreate || first.Control.Params.Preset != "default" ||
		first.To[0] != "nodeb/claude" || first.Body != "hello" {
		t.Errorf("first send %+v, want session.create for the preset to the alias with the task", first)
	}
	if _, open := h.app.env.State.View().Sessions["nodeb/sess-1"]; !open {
		t.Fatal("the session the control reply named is not recorded as open")
	}

	// The session counts as used when the adapter records the card finished.
	until(t, "the card to be finished here", func() bool { return cardState(h, "t_1").Phase == state.PhaseFinished })
	clk.advance(2 * time.Hour)
	until(t, "the session to be forgotten", func() bool { return len(h.app.env.State.View().Sessions) == 0 })
	d.mu.Lock()
	defer d.mu.Unlock()
	closing := d.sends[len(d.sends)-1]
	if closing.Control == nil || closing.Control.Signal != adapterproto.SignalSessionClose || closing.To[0] != "nodeb/sess-1" ||
		closing.Endpoint != d.refs["orphan"] {
		t.Errorf("last send %+v, want session.close to the session from the endpoint that created it", closing)
	}
}
