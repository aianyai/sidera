package console

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// create is `new`: it sends the session.create signal and then asks for its
// control reply until there is one. The node answers a signal when the target
// adapter has, which for a runtime on another node may take a while; the wait ends
// at --timeout, and the message id printed first lets `status` ask later.
func (c *client) create(ctx context.Context, inv *invocation) int {
	sent, err := c.call(ctx, inv.req)
	if err != nil {
		return c.errorf(err)
	}
	id := sent.Send.MessageID
	fmt.Fprintln(c.env.Stdout, "message:", id)
	statusReq := request{Op: opStatus, Status: &adapterproto.MessageStatusParams{MessageID: id}}
	deadline := time.Now().Add(inv.timeout)
	for {
		resp, err := c.call(ctx, statusReq)
		if err != nil {
			return c.errorf(err)
		}
		if reply := resp.Status.ControlReply; reply != nil {
			return c.printCreateReply(resp.Status.Recipients, reply.Create)
		}
		if reason, ok := refused(resp.Status.Recipients); ok {
			fmt.Fprintln(c.env.Stderr, "sidera-adapter-console: the message was not delivered:", reason)
			return exitFailed
		}
		if !time.Now().Add(inv.pollEvery).Before(deadline) {
			fmt.Fprintf(c.env.Stderr, "sidera-adapter-console: no reply within %s; ask again with: sidera-adapter-console status %s\n", inv.timeout, quote(string(id)))
			return exitUnsettled
		}
		select {
		case <-ctx.Done():
			return c.errorf(ctx.Err())
		case <-time.After(inv.pollEvery):
		}
	}
}

// refused finds a recipient whose delivery has ended without the message reaching
// it, so that no reply will come.
func refused(recipients []adapterproto.RecipientStatus) (string, bool) {
	for _, r := range recipients {
		switch r.State {
		case adapterproto.DeliveryRejected:
			return fmt.Sprintf("%s rejected it (%s)", r.To, r.Reason), true
		case adapterproto.DeliveryExpiredUnconfirmed, adapterproto.DeliveryClosedUnconfirmed:
			return fmt.Sprintf("%s is %s", r.To, r.State), true
		}
	}
	return "", false
}

func (c *client) printCreateReply(recipients []adapterproto.RecipientStatus, r *adapterproto.ControlCreateReply) int {
	switch r.Outcome {
	case adapterproto.CreateReplyCreated:
		fmt.Fprintln(c.env.Stdout, "session:", sessionAddress(recipients, r.SessionEndpoint))
		return exitOK
	case adapterproto.CreateReplyUnconfirmed:
		fmt.Fprintln(c.env.Stdout, "session:", sessionAddress(recipients, r.SessionEndpoint))
		fmt.Fprintf(c.env.Stderr, "sidera-adapter-console: the session may exist but is not confirmed ready (%s)\n", r.Reason)
		return exitUnsettled
	}
	fmt.Fprintf(c.env.Stderr, "sidera-adapter-console: the session was not created (%s)\n", r.Reason)
	return exitFailed
}

// printStatus writes the delivery state of each recipient and, for a signal, its
// reply.
func (c *client) printStatus(s adapterproto.MessageStatusResult) {
	w := c.env.Stdout
	for _, r := range s.Recipients {
		if r.Reason != "" {
			fmt.Fprintf(w, "to %s: %s (%s)\n", r.To, r.State, r.Reason)
		} else {
			fmt.Fprintf(w, "to %s: %s\n", r.To, r.State)
		}
	}
	reply := s.ControlReply
	switch {
	case reply == nil:
	case reply.Create != nil:
		cr := reply.Create
		fmt.Fprintf(w, "control %s: %s", reply.Signal, cr.Outcome)
		if cr.Reason != 0 {
			fmt.Fprintf(w, " (%s)", cr.Reason)
		}
		fmt.Fprintln(w)
		if cr.SessionEndpoint != "" {
			fmt.Fprintln(w, "session:", sessionAddress(s.Recipients, cr.SessionEndpoint))
		}
	default:
		raw, _ := json.Marshal(reply)
		fmt.Fprintf(w, "control reply: %s\n", raw)
	}
}

// receive is `recv`: it prints each page and acknowledges it after it was
// printed, never before, so a message the person did not get to see is handed out
// again. A message that is cut into parts is followed to its last part.
func (c *client) receive(ctx context.Context, inv *invocation) int {
	req := inv.req
	for first := true; ; first = false {
		resp, err := c.call(ctx, req)
		if err != nil {
			return c.errorf(err)
		}
		page := resp.Recv.Page
		if len(page.Messages) == 0 {
			if first {
				fmt.Fprintln(c.env.Stderr, "no messages")
			}
			return exitOK
		}
		if code := c.show(ctx, page); code != exitOK {
			return code
		}
		for _, m := range page.Messages {
			for next := nextPart(m); next != nil; {
				part, err := c.call(ctx, request{Op: opRecv, Recv: &adapterproto.RecvParams{MessageID: next.Msg, Part: next.Part}})
				if err != nil {
					return c.errorf(err)
				}
				if code := c.show(ctx, part.Recv.Page); code != exitOK {
					return code
				}
				next = nil
				if len(part.Recv.Page.Messages) == 1 {
					next = nextPart(part.Recv.Page.Messages[0])
				}
			}
		}
		if page.More == 0 {
			return exitOK
		}
		// The rest of the queue is there now: do not wait for it.
		zero := 0
		req = request{Op: opRecv, Recv: &adapterproto.RecvParams{WaitS: &zero}}
	}
}

func nextPart(m adapterproto.PageMessage) *adapterproto.PageNext {
	if m.Part == nil {
		return nil
	}
	return m.Part.Next
}

// show prints a page and acknowledges what it printed.
func (c *client) show(ctx context.Context, page adapterproto.RecvPage) int {
	items := make([]adapterproto.PartRef, 0, len(page.Messages))
	for _, m := range page.Messages {
		c.printMessage(m)
		part := 1
		if m.Part != nil {
			part = m.Part.Index
		}
		items = append(items, adapterproto.PartRef{MessageID: m.ID, Part: part})
	}
	acked, err := c.call(ctx, request{Op: opAck, Ack: &adapterproto.AckParams{Lease: page.Lease, Items: items}})
	if err != nil {
		return c.errorf(fmt.Errorf("acknowledging what was printed: %w", err))
	}
	for _, it := range acked.Ack.Items {
		switch it.Ack {
		case adapterproto.ItemRecorded, adapterproto.ItemCompleted, adapterproto.ItemDuplicate:
		default:
			fmt.Fprintf(c.env.Stderr, "sidera-adapter-console: message %s part %d was not acknowledged (%s)\n", it.MessageID, it.Part, it.Ack)
		}
	}
	return exitOK
}

func (c *client) printMessage(m adapterproto.PageMessage) {
	w := c.env.Stdout
	fmt.Fprintf(w, "from %s  id %s", m.From, m.ID)
	if m.Part != nil {
		fmt.Fprintf(w, "  %s", m.Part.Label)
	}
	if m.PossiblyDuplicate {
		fmt.Fprint(w, "  (possibly shown before)")
	}
	fmt.Fprintf(w, "\n%s\n\n", m.Body)
}
