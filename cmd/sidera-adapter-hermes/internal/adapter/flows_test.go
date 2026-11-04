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
func (r *recordingInbound) HandleNotice(_ context.Context, m Message, _ bool) (Verdict, error) {
	return r.HandleInbound(context.Background(), m)
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
	if len(card.Attachments) != 1 || card.Attachments[0].Content != "looks good" || len(card.Runs) != 1 ||
		card.Runs[0].Metadata["sidera_message_id"] != "m1" || !strings.HasPrefix(card.Runs[0].Summary, "Sidera reply from codex") {
		t.Errorf("attachments %+v runs %+v, want the reply attached, with its message id and a summary written by the adapter",
			card.Attachments, card.Runs)
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

// A crash after Hermes took the completion of a reply and before the adapter
// recorded the message as processed: the next start finds the card done and ends it
// in the record, and the node hands the reply out again. It must not become a card
// of its own that wakes the conversation a second time. The record names the reply
// before the completion, so the message is recognised whichever of recovery and
// receiving comes first.
func TestRedeliveredReplyOfAFinishedCardIsNotANewMessage(t *testing.T) {
	d := &daemon{directAccess: true}
	var release atomic.Bool
	d.serve("orphan", message("m1", "the answer"), func() bool { return release.Load() }, false)
	stateDir, hermesDir := t.TempDir(), t.TempDir()
	h := start(t, options{d: d, lane: true, flows: LaneFlows(nil), stateDir: stateDir, hermesDir: hermesDir})
	addCard(t, h, "t_1", messageCard)
	until(t, "the card to be sent", func() bool { return cardState(h, "t_1").Phase == state.PhaseSent })

	// The crash: the reply was named on the record, Hermes took the completion.
	if err := h.app.env.State.Update(func(d *state.Data) error { d.Cards["t_1"].ReplyID = "m1"; return nil }); err != nil {
		t.Fatal(err)
	}
	kanbantest.Update(t, h.hermes, func(b *kanbantest.Board) {
		b.Cards["t_1"].Status = "done"
		b.Cards["t_1"].Runs = append(b.Cards["t_1"].Runs, kanbantest.Run{Outcome: "completed", Summary: "x", Metadata: map[string]any{"sidera_message_id": "m1"}})
	})
	h.stop()
	d.mu.Lock()
	ref := d.refs["orphan"]
	d.mu.Unlock()

	in := &recordingInbound{}
	h2 := start(t, options{d: d, lane: true, flows: in.flows(), stateDir: stateDir, hermesDir: hermesDir,
		listed: []adapterproto.DirectAttach{{Endpoint: ref, Key: "orphan"}}})
	until(t, "recovery to end the card in the record", func() bool { return cardState(h2, "t_1").Phase == state.PhaseFinished })
	release.Store(true)
	h2.eventually("the acknowledgement", func() bool { return slices.Contains(d.acked(), "m1") })
	if n := len(in.messages()); n != 0 {
		t.Errorf("the inbound half got %d messages, want none: the reply was written already", n)
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
	if atts := boardCard(h, "t_1").Attachments; len(atts) != 1 || atts[0].Content != "sorry, late" {
		t.Errorf("attachments %+v, want the late reply", atts)
	}
}

// The reply of a peer is text from another machine. Hermes uploads to the chat of
// the card's creator every local file that the summary or the result of a completed
// card names, so a reply, and the id the sender gave its message, reach the card as
// an attachment only: the summary and the result hold the adapter's words, and
// neither a path nor a NUL byte gets near a command line. The reply is attached
// once however often the node hands it out.
func TestReplyReachesTheCardOnlyAsAnAttachment(t *testing.T) {
	const id = "/home/owner/contracts/2026.pdf"
	body := "see ~/notes/passwords.txt and /etc/hosts.txt\x00and then " + strings.Repeat("0123456789", 2000)
	d := &daemon{directAccess: true}
	d.serve("orphan", message(id, body), sentOnce(d), true)
	h := start(t, options{d: d, lane: true, flows: LaneFlows(nil)})
	addCard(t, h, "t_1", messageCard)

	until(t, "the card to be done", func() bool { return boardCard(h, "t_1").Status == "done" })
	h.eventually("a redelivery to be acknowledged", func() bool { return len(d.acks) >= 2 })

	card := boardCard(h, "t_1")
	if len(card.Attachments) != 1 || card.Attachments[0].Content != body {
		t.Fatalf("%d attachments, want one holding the reply", len(card.Attachments))
	}
	name := card.Attachments[0].Filename
	want := "The reply of codex is attached as " + name + "."
	if card.Result != want || len(card.Runs) != 1 || !strings.Contains(card.Runs[0].Summary, name) {
		t.Errorf("result %q summary %q, want %q and a summary naming the attachment", card.Result, card.Runs[0].Summary, want)
	}
	for _, text := range []string{name, card.Result, card.Runs[0].Summary} {
		if strings.ContainsAny(text, "/~\\") {
			t.Errorf("%q can name a path", text)
		}
	}
}

// A failure of Hermes while writing a reply that may pass leaves the message
// unacknowledged; one that will not goes to the inbound half.
func TestFailuresToCompleteAReply(t *testing.T) {
	t.Run("transient failure retries and attaches once", func(t *testing.T) {
		d := &daemon{directAccess: true}
		var release atomic.Bool
		d.serve("orphan", message("m1", "answer"), func() bool { return release.Load() }, false)
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

		if atts := boardCard(h, "t_1").Attachments; len(atts) != 1 || atts[0].Content != "answer" {
			t.Errorf("attachments %+v, want one holding the reply", atts)
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

// A cron job's card is subscribed to the home target by the adapter. Planning it
// again (it was unblocked after a failed delivery) must not take that subscription
// for the card's own source and move the card to the home chat's endpoint: the
// remote session would be kept as long as a conversation's and a late reply would
// arrive at an endpoint the first attempt never used.
func TestCardKeepsItsSourceWhenPlannedAgain(t *testing.T) {
	d := &daemon{directAccess: true}
	d.sendError = func(n int, _ adapterproto.SendParams) (error, bool) {
		if n == 1 {
			return peer.NewError(adapterproto.CodeSendRefused, "refused").WithData(map[string]any{"reason": "too_large"}), false
		}
		return nil, true
	}
	h := start(t, options{d: d, lane: true, flows: LaneFlows(nil)})
	kanbantest.Update(t, h.hermes, func(b *kanbantest.Board) {
		b.Cards["t_1"] = &kanbantest.Card{ID: "t_1", Title: "t", Body: sessionCard, Assignee: "sidera", Status: "ready",
			CreatedBy: "hermes", SessionID: "cron_ab12cd_20261104_120000"}
	})
	until(t, "the card to be blocked", func() bool { return boardCard(h, "t_1").Status == "blocked" })
	kanbantest.Update(t, h.hermes, func(b *kanbantest.Board) { b.Cards["t_1"].Status = "ready" })
	until(t, "the card to be sent", func() bool { return cardState(h, "t_1").Phase == state.PhaseSent })

	const want = "cron:default:ab12cd"
	if got := cardState(h, "t_1").Endpoint; got != want {
		t.Errorf("the card sends from %q after being planned again, want %q", got, want)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.sends) != 2 || d.sends[0].Endpoint != d.sends[1].Endpoint || d.sends[1].Endpoint != d.refs[want] {
		t.Errorf("sends %+v, want both from the endpoint of the cron job", d.sends)
	}
}

// A blocked card that its creator completes instead of unblocking, as the Skill
// allows, is ended in the record within minutes, and the remote session it held is
// then closed in its turn: nothing else would tell the adapter.
func TestBlockedCardCompletedByHermesFreesItsSession(t *testing.T) {
	clk := newClock()
	d := &daemon{directAccess: true}
	h := start(t, options{d: d, lane: true, flows: LaneFlows(nil), now: clk.now})
	addCard(t, h, "t_1", sessionCard)
	until(t, "the session to be recorded", func() bool { return len(h.app.env.State.View().Sessions) == 1 })

	clk.advance(2 * time.Hour) // the configured wait is an hour
	until(t, "the card to be blocked", func() bool { return boardCard(h, "t_1").Status == "blocked" })
	kanbantest.Update(t, h.hermes, func(b *kanbantest.Board) { b.Cards["t_1"].Status = "done" })
	clk.advance(blockedCheckInterval + time.Minute)
	until(t, "the card to be finished in the record", func() bool { return cardState(h, "t_1").Phase == state.PhaseFinished })

	clk.advance(2 * time.Hour) // the close window of an orphan card's session is an hour
	until(t, "the session to be closed", func() bool { return len(h.app.env.State.View().Sessions) == 0 })
	d.mu.Lock()
	defer d.mu.Unlock()
	if last := d.sends[len(d.sends)-1]; last.Control == nil || last.Control.Signal != adapterproto.SignalSessionClose {
		t.Errorf("last send %+v, want session.close", last)
	}
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

// A crash between claiming a card and writing the rest of its record leaves the
// card running under the adapter's claim, and the poll lists only ready cards. The
// record written before the claim is how the next start finds it: a card that
// shows running is adopted and sent, one still ready is claimed and sent as any
// new card.
func TestCardClaimedBeforeACrashIsFoundAgain(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		wantClaims int
	}{
		{"the claim was made", "running", 0},
		{"the claim was not made", "ready", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, hermesDir := t.TempDir(), t.TempDir()
			store, err := state.Open(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Update(func(d *state.Data) error {
				d.Cards["t_1"] = &state.Card{ID: "t_1", Phase: state.PhaseClaiming, ClaimedAt: newClock().now()}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			kanbantest.Update(t, hermesDir, func(b *kanbantest.Board) {
				b.Cards["t_1"] = &kanbantest.Card{ID: "t_1", Title: "t", Body: messageCard, Assignee: "sidera", Status: tc.status, CreatedBy: "hermes"}
			})

			d := &daemon{directAccess: true}
			h := start(t, options{d: d, lane: true, flows: LaneFlows(nil), stateDir: stateDir, hermesDir: hermesDir})
			until(t, "the card to be sent", func() bool { return cardState(h, "t_1").Phase == state.PhaseSent })
			if n := commandCount(h, "claim"); n != tc.wantClaims {
				t.Errorf("the card was claimed %d times, want %d", n, tc.wantClaims)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if len(d.sends) != 1 || d.sends[0].IdempotencyKey != "t_1:1" {
				t.Errorf("sends %+v, want the task once with the key t_1:1", d.sends)
			}
		})
	}
}

// A reply card the adapter blocked and its creator then completed instead of
// unblocking is ended in the record within minutes, and its request no longer counts
// as unanswered: the endpoint that received it is free to be retired.
func TestBlockedReplyCardCompletedByHermesAnswersItsRequest(t *testing.T) {
	clk := newClock()
	d := &daemon{directAccess: true}
	d.sendError = func(int, adapterproto.SendParams) (error, bool) {
		return peer.NewError(adapterproto.CodeSendRefused, "refused").WithData(map[string]any{"reason": "too_large"}), false
	}
	d.serve("hermes", message("m1", "please review"), ready, false)
	h := start(t, options{d: d, lane: true, flows: LaneFlows(NewInbound), now: clk.now})
	until(t, "the request card", func() bool { return len(inboxCards(h)) == 1 })
	kanbantest.Update(t, h.hermes, func(b *kanbantest.Board) {
		b.Cards["r_1"] = &kanbantest.Card{ID: "r_1", Title: "answer", Body: replyCard, Assignee: "sidera", Status: "ready", CreatedBy: "hermes"}
	})
	// The adapter records the block after Hermes has taken it, so the board shows
	// the card blocked before the record does.
	until(t, "the reply card to be blocked in the record", func() bool {
		r := h.app.env.State.View().Replies["r_1"]
		return r != nil && r.Phase == state.PhaseBlocked
	})
	if r := h.app.env.State.View().Replies["r_1"]; r.Answered() {
		t.Fatalf("reply record %+v, want its request unanswered", r)
	}

	kanbantest.Update(t, h.hermes, func(b *kanbantest.Board) { b.Cards["r_1"].Status = "done" })
	clk.advance(blockedCheckInterval + time.Minute)
	until(t, "the reply card to be finished in the record", func() bool {
		r := h.app.env.State.View().Replies["r_1"]
		return r != nil && r.Phase == state.PhaseFinished && r.Answered()
	})
}
