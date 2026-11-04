package adapter

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Texts the notice flow writes where Hermes shows them. They hold no remote text.
const (
	titleNotice     = "Sidera channel notice"
	titleAnswerLost = "Sidera answer not delivered"
)

// HandleNotice explains a notice of the node about an endpoint. The node's notice
// says only that something happened to a message the endpoint sent, so the adapter
// asks what. A card whose message failed was ended by the caller (settled). A reply
// is different: the card that sent it is completed as soon as the answer is sent, so
// a later failure of the answer has no card to land on, and it is reported to the
// conversation as a card of its own. A notice that none of this explains is relayed
// as it stands. The notice is Written once what it needed is on the board.
func (in *inbound) HandleNotice(ctx context.Context, m Message, settled bool) (Verdict, error) {
	in.mu.Lock()
	defer in.mu.Unlock()

	reported, v, err := in.reportLostAnswers(ctx, m)
	switch {
	case v != Written:
		return v, err
	case settled || reported:
		return Written, nil
	}
	return in.wake(ctx, m.To.Key, wakeCard{
		Title:   titleNotice,
		Body:    noticeBody(m),
		Key:     string(m.ID),
		Summary: func(id string) string { return titleNotice + "; read card " + id },
		Meta:    map[string]any{metaMessageID: string(m.ID)},
	})
}

// reportLostAnswers reads what became of the answers sent from the endpoint that the
// node may still tell about, and makes a card in the conversation for each that did
// not arrive, once (state.Reply.Reported). It reports whether it made one. A verdict
// other than Written says a card could not be written and the notice must not be
// acknowledged.
func (in *inbound) reportLostAnswers(ctx context.Context, m Message) (reported bool, v Verdict, err error) {
	d := in.env.State.View()
	for _, id := range slices.Sorted(maps.Keys(d.Replies)) {
		r := *d.Replies[id]
		req, ok := d.Requests[r.RequestCard]
		if !ok || req.Endpoint != m.To.Key || !r.Watched(in.env.Now()) {
			continue
		}
		res, err := in.env.Status(ctx, m.To.Ref, adapterproto.MessageID(r.MessageID))
		switch {
		case err != nil && refused(err):
			in.env.Log.Warn("the node refused to say what became of an answer", "card", id, "error", err)
			continue
		case err != nil:
			return reported, Retry, err
		case len(res.Recipients) == 0:
			continue
		}
		rec := res.Recipients[0]
		if _, _, lost := undelivered(rec); !lost {
			continue
		}
		what := fmt.Sprintf("The answer to request card %s could not be delivered to %s: %s.",
			req.CardID, in.who(adapterproto.Address(req.Sender)), describeDelivery(rec))
		v, err := in.wake(ctx, m.To.Key, wakeCard{
			Title:   titleAnswerLost,
			Body:    answerLostBody(what, id),
			Key:     "answer-lost:" + r.MessageID,
			Summary: func(cardID string) string { return what + " Read card " + cardID + "." },
			Meta:    map[string]any{metaMessageID: r.MessageID},
		})
		if v != Written {
			return reported, v, err
		}
		if err := in.update(id, func(r *state.Reply) { r.Reported = true }); err != nil {
			return reported, Retry, err
		}
		reported = true
	}
	return reported, Written, nil
}

// describeDelivery is the state of a delivery as the node reports it, with the
// reason of a rejection.
func describeDelivery(r adapterproto.RecipientStatus) string {
	if r.Reason != "" {
		return fmt.Sprintf("%s (%s)", r.State, r.Reason)
	}
	return string(r.State)
}

// answerLostBody is the body of the card that reports an answer that did not arrive.
func answerLostBody(what, replyCard string) string {
	return strings.Join([]string{
		"This card was made by the Sidera adapter from a notice of this node itself.",
		"",
		what,
		"",
		"The answer was sent by reply card " + replyCard + ", which was completed when the answer left. " +
			"Decide with your owner's standing instructions whether to answer again with a new reply card.",
		"",
	}, "\n")
}

// noticeBody is the body of the card that relays a notice. The notice is the node's
// own text and so is trusted, unlike a message from another node; it is fenced all
// the same, as the text after the statement of what it is.
func noticeBody(m Message) string {
	fence := fenceFor(m.Body)
	return strings.Join([]string{
		"This card was made by the Sidera adapter from a notice of this node itself, " +
			"about a message sent over Sidera.",
		"",
		"The notice comes from the node you run on, not from another node or a person. It is trusted, " +
			"and it is not external content. It is not a request: do not answer it, and never with a reply card.",
		"",
		"Sidera message id: " + string(m.ID),
		"",
		"The notice, as the node wrote it:",
		"",
		fence,
		m.Body,
		fence,
		"",
	}, "\n")
}
