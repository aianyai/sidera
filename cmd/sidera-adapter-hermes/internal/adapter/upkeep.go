package adapter

import (
	"context"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/lane"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Recover reconciles the cards the record holds with the board after a start: a
// card Hermes has finished or no longer knows ends here too. What is left is moved
// on by Tick from the phase the record persisted, and cards the board lists as
// ready again go through the poll.
func (f *laneFlows) Recover(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.env.State.View()
	for _, c := range d.CardList() {
		if !c.Unfinished() {
			continue
		}
		if c.Phase == state.PhaseClaiming {
			if err := f.reconcileClaiming(ctx, c.ID); err != nil {
				return err
			}
			continue
		}
		detail, err := f.env.Board.Show(ctx, c.ID)
		switch {
		case err == nil && terminal(detail.Card.Status), err != nil && isPermanent(err):
			f.finishLocal(ctx, c.ID)
		case err != nil:
			return err
		}
	}
	return nil
}

// Tick does what the clock makes due: sends and settles, waits that ran out,
// remote sessions to close, records to forget.
func (f *laneFlows) Tick(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.env.State.View()
	for _, c := range d.CardList() {
		switch c.Phase {
		case state.PhaseClaiming:
			if err := f.reconcileClaiming(ctx, c.ID); err != nil {
				f.env.Log.Warn("reading a card whose claim was intended", "card", c.ID, "error", err)
			}
		case state.PhaseNew, state.PhaseAttempting, state.PhaseSent:
			f.advance(ctx, c.ID)
		}
	}
	f.closeSessions(ctx)
	if err := f.env.State.Update(func(d *state.Data) error {
		d.Prune(f.env.Now(), f.env.Config.SessionRetention)
		return nil
	}); err != nil {
		f.env.Log.Error("forgetting finished cards", "error", err)
	}
}

// closeSessions reads what became of the closes asked earlier, then asks for the
// ones that are due. A close is not waited for: the node answers when the other
// node has, which may be long.
func (f *laneFlows) closeSessions(ctx context.Context) {
	d := f.env.State.View()
	for _, s := range d.SessionList() {
		if s.CloseMessage != "" && ctx.Err() == nil {
			f.collectClose(ctx, s)
		}
	}
	d = f.env.State.View()
	cfg := f.env.Config
	due := lane.SessionsToClose(d.SessionList(), d.CardList(),
		lane.SessionPolicy{CloseWindow: cfg.CloseWindow, Retention: cfg.SessionRetention, Max: cfg.MaxOpenSessions}, f.env.Now())
	for _, addr := range due {
		if ctx.Err() == nil {
			f.askClose(ctx, *d.Sessions[addr])
		}
	}
}

// askClose sends session.close from the endpoint that created the session; only
// the creator may close it. The try is recorded even when the send failed, so that
// a node that refuses is asked again after lane.CloseRetryInterval and not every
// tick.
func (f *laneFlows) askClose(ctx context.Context, s state.Session) {
	d := f.env.State.View()
	ep, ok := d.Endpoints[s.Endpoint]
	if !ok {
		f.env.Log.Warn("a remote session cannot be closed: the endpoint that created it is gone; it is forgotten", "session", s.Address)
		f.forget(s.Address)
		return
	}
	id, err := f.env.Send(ctx, Outgoing{From: adapterproto.EndpointRef(ep.Ref), To: adapterproto.Address(s.Address),
		Control: &adapterproto.Control{Signal: adapterproto.SignalSessionClose}})
	if err != nil {
		f.env.Log.Warn("asking a remote session to close", "session", s.Address, "error", err)
	}
	_ = f.env.State.Update(func(d *state.Data) error {
		if rec, ok := d.Sessions[s.Address]; ok {
			rec.CloseTriedAt, rec.CloseMessage = f.env.Now(), string(id)
		}
		return nil
	})
}

// collectClose reads the reply to a close. A session that is closed, or whose
// endpoint is gone, is forgotten; a close the other node could not confirm stays
// recorded and is asked again after lane.CloseRetryInterval.
func (f *laneFlows) collectClose(ctx context.Context, s state.Session) {
	d := f.env.State.View()
	ep, ok := d.Endpoints[s.Endpoint]
	if !ok {
		f.forget(s.Address)
		return
	}
	res, err := f.env.Status(ctx, adapterproto.EndpointRef(ep.Ref), adapterproto.MessageID(s.CloseMessage))
	if err != nil {
		f.env.Log.Warn("reading the reply to a close", "session", s.Address, "error", err)
		return
	}
	switch {
	case res.ControlReply != nil && res.ControlReply.Close != nil:
		switch res.ControlReply.Close.Outcome {
		case adapterproto.CloseReplyUnconfirmed:
			f.settleClose(s.Address)
		default:
			// Closed; or unsupported, which a session of an agent does not answer and
			// which asking again would not change.
			f.forget(s.Address)
		}
	case len(res.Recipients) == 1 && res.Recipients[0].State == adapterproto.DeliveryRejected:
		switch res.Recipients[0].Reason {
		case adapterproto.RejectEndpointClosed, adapterproto.RejectEndpointMissing:
			f.forget(s.Address)
		default:
			f.settleClose(s.Address)
		}
	}
}

// settleClose drops the pending close message; the session is asked again when
// lane.SessionsToClose offers it after the retry interval.
func (f *laneFlows) settleClose(addr string) {
	_ = f.env.State.Update(func(d *state.Data) error {
		if rec, ok := d.Sessions[addr]; ok {
			rec.CloseMessage = ""
		}
		return nil
	})
}

func (f *laneFlows) forget(addr string) {
	if err := f.env.State.Update(func(d *state.Data) error {
		delete(d.Sessions, addr)
		return nil
	}); err != nil {
		f.env.Log.Error("forgetting a remote session", "session", addr, "error", err)
	}
}
