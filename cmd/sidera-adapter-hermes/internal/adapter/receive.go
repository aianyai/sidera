package adapter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// receiver takes the messages that wait for the adapter's endpoints and gives them
// to the message handler.
//
// It reads only while Hermes can be used. A message that is read and not
// acknowledged is handed out again when its lease ends, and after
// adapterproto.MaxRedeliveries redeliveries its outcome is declared unknown to the
// sender. Leases arise only on a read, so a receiver that does not read while the
// card cannot be written costs the messages nothing; they wait in the durable queue
// of the node. Reads never wait (wait_s 0): the adapter reads several endpoints from
// one loop, and a waiting read holds one of the connection's concurrent requests.
type receiver struct {
	conn    *adapterkit.Conn
	eps     *endpoints
	store   *state.Store
	gate    *gate
	handler MessageHandler
	log     *slog.Logger
	now     func() time.Time
	tm      timing

	wake chan struct{}
	mu   sync.Mutex
	// dirty are the endpoints a notification said hold messages.
	dirty map[adapterproto.EndpointRef]bool
}

func newReceiver(conn *adapterkit.Conn, eps *endpoints, store *state.Store, g *gate, h MessageHandler, log *slog.Logger, now func() time.Time, tm timing) *receiver {
	return &receiver{conn: conn, eps: eps, store: store, gate: g, handler: h, log: log, now: now, tm: tm,
		wake: make(chan struct{}, 1), dirty: map[adapterproto.EndpointRef]bool{}}
}

// notify records that messages wait for the endpoint and wakes the loop.
func (r *receiver) notify(ref adapterproto.EndpointRef) {
	r.mu.Lock()
	r.dirty[ref] = true
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// run reads until ctx ends. A round reads the endpoints a notification named, or all
// of them when the low-frequency poll is due: a notification is best effort. The
// first round reads all, for the messages that arrived while the adapter was down.
func (r *receiver) run(ctx context.Context) {
	poll := time.NewTicker(r.tm.receivePoll)
	defer poll.Stop()
	pauses := newBackoff(r.tm.retryMin, r.tm.retryMax)
	all := true
	for {
		if !all {
			select {
			case <-ctx.Done():
				return
			case <-r.wake:
			case <-poll.C:
				all = true
			}
		}
		paused := r.round(ctx, all)
		all = false
		if paused {
			if !sleep(ctx, pauses.next()) {
				return
			}
			// Notifications that came during the pause were recorded; any that were
			// lost are covered by reading everything.
			all = true
			continue
		}
		pauses.reset()
		if ctx.Err() != nil {
			return
		}
	}
}

// round reads the endpoints it should and reports whether receiving has to pause.
func (r *receiver) round(ctx context.Context, all bool) (paused bool) {
	if err := r.gate.Check(ctx); err != nil {
		return ctx.Err() == nil
	}
	r.mu.Lock()
	dirty := r.dirty
	r.dirty = map[adapterproto.EndpointRef]bool{}
	r.mu.Unlock()
	for _, ep := range r.eps.list() {
		if !all && !dirty[ep.Ref] {
			continue
		}
		if r.drain(ctx, ep) {
			return true
		}
	}
	return false
}

// drain reads an endpoint until it holds nothing more to hand out, and reports
// whether receiving has to pause.
func (r *receiver) drain(ctx context.Context, ep Endpoint) (paused bool) {
	zero := 0
	for ctx.Err() == nil {
		res, err := r.conn.Recv(ctx, adapterproto.RecvParams{Endpoint: ep.Ref, WaitS: &zero})
		if err != nil {
			if ctx.Err() == nil {
				r.log.Warn("reading an endpoint", "endpoint", ep.Key, "error", err)
			}
			return false
		}
		page := res.Page
		if len(page.Messages) == 0 {
			return false
		}
		if err := r.eps.touch(ep.Key); err != nil {
			r.log.Error("recording the use of an endpoint", "endpoint", ep.Key, "error", err)
			return true
		}
		for _, m := range page.Messages {
			if r.process(ctx, ep, page.Lease, m) {
				return true
			}
		}
		if page.More == 0 {
			return false
		}
	}
	return false
}

// A part of a message and the lease of the page it came on, which is what
// acknowledges it.
type heldPart struct {
	lease adapterproto.LeaseRef
	ref   adapterproto.PartRef
}

// process handles one message of a page and reports whether receiving has to pause.
// Everything that decides is here: a processed message is acknowledged without the
// handler, and a message is acknowledged after the handler says it is written.
func (r *receiver) process(ctx context.Context, ep Endpoint, lease adapterproto.LeaseRef, first adapterproto.PageMessage) (paused bool) {
	msg, parts, err := r.assemble(ctx, ep, lease, first)
	if err != nil {
		// Nothing was acknowledged; the lease ends and the message comes again.
		r.log.Warn("reading all the parts of a message", "message", first.ID, "error", err)
		return false
	}
	d := r.store.View()
	if !d.Seen(string(msg.ID), r.now()) {
		verdict, herr := r.handler.HandleMessage(ctx, msg)
		switch verdict {
		case Written:
			if err := r.store.Update(func(d *state.Data) error {
				d.MarkProcessed(string(msg.ID), r.now())
				return nil
			}); err != nil {
				r.log.Error("recording a processed message; it is not acknowledged", "message", msg.ID, "error", err)
				return true
			}
		case Retry:
			r.log.Warn("a message could not be written now; receiving pauses", "message", msg.ID, "endpoint", ep.Key, "error", herr)
			return true
		default:
			r.log.Error("a message was left unacknowledged: it cannot be written", "message", msg.ID, "endpoint", ep.Key, "verdict", verdict, "error", herr)
			return false
		}
	}
	r.acknowledge(ctx, ep, parts)
	return false
}

// assemble reads the rest of a message cut into parts and returns it whole, with
// the parts that acknowledge it. A message is handled only when every part is in
// hand: a task cut short would be carried out as if it were complete.
func (r *receiver) assemble(ctx context.Context, ep Endpoint, lease adapterproto.LeaseRef, first adapterproto.PageMessage) (Message, []heldPart, error) {
	msg := Message{ID: first.ID, From: first.From, To: ep, PossiblyDuplicate: first.PossiblyDuplicate}
	if first.Part == nil {
		msg.Body = first.Body
		return msg, []heldPart{{lease, adapterproto.PartRef{MessageID: first.ID, Part: 1}}}, nil
	}
	if first.Part.Index != 1 {
		return Message{}, nil, fmt.Errorf("the page starts at part %s, not part 1", first.Part.Label)
	}
	var body strings.Builder
	var parts []heldPart
	for cur, curLease := first, lease; ; {
		body.WriteString(cur.Body)
		msg.PossiblyDuplicate = msg.PossiblyDuplicate || cur.PossiblyDuplicate
		parts = append(parts, heldPart{curLease, adapterproto.PartRef{MessageID: first.ID, Part: cur.Part.Index}})
		next := cur.Part.Next
		if next == nil {
			break
		}
		res, err := r.conn.Recv(ctx, adapterproto.RecvParams{Endpoint: ep.Ref, MessageID: next.Msg, Part: next.Part})
		if err != nil {
			return Message{}, nil, err
		}
		if len(res.Page.Messages) != 1 || res.Page.Messages[0].ID != first.ID || res.Page.Messages[0].Part == nil ||
			res.Page.Messages[0].Part.Index != next.Part {
			return Message{}, nil, fmt.Errorf("part %d was asked for and another page came", next.Part)
		}
		cur, curLease = res.Page.Messages[0], res.Page.Lease
	}
	msg.Body = body.String()
	return msg, parts, nil
}

// acknowledge acknowledges every part, each with the lease of its page. A failure
// is only logged: an unacknowledged message comes again and is then recognised as
// processed.
func (r *receiver) acknowledge(ctx context.Context, ep Endpoint, parts []heldPart) {
	for _, p := range parts {
		res, err := r.conn.Ack(ctx, adapterproto.AckParams{Endpoint: ep.Ref, Lease: p.lease, Items: []adapterproto.PartRef{p.ref}})
		if err != nil {
			if ctx.Err() == nil {
				r.log.Warn("acknowledging a message", "message", p.ref.MessageID, "part", p.ref.Part, "error", err)
			}
			return
		}
		for _, it := range res.Items {
			switch it.Ack {
			case adapterproto.ItemRecorded, adapterproto.ItemCompleted, adapterproto.ItemDuplicate:
			default:
				r.log.Warn("a message part was not acknowledged", "message", it.MessageID, "part", it.Part, "ack", it.Ack)
			}
		}
	}
}
