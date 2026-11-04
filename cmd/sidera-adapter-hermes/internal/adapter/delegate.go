package adapter

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/card"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/lane"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// cronSession is the session id of a card made by a cron job: cron_<job>_<date>_<time>.
var cronSession = regexp.MustCompile(`^cron_([0-9a-f]+)_\d{8}_\d{6}$`)

// HandleCard takes a card the poll found ready. A card the adapter does not know is
// validated and delegated; one it knows is ready again because it was unblocked or
// its claim lapsed, and lane.OnReady says what that means.
func (f *laneFlows) HandleCard(ctx context.Context, c kanban.Card) {
	if f.handleCard(ctx, c) {
		f.inbound.HandleReplyCard(ctx, c)
	}
}

// handleCard reports whether the card is a reply card, which is the inbound half's.
func (f *laneFlows) handleCard(ctx context.Context, c kanban.Card) (reply bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.env.State.View()
	rec := d.Cards[c.ID]
	if rec != nil && rec.Mode == card.ModeReply {
		return true
	}
	act := lane.OnReady(rec, f.env.Now())
	switch act.Kind {
	case lane.ActionIgnore:
	case lane.ActionNew, lane.ActionRevalidate, lane.ActionSend:
		return f.take(ctx, c.ID)
	case lane.ActionKeepWaiting:
		if !f.claim(ctx, c.ID) {
			return false
		}
		if act.ResetTimer {
			_ = f.update(c.ID, func(_ *state.Data, r *state.Card) { r.Sent(r.MessageID, f.env.Now()) })
		}
	case lane.ActionOutcomeUnknown:
		if f.claim(ctx, c.ID) {
			f.fail(ctx, c.ID, lane.ReasonOutcomeUnknown, "The adapter cannot tell whether the task was sent; it was too long ago to send it again.")
		}
	}
	return false
}

// claim takes the card for the configured claim time. It reports whether the card
// is the adapter's now; a card someone else took or whose state changed is left.
func (f *laneFlows) claim(ctx context.Context, id string) bool {
	if err := f.env.Board.Claim(ctx, id, f.env.Config.ClaimTTL); err != nil {
		f.env.Log.Warn("claiming a card", "card", id, "error", err)
		return false
	}
	return true
}

// plan is what is learned about a card before it is claimed. Everything in it is
// read or idempotent, so a card that fails here is simply listed again.
type plan struct {
	card    kanban.Card
	parents []string
	reply   bool
	// failure, when set, is why the card cannot go on; the other fields are then
	// only as far filled in as the failure allows.
	failure   *failure
	mode      string
	alias     string
	endpoint  string
	placeNote string
	peer      string
	session   bool
}

type failure struct {
	reason lane.Reason
	text   string
}

// take validates a card the adapter does not hold (or holds blocked), places it,
// claims it, records it and starts it. It reports whether the card is a reply card.
//
// The claim comes after everything that only reads or can be repeated: a card
// claimed and then left on a transient failure would stay claimed, unrecorded, for
// the whole claim time.
func (f *laneFlows) take(ctx context.Context, id string) (reply bool) {
	p, err := f.plan(ctx, id)
	if err != nil {
		if !isPermanent(err) {
			f.env.Log.Warn("preparing a card; it is looked at again", "card", id, "error", err)
		}
		return false
	}
	if p.reply {
		return true
	}
	if !f.claim(ctx, id) {
		return false
	}
	f.adopt(id, p)
	if p.placeNote != "" {
		f.comment(ctx, id, p.placeNote)
	}
	if p.failure != nil {
		f.fail(ctx, id, p.failure.reason, p.failure.text)
		return false
	}
	f.advance(ctx, id)
	return false
}

// plan reads the card and decides where it goes. The returned error is a failure of
// Hermes or of the daemon; a card that cannot be served is a plan with a failure.
func (f *laneFlows) plan(ctx context.Context, id string) (*plan, error) {
	detail, err := f.env.Board.Show(ctx, id)
	if err != nil {
		return nil, err
	}
	p := &plan{card: detail.Card, parents: detail.Parents}
	req, inv := f.request(detail)
	if inv != nil {
		p.failure = &failure{lane.ReasonInvalidCard, inv.Reason}
		return p, nil
	}
	if req.Mode == card.ModeReply {
		p.reply = true
		return p, nil
	}
	p.mode, p.alias, p.peer = req.Mode, req.Alias, string(req.Address)
	if req.Mode == card.ModeNewSession {
		p.peer = ""
	}

	d := f.env.State.View()
	var route lane.Route
	if req.Alias == "" {
		var parents []state.Card
		for _, pid := range detail.Parents {
			if rec, ok := d.Cards[pid]; ok {
				parents = append(parents, *rec)
			}
		}
		r, reason, ok := lane.RouteFollowUp(parents, d.SessionList())
		if !ok {
			p.failure = &failure{reason, followUpText(reason)}
			return p, nil
		}
		route = r
		p.alias, p.peer, p.session = r.Alias, r.Peer, r.Session
	}

	src, target, err := f.source(ctx, id, detail.Card)
	if err != nil {
		return nil, err
	}
	if _, ok := d.Endpoints[route.Endpoint]; route.Endpoint != "" && ok {
		p.endpoint = route.Endpoint
		return p, nil
	}
	if route.Session {
		// A session answers only the endpoint that created it, and that one is gone.
		p.failure = &failure{lane.ReasonSessionClosed, followUpText(lane.ReasonSessionClosed)}
		return p, nil
	}
	placed, err := f.env.Place(ctx, src, target)
	if err != nil {
		return nil, err
	}
	p.endpoint, p.placeNote = placed.Key, placed.Reason
	return p, nil
}

func followUpText(r lane.Reason) string {
	if r == lane.ReasonSessionClosed {
		return "The remote session of the earlier card is closed. Create a new_session card to start another one."
	}
	return `This follow-up has no "to" and none of its parent cards was sent to a peer.`
}

// request reads and validates the envelope of a card against the alias table.
func (f *laneFlows) request(d *kanban.Detail) (card.Request, *card.InvalidError) {
	env, err := card.Decode(d.Card.Body)
	if err == nil && env.Mode == card.ModeReply {
		// A reply card is the inbound half's, which validates it.
		return card.Request{Mode: card.ModeReply}, nil
	}
	if err == nil {
		err = card.CheckCreator(f.env.Config.CreatorProfiles, d.Card.CreatedBy)
	}
	var req card.Request
	if err == nil {
		req, err = env.Validate(f.env.Config.Aliases, len(d.Parents) > 0)
	}
	if err != nil {
		// Decode, CheckCreator and Validate return nothing else.
		return card.Request{}, err.(*card.InvalidError)
	}
	return req, nil
}

// source finds the conversation or job a card comes from and makes sure its
// completion will wake someone. A card with no subscription (a cron job's, or one
// made outside a chat) is subscribed to the home target here, before the card can
// be completed: the subscription starts caught up on the card's events, so one made
// after the completion would never wake anybody.
func (f *laneFlows) source(ctx context.Context, id string, c kanban.Card) (lane.Source, config.Target, error) {
	subs, err := f.env.Board.NotifyList(ctx, id)
	if err != nil {
		return lane.Source{}, config.Target{}, err
	}
	if len(subs) > 0 {
		s := subs[0]
		meta := func(key string) string { v, _ := s.DeliveryMetadata[key].(string); return v }
		target := config.Target{Profile: s.NotifierProfile, Platform: s.Platform, ChatID: s.ChatID, ThreadID: s.ThreadID,
			ChatType: s.ChatType, UserID: s.UserID, UserIDAlt: s.UserIDAlt,
			ParentChatID: meta("parent_chat_id"), GuildID: meta("guild_id")}
		return lane.Source{Profile: s.NotifierProfile, Platform: s.Platform, ChatID: s.ChatID, ThreadID: s.ThreadID}, target, nil
	}
	home := f.env.Config.Home
	err = f.env.Board.NotifySubscribe(ctx, id, kanban.Subscription{
		Platform: home.Platform, ChatID: home.ChatID, ThreadID: home.ThreadID, UserID: home.UserID,
		UserIDAlt: home.UserIDAlt, ChatType: home.ChatType, ParentChatID: home.ParentChatID, GuildID: home.GuildID,
		NotifierProfile: home.Profile, DeliveryMode: kanban.DeliveryNotifyWake,
	})
	if err != nil {
		return lane.Source{}, config.Target{}, err
	}
	src := lane.Source{Profile: home.Profile}
	if m := cronSession.FindStringSubmatch(c.SessionID); m != nil {
		src.CronJobID = m[1]
	}
	return src, home, nil
}

// adopt writes the record of a claimed card. A card the adapter knew keeps its
// attempt number and its counts of blocks, which is what keeps a retried card from
// being blocked a second time for the same cause.
func (f *laneFlows) adopt(id string, p *plan) {
	err := f.env.State.Update(func(d *state.Data) error {
		c, ok := d.Cards[id]
		if !ok {
			c = &state.Card{ID: id, Phase: state.PhaseNew, ClaimedAt: f.env.Now()}
			d.Cards[id] = c
		}
		c.Mode, c.Alias, c.Endpoint, c.Peer, c.Session = p.mode, p.alias, p.endpoint, p.peer, p.session
		c.Parent = ""
		if len(p.parents) > 0 {
			c.Parent = p.parents[0]
		}
		if c.Phase == state.PhaseBlocked {
			c.Unblock()
		}
		return nil
	})
	if err != nil {
		f.env.Log.Error("recording a claimed card", "card", id, "error", err)
	}
}

// advance moves a claimed card on as far as it can go without waiting for an
// event: send it when it is its turn, send again what an attempt left undone, ask
// what became of what was sent, block it when the peer has been silent too long.
func (f *laneFlows) advance(ctx context.Context, id string) {
	if ctx.Err() != nil {
		return
	}
	d := f.env.State.View()
	c, ok := d.Cards[id]
	if !ok {
		return
	}
	now := f.env.Now()
	switch c.Phase {
	case state.PhaseNew:
		if lane.ShouldQueue(d.CardList(), *c) {
			if !f.queued[id] {
				f.queued[id] = true
				f.comment(ctx, id, commentQueued)
			}
			return
		}
		f.send(ctx, id, c.Attempt+1)
	case state.PhaseAttempting:
		switch act := lane.OnReady(c, now); act.Kind {
		case lane.ActionSend:
			f.send(ctx, id, act.Attempt)
		case lane.ActionOutcomeUnknown:
			f.fail(ctx, id, lane.ReasonOutcomeUnknown, "The adapter cannot tell whether the task was sent; it was too long ago to send it again.")
		}
	case state.PhaseSent:
		if !c.Delivered {
			if err := f.settle(ctx, *c); err != nil {
				f.env.Log.Warn("reading what became of a message", "card", id, "error", err)
			}
		}
		// The status may have failed the card; look again before judging the wait.
		if c, ok = f.env.State.View().Cards[id]; ok && lane.NoReplyDue(*c, now, f.env.Config.NoReplyTimeout) {
			f.fail(ctx, id, lane.ReasonNoReply, "No reply from the peer after "+f.env.Config.NoReplyTimeout.String()+".")
		}
	}
}

// send sends attempt number attempt of the card. A new attempt number is written
// down first, together with its start, because the idempotency key is built from it
// and recovery decides from it whether the same message may still be sent: written
// after the send, a crash in between would send the next time under a new key and
// could create a second remote session.
func (f *laneFlows) send(ctx context.Context, id string, attempt int) {
	d := f.env.State.View()
	c := d.Cards[id]
	detail, err := f.env.Board.Show(ctx, id)
	if err != nil {
		if isPermanent(err) {
			f.finishLocal(ctx, id)
		} else {
			f.env.Log.Warn("reading a card to send it", "card", id, "error", err)
		}
		return
	}
	req, inv := f.request(detail)
	if inv != nil {
		f.fail(ctx, id, lane.ReasonInvalidCard, inv.Reason)
		return
	}
	ep, ok := d.Endpoints[c.Endpoint]
	if !ok {
		f.fail(ctx, id, lane.ReasonDeliveryFailed, "The conversation endpoint this card sends from is no longer open.")
		return
	}
	if attempt != c.Attempt {
		if err := f.update(id, func(_ *state.Data, r *state.Card) {
			r.BeginAttempt(attempt, f.env.Now())
			if r.Mode == card.ModeNewSession {
				r.Peer, r.Session = "", false
			}
		}); err != nil {
			f.env.Log.Error("recording an attempt", "card", id, "error", err)
			return
		}
	}
	out := Outgoing{From: adapterproto.EndpointRef(ep.Ref), To: adapterproto.Address(c.Peer), Body: req.Task,
		IdempotencyKey: lane.IdempotencyKey(id, attempt)}
	if c.Mode == card.ModeNewSession {
		out.To = req.Address
		out.Control = &adapterproto.Control{Signal: adapterproto.SignalSessionCreate, Params: &adapterproto.ControlParams{Preset: req.Preset}}
	}
	mid, err := f.env.Send(ctx, out)
	switch {
	case err == nil:
	case refused(err):
		f.fail(ctx, id, lane.ReasonDeliveryFailed, clip(fmt.Sprintf("The message was refused: %v", err)))
		return
	default:
		// Not known to have failed: the same attempt is sent again with the same
		// key at the next tick, within the window.
		f.env.Log.Warn("sending a card's message; it is tried again", "card", id, "error", err)
		return
	}
	if err := f.update(id, func(_ *state.Data, r *state.Card) { r.Sent(string(mid), f.env.Now()) }); err != nil {
		f.env.Log.Error("recording a sent message", "card", id, "error", err)
	}
	_ = f.env.endpoints.touch(c.Endpoint)
}

// settle asks the node what became of the message of a card in phase sent, and acts
// on it: the reply to a session creation names the session, a refusal or a lost
// delivery fails the card.
func (f *laneFlows) settle(ctx context.Context, c state.Card) error {
	d := f.env.State.View()
	ep, ok := d.Endpoints[c.Endpoint]
	if !ok || c.MessageID == "" {
		return nil
	}
	res, err := f.env.Status(ctx, adapterproto.EndpointRef(ep.Ref), adapterproto.MessageID(c.MessageID))
	if err != nil {
		return err
	}
	creating := c.Mode == card.ModeNewSession && c.Peer == ""
	if creating && res.ControlReply != nil && res.ControlReply.Create != nil {
		f.created(ctx, c, res)
		return nil
	}
	if len(res.Recipients) == 0 {
		return nil
	}
	switch r := res.Recipients[0]; r.State {
	case adapterproto.DeliveryDelivered:
		if !creating {
			_ = f.update(c.ID, func(_ *state.Data, r *state.Card) { r.Delivered = true })
		}
	case adapterproto.DeliveryRejected:
		f.fail(ctx, c.ID, lane.ReasonDeliveryFailed, fmt.Sprintf("The message was rejected (%s).", r.Reason))
	case adapterproto.DeliveryExpiredUnconfirmed:
		f.fail(ctx, c.ID, lane.ReasonDeliveryFailed, "The message was not delivered in time.")
	case adapterproto.DeliveryUnknown, adapterproto.DeliveryClosedUnconfirmed:
		f.fail(ctx, c.ID, lane.ReasonOutcomeUnknown, fmt.Sprintf("Whether the message arrived is unknown (%s).", r.State))
	}
	return nil
}

// created acts on the reply to a session.create: a session that exists (confirmed
// ready or not) becomes the card's peer and is recorded as open, so that its
// replies match the card and it is closed in time; a failed creation fails the card.
func (f *laneFlows) created(ctx context.Context, c state.Card, res adapterproto.MessageStatusResult) {
	r := res.ControlReply.Create
	if r.Outcome == adapterproto.CreateReplyFailed {
		f.fail(ctx, c.ID, lane.ReasonDeliveryFailed, fmt.Sprintf("The remote session could not be created (%s).", r.Reason))
		return
	}
	addr := sessionAddress(res.Recipients, r.SessionEndpoint)
	err := f.env.State.Update(func(d *state.Data) error {
		rec, ok := d.Cards[c.ID]
		if !ok {
			return nil
		}
		now := f.env.Now()
		rec.Peer, rec.Session, rec.Delivered = addr, true, true
		d.Sessions[addr] = &state.Session{Address: addr, Endpoint: rec.Endpoint, LastUse: now}
		return nil
	})
	if err != nil {
		f.env.Log.Error("recording a remote session", "card", c.ID, "error", err)
		return
	}
	if r.Outcome == adapterproto.CreateReplyUnconfirmed {
		f.comment(ctx, c.ID, commentUnconfirm)
	}
}

// fail ends what the card was doing, once per cause: the first time the card is
// blocked so that its creator decides, the second time it is completed with the
// reason, since Hermes would send a card blocked twice for one kind to a state in
// which nobody is woken (lane.OnFailure).
func (f *laneFlows) fail(ctx context.Context, id string, reason lane.Reason, text string) {
	c, ok := f.env.State.View().Cards[id]
	if !ok {
		return
	}
	cat := reason.Category()
	if lane.OnFailure(*c, reason) == lane.VerdictBlock {
		err := f.env.Board.Block(ctx, id, lane.BlockKindOf(cat), clip(text))
		switch {
		case err == nil:
			_ = f.update(id, func(_ *state.Data, r *state.Card) { r.Block(cat) })
		case isPermanent(err):
			f.reconcileBlock(ctx, id, cat, err)
		default:
			f.env.Log.Warn("blocking a card; it is tried again", "card", id, "error", err)
		}
		return
	}
	summary := summaryNoDelivery
	switch cat {
	case state.CategoryNoReply:
		summary, text = summaryNoReply, "The peer still did not reply after "+f.env.Config.NoReplyTimeout.String()+"."
	case state.CategoryInvalidCard:
		summary = summaryInvalid
	}
	err := f.env.Board.Complete(ctx, id, kanban.Completion{Summary: summary, Result: clip(text)})
	switch {
	case err == nil, isPermanent(err):
		// A card Hermes refuses to complete is terminal or not ours any more.
		f.finishLocal(ctx, id)
	default:
		f.env.Log.Warn("completing a card; it is tried again", "card", id, "error", err)
	}
}

// reconcileBlock reads the card after Hermes refused to block it: if it is blocked
// already (the adapter blocked it just before it ended, or its creator did) the
// record catches up; if it is over, the record ends.
func (f *laneFlows) reconcileBlock(ctx context.Context, id string, cat state.Category, cause error) {
	detail, err := f.env.Board.Show(ctx, id)
	if err != nil {
		if isPermanent(err) {
			f.finishLocal(ctx, id)
		}
		return
	}
	switch detail.Card.Status {
	case kanban.StatusBlocked:
		_ = f.update(id, func(_ *state.Data, r *state.Card) { r.Block(cat) })
	case kanban.StatusDone, kanban.StatusArchived:
		f.finishLocal(ctx, id)
	default:
		f.env.Log.Warn("a card could not be blocked", "card", id, "status", detail.Card.Status, "error", cause)
	}
}

// refreshCreations reads the control replies of the session creations still waiting
// on the endpoint: the first reply of a new session can reach the endpoint before
// the reply to the signal that created it has been read. It is best effort; a
// message that stays unmatched goes on as a new message.
func (f *laneFlows) refreshCreations(ctx context.Context, endpoint string) {
	d := f.env.State.View()
	for _, c := range d.CardList() {
		if c.Endpoint != endpoint || c.Mode != card.ModeNewSession || c.Peer != "" || c.Phase != state.PhaseSent {
			continue
		}
		if err := f.settle(ctx, c); err != nil {
			f.env.Log.Warn("reading the reply to a session creation", "card", c.ID, "error", err)
		}
	}
}

// terminal reports a Hermes status after which a card takes no completion.
func terminal(s kanban.Status) bool {
	return slices.Contains([]kanban.Status{kanban.StatusDone, kanban.StatusArchived}, s)
}
