package adapter

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban/kanbantest"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// step is a message the daemon hands out once cond holds.
type step struct {
	m    adapterproto.PageMessage
	cond func() bool
}

// serveInOrder hands out the steps one after the other on the endpoint of key: a
// step comes only after the one before it was acknowledged.
func (d *daemon) serveInOrder(key string, steps ...step) {
	d.recvAnswer = func(p adapterproto.RecvParams) adapterproto.RecvPage {
		ref, ok := d.refs[key]
		if !ok || p.Endpoint != ref {
			return page()
		}
		for _, s := range steps {
			if slices.Contains(d.acked(), string(s.m.ID)) {
				continue
			}
			if s.cond() {
				return page(s.m)
			}
			break
		}
		return page()
	}
}

// notice is a notice of the node about the endpoint, as a page carries it.
func notice(id, from, body string) adapterproto.PageMessage {
	return adapterproto.PageMessage{ID: adapterproto.MessageID(id), From: adapterproto.Address(from), Body: body, Feedback: true}
}

const systemSender = "node-1/system"

// The node's notice ends the cards that sent from the endpoint and whose message the
// node says did not arrive, and is acknowledged only once the card is written on the
// board. Nothing else is made of it: the card was the explanation.
func TestNoticeSettlesTheCardsInFlight(t *testing.T) {
	var armed, released atomic.Bool
	d := &daemon{directAccess: true}
	d.statusAnswer = func(sent adapterproto.SendParams) adapterproto.MessageStatusResult {
		if !armed.Load() {
			return adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{{To: sent.To[0], State: adapterproto.DeliveryAccepted}}}
		}
		return adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{
			{To: sent.To[0], State: adapterproto.DeliveryRejected, Reason: adapterproto.RejectEndpointMissing}}}
	}
	d.serveInOrder("orphan", step{notice("n1", systemSender, "Your message was refused."), released.Load})
	h := start(t, options{d: d, lane: true, flows: LaneFlows(NewInbound), tick: time.Hour})
	addCard(t, h, "t_1", messageCard)
	until(t, "the card to be sent", func() bool { return cardState(h, "t_1").Phase == state.PhaseSent })

	kanbantest.Fail(t, h.hermes, "block", "transient")
	armed.Store(true)
	released.Store(true)
	until(t, "two tries to block the card", func() bool { return commandCount(h, "block") >= 2 })
	d.mu.Lock()
	acked := len(d.acks)
	d.mu.Unlock()
	if acked != 0 {
		t.Fatal("the notice was acknowledged while the card it settles could not be written")
	}
	kanbantest.Fail(t, h.hermes, "block", "")
	h.eventually("the acknowledgement", func() bool { return len(d.acks) > 0 })

	if c := boardCard(h, "t_1"); c.Status != "blocked" || c.BlockKind != "transient" {
		t.Errorf("card status %q kind %q, want blocked as a failed delivery", c.Status, c.BlockKind)
	}
	if n := len(inboxCards(h)); n != 0 {
		t.Errorf("%d cards made of a notice that a card explained, want none", n)
	}
}

// The failure of an answer has no card to land on, because the reply card was
// completed when the answer left: it becomes a card of the conversation, once.
func TestNoticeReportsAnAnswerThatDidNotArrive(t *testing.T) {
	var armed, released, second atomic.Bool
	d := &daemon{directAccess: true}
	d.statusAnswer = func(sent adapterproto.SendParams) adapterproto.MessageStatusResult {
		res := defaultStatus(sent)
		if armed.Load() && sent.ReplyTo == "m1" {
			res.Recipients[0].State, res.Recipients[0].Reason = adapterproto.DeliveryRejected, adapterproto.RejectEndpointMissing
		}
		return res
	}
	d.serveInOrder(InstanceKey,
		step{message("m1", "please review"), ready},
		step{notice("n1", systemSender, "Your message was refused."), released.Load},
		step{notice("n2", systemSender, "Your message was refused."), second.Load})
	h := start(t, options{d: d, lane: true, flows: LaneFlows(NewInbound)})
	until(t, "the request card", func() bool { return len(inboxCards(h)) == 1 })
	request := inboxCards(h)[0]
	kanbantest.Update(t, h.hermes, func(b *kanbantest.Board) {
		b.Cards["r_1"] = &kanbantest.Card{ID: "r_1", Title: "answer", Body: replyCard, Assignee: "sidera", Status: "ready", CreatedBy: "hermes"}
	})
	until(t, "the answer to be sent", func() bool { return boardCard(h, "r_1").Status == "done" })

	armed.Store(true)
	released.Store(true)
	h.eventually("the first notice to be acknowledged", func() bool { return slices.Contains(d.acked(), "n1") })
	second.Store(true)
	h.eventually("the second notice to be acknowledged", func() bool { return slices.Contains(d.acked(), "n2") })

	var lost, relayed []kanbantest.Card
	for _, c := range inboxCards(h) {
		switch c.Title {
		case titleAnswerLost:
			lost = append(lost, c)
		case titleNotice:
			relayed = append(relayed, c)
		}
	}
	if len(lost) != 1 {
		t.Fatalf("%d cards report the lost answer, want 1: %+v", len(lost), inboxCards(h))
	}
	c := lost[0]
	first, _, _ := strings.Cut(c.Runs[0].Summary, "\n")
	if c.Status != "done" || len(c.Subs) != 1 || c.Subs[0].ChatID != "oc_1" ||
		!strings.Contains(first, "request card "+request.ID) || !strings.Contains(first, "codex") ||
		!strings.Contains(first, "rejected (endpoint_missing)") {
		t.Errorf("card %+v, want a completed card for the home target whose wake line names the request, the sender and the state", c)
	}
	// The second notice is about the same failure, already reported: nothing explains
	// it, so it is relayed as it stands.
	if len(relayed) != 1 {
		t.Errorf("%d relayed notices, want 1 (the second)", len(relayed))
	}
}

// A notice that nothing explains is relayed to the conversation of the endpoint as a
// card that says it is the node's own, once however often it is handed out. It is
// never matched as the reply of a card that waits for its sender, and never turned
// into an untrusted request.
func TestNoticeThatNothingExplainsIsRelayed(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		from string
		card bool
	}{
		{"nothing in flight", InstanceKey, systemSender, false},
		{"sent by the peer a card waits for", "orphan", "nodeb/codex", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &daemon{directAccess: true}
			n := notice("n1", tc.from, "A recipient is blocked ``` for now.")
			d.serve(tc.key, n, func() bool { return !tc.card || len(d.sends) > 0 }, true)
			h := start(t, options{d: d, lane: true, flows: LaneFlows(NewInbound), tick: time.Hour})
			if tc.card {
				addCard(t, h, "t_1", messageCard)
			}
			h.eventually("a redelivery to be acknowledged", func() bool { return len(d.acks) >= 2 })

			cards := inboxCards(h)
			if len(cards) != 1 {
				t.Fatalf("%d cards, want 1: %+v", len(cards), cards)
			}
			c := cards[0]
			first, _, _ := strings.Cut(c.Runs[0].Summary, "\n")
			if c.Title != titleNotice || c.Status != "done" || len(c.Subs) != 1 || c.Subs[0].ChatID != "oc_1" ||
				!strings.HasPrefix(first, titleNotice) || !strings.Contains(c.Body, "not external content") ||
				!strings.Contains(c.Body, "never with a reply card") || !strings.Contains(c.Body, "````\n"+n.Body+"\n````") {
				t.Errorf("card %+v, want the node's own notice, completed, with the text fenced", c)
			}
			if tc.card {
				if got := boardCard(h, "t_1"); got.Status == "done" || len(got.Runs) != 0 {
					t.Errorf("the card waiting for the peer was %q with %d runs, want it untouched", got.Status, len(got.Runs))
				}
			}
		})
	}
}
