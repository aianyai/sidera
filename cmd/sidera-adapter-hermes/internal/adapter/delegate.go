package adapter

import (
	"context"
	"errors"
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
	act := lane.OnReady(d.Cards[c.ID], f.env.Now())
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
	// origin is where the card comes from (state.Origin): read from the record of a
	// card planned before, else from the board.
	origin state.Origin
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
// the whole claim time. For the same reason a record of the intent is written just
// before the claim (intendClaim), so that a crash between the claim and the rest of
// the record is found at the next start.
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
	if !f.intendClaim(id, p.origin) {
		return false
	}
	if err := f.ensureSubscribed(ctx, id, p.origin); err != nil {
		f.env.Log.Warn("subscribing a card; it is looked at again", "card", id, "error", err)
		return false
	}
	if !f.claim(ctx, id) {
		return false
	}
	f.begin(ctx, id, p)
	return false
}

// intendClaim writes the record of a card the adapter does not know yet before the
// card is claimed, with its origin; a card it knows keeps the origin it has. A claim
// that is made and then forgotten by a crash leaves the card running for the whole
// claim time, listed by nobody; the record is how the next start finds it
// (reconcileClaiming). It reports whether the record is written, and the card is not
// claimed when it is not.
func (f *laneFlows) intendClaim(id string, origin state.Origin) bool {
	err := f.env.State.Update(func(d *state.Data) error {
		c, ok := d.Cards[id]
		if !ok {
			c = &state.Card{ID: id, Phase: state.PhaseClaiming, ClaimedAt: f.env.Now()}
			d.Cards[id] = c
		}
		if c.Origin == nil {
			c.Origin = &origin
		}
		return nil
	})
	if err != nil {
		f.env.Log.Error("recording that a card is about to be claimed; it is not claimed", "card", id, "error", err)
	}
	return err == nil
}

// ensureSubscribed makes sure that the completion of a card with no subscription of
// its own wakes someone: the home target is subscribed before the card is claimed,
// since a subscription starts caught up on the card's events and one made after the
// completion would never wake anybody. It is done after the origin is recorded, so
// that a crash in between cannot make the subscription look like the card's own.
func (f *laneFlows) ensureSubscribed(ctx context.Context, id string, o state.Origin) error {
	if !o.Home {
		return nil
	}
	subs, err := f.env.Board.NotifyList(ctx, id)
	if err != nil || len(subs) > 0 {
		return err
	}
	return f.env.Board.NotifySubscribe(ctx, id, subscriptionFor(o.Target))
}

// begin starts a card the adapter has claimed: it completes the record, tells the
// creator where the card was placed, and sends it or fails it.
func (f *laneFlows) begin(ctx context.Context, id string, p *plan) {
	f.adopt(id, p)
	if p.placeNote != "" {
		f.comment(ctx, id, p.placeNote)
	}
	if p.failure != nil {
		f.fail(ctx, id, p.failure.reason, p.failure.text)
		return
	}
	f.advance(ctx, id)
}

// reconcileClaiming settles a record that was written before a claim, at a start
// and on every tick. A card the board shows running was claimed by this adapter
// (nothing else claims a card of this lane): it is adopted as if the claim had just
// been made; one it cannot serve is blocked with the reason, so that its creator
// knows. A card still ready was not claimed, and the poll takes it again. Any other
// card is not the adapter's, and the record is dropped. The returned error
// is a failure of Hermes that may pass.
func (f *laneFlows) reconcileClaiming(ctx context.Context, id string) error {
	detail, err := f.env.Board.Show(ctx, id)
	switch {
	case err != nil && !isPermanent(err):
		return err
	case err != nil:
		f.forgetCard(id)
	case detail.Card.Status == kanban.StatusReady:
	case detail.Card.Status != kanban.StatusRunning:
		f.forgetCard(id)
	default:
		p, err := f.plan(ctx, id)
		switch {
		case err != nil && !isPermanent(err):
			return err
		case err != nil || p.reply:
			// Left alone the card would stay claimed for the whole claim time with nobody
			// told. Its creator is, by blocking it with the reason; a card Hermes no
			// longer has ends in the record instead (fail reads it back).
			f.env.Log.Warn("a card claimed by this adapter cannot be served; it is blocked", "card", id, "error", err)
			return f.fail(ctx, id, lane.ReasonInvalidCard, "The Sidera adapter claimed this card but cannot serve it. Unblock it to try again.")
		default:
			f.begin(ctx, id, p)
		}
	}
	return nil
}

// forgetCard drops the record of a card the adapter does not hold.
func (f *laneFlows) forgetCard(id string) {
	if err := f.env.State.Update(func(d *state.Data) error {
		delete(d.Cards, id)
		return nil
	}); err != nil {
		f.env.Log.Error("forgetting a card", "card", id, "error", err)
	}
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

	if rec, ok := d.Cards[id]; ok && rec.Origin != nil {
		p.origin = *rec.Origin
	} else if p.origin, err = f.origin(ctx, id, detail.Card); err != nil {
		return nil, err
	}
	src, target := p.origin.Source, p.origin.Target
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
	if err != nil {
		// Decode returns nothing else.
		return card.Request{}, err.(*card.InvalidError)
	}
	return f.env.validate(env, d.Card.CreatedBy, len(d.Parents) > 0)
}

// origin finds the conversation or job a card comes from, reading the board only. A
// card with no subscription (a cron job's, or one made outside a chat) has none: its
// completion will wake the home target, which take subscribes before the card is
// claimed (ensureSubscribed). The result is recorded with the card by intendClaim and
// never read from the board again, because the board then holds the subscription of
// the home target the adapter made.
func (f *laneFlows) origin(ctx context.Context, id string, c kanban.Card) (state.Origin, error) {
	subs, err := f.env.Board.NotifyList(ctx, id)
	if err != nil {
		return state.Origin{}, err
	}
	if len(subs) > 0 {
		s := subs[0]
		meta := func(key string) string { v, _ := s.DeliveryMetadata[key].(string); return v }
		target := config.Target{Profile: s.NotifierProfile, Platform: s.Platform, ChatID: s.ChatID, ThreadID: s.ThreadID,
			ChatType: s.ChatType, UserID: s.UserID, UserIDAlt: s.UserIDAlt,
			ParentChatID: meta("parent_chat_id"), GuildID: meta("guild_id")}
		return state.Origin{Source: lane.Source{Profile: s.NotifierProfile, Platform: s.Platform, ChatID: s.ChatID, ThreadID: s.ThreadID}, Target: target}, nil
	}
	home := f.env.Config.Home
	src := lane.Source{Profile: home.Profile}
	if m := cronSession.FindStringSubmatch(c.SessionID); m != nil {
		src.CronJobID = m[1]
	}
	return state.Origin{Source: src, Target: home, Home: true}, nil
}

// adopt completes the record of a claimed card (intendClaim wrote its start). A card the adapter knew keeps its
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
		if c.Phase == state.PhaseBlocked || c.Phase == state.PhaseClaiming {
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
			if _, err := f.settle(ctx, *c); err != nil {
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
// delivery fails the card. ended says the card was failed because its message did
// not arrive; the error is a failure to read the status or to write the card that
// may pass, and the card is then settled again by the next try.
func (f *laneFlows) settle(ctx context.Context, c state.Card) (ended bool, err error) {
	d := f.env.State.View()
	ep, ok := d.Endpoints[c.Endpoint]
	if !ok || c.MessageID == "" {
		return false, nil
	}
	res, err := f.env.Status(ctx, adapterproto.EndpointRef(ep.Ref), adapterproto.MessageID(c.MessageID))
	if err != nil {
		return false, err
	}
	creating := c.Mode == card.ModeNewSession && c.Peer == ""
	if creating && res.ControlReply != nil && res.ControlReply.Create != nil {
		return f.created(ctx, c, res)
	}
	if len(res.Recipients) == 0 {
		return false, nil
	}
	r := res.Recipients[0]
	if r.State == adapterproto.DeliveryDelivered {
		if !creating {
			_ = f.update(c.ID, func(_ *state.Data, r *state.Card) { r.Delivered = true })
		}
		return false, nil
	}
	if reason, text, failed := undelivered(r); failed {
		f.noteDeliveryFailure(c.Endpoint)
		return true, f.fail(ctx, c.ID, reason, text)
	}
	return false, nil
}

// undelivered tells whether the node reports that a message did not arrive, or that
// it cannot tell, with the reason a card that sent it is failed for and the text
// that says so. A message still on its way is neither.
func undelivered(r adapterproto.RecipientStatus) (lane.Reason, string, bool) {
	switch r.State {
	case adapterproto.DeliveryRejected:
		return lane.ReasonDeliveryFailed, fmt.Sprintf("The message was rejected (%s).", r.Reason), true
	case adapterproto.DeliveryExpiredUnconfirmed:
		return lane.ReasonDeliveryFailed, "The message was not delivered in time.", true
	case adapterproto.DeliveryUnknown, adapterproto.DeliveryClosedUnconfirmed:
		return lane.ReasonOutcomeUnknown, fmt.Sprintf("Whether the message arrived is unknown (%s).", r.State), true
	}
	return 0, "", false
}

// settleEndpoint settles, as the tick does, every card that sent from the endpoint
// and whose message is not known to have been delivered, and reports whether any of
// them ended. A status the node refuses for good (it no longer knows the message)
// leaves that card to the tick; only a failure that may pass is returned.
func (f *laneFlows) settleEndpoint(ctx context.Context, key string) (ended bool, err error) {
	d := f.env.State.View()
	for _, c := range d.CardList() {
		if c.Endpoint != key || c.Phase != state.PhaseSent || c.Delivered {
			continue
		}
		e, serr := f.settle(ctx, c)
		switch {
		case serr != nil && refused(serr):
			f.env.Log.Warn("the node refused to say what became of a message", "card", c.ID, "error", serr)
		case serr != nil:
			err = errors.Join(err, serr)
		}
		ended = ended || e
	}
	return ended, err
}

// created acts on the reply to a session.create: a session that exists (confirmed
// ready or not) becomes the card's peer and is recorded as open, so that its
// replies match the card and it is closed in time; a failed creation fails the card.
func (f *laneFlows) created(ctx context.Context, c state.Card, res adapterproto.MessageStatusResult) (ended bool, err error) {
	r := res.ControlReply.Create
	if r.Outcome == adapterproto.CreateReplyFailed {
		f.noteDeliveryFailure(c.Endpoint)
		return true, f.fail(ctx, c.ID, lane.ReasonDeliveryFailed, fmt.Sprintf("The remote session could not be created (%s).", r.Reason))
	}
	addr := sessionAddress(res.Recipients, r.SessionEndpoint)
	err = f.env.State.Update(func(d *state.Data) error {
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
		return false, nil
	}
	if r.Outcome == adapterproto.CreateReplyUnconfirmed {
		f.comment(ctx, c.ID, commentUnconfirm)
	}
	return false, nil
}

// fail ends what the card was doing, once per cause (endFailed). The error is a
// failure of Hermes that may pass: the card is then not ended and the caller sees to
// it again.
func (f *laneFlows) fail(ctx context.Context, id string, reason lane.Reason, text string) error {
	c, ok := f.env.State.View().Cards[id]
	if !ok {
		return nil
	}
	cat := reason.Category()
	again := ""
	if cat == state.CategoryNoReply {
		again = "The peer still did not reply after " + f.env.Config.NoReplyTimeout.String() + "."
	}
	blocked, err := endFailed(ctx, f.env.Board, id, c.Blocks, reason, text, again, false)
	switch {
	case err == nil && blocked:
		_ = f.update(id, func(_ *state.Data, r *state.Card) { r.Block(cat) })
	case err == nil, isPermanent(err) && !blocked:
		// A card Hermes refuses to complete is terminal or not ours any more.
		f.finishLocal(ctx, id)
	case isPermanent(err):
		f.reconcileBlock(ctx, id, cat, err)
	case blocked:
		f.env.Log.Warn("blocking a card; it is tried again", "card", id, "error", err)
		return err
	default:
		f.env.Log.Warn("completing a card; it is tried again", "card", id, "error", err)
		return err
	}
	return nil
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
		if _, err := f.settle(ctx, c); err != nil {
			f.env.Log.Warn("reading the reply to a session creation", "card", c.ID, "error", err)
		}
	}
}

// terminal reports a Hermes status after which a card takes no completion.
func terminal(s kanban.Status) bool {
	return slices.Contains([]kanban.Status{kanban.StatusDone, kanban.StatusArchived}, s)
}
