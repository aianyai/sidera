package console

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/termout"
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
	to := make([]string, len(inv.req.Send.To))
	for i, a := range inv.req.Send.To {
		to[i] = string(a)
	}
	fmt.Fprint(c.out, c.out.OK("Session request accepted for delivery.\n"+c.out.Block([]termout.Field{
		{Label: "Message", Value: string(id)},
		{Label: "To", Value: strings.Join(to, "\n")},
	})))
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
			fmt.Fprintf(c.env.Stderr, "sidera-adapter-console: no reply within %s; ask again with: %s\n", inv.timeout, inv.command("status", termout.ShellWord(runtime.GOOS, string(id))))
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
	session := c.out.Block([]termout.Field{{Label: "Session", Value: sessionAddress(recipients, r.SessionEndpoint)}})
	switch r.Outcome {
	case adapterproto.CreateReplyCreated:
		fmt.Fprint(c.out, "\n"+c.out.OK("Session created.\n"+session))
		return exitOK
	case adapterproto.CreateReplyUnconfirmed:
		fmt.Fprint(c.out, "\n"+c.out.Warn("Session created, but not confirmed ready.\n"+session))
		fmt.Fprintf(c.env.Stderr, "sidera-adapter-console: the session may exist but is not confirmed ready (%s)\n", r.Reason)
		return exitUnsettled
	}
	fmt.Fprintf(c.env.Stderr, "sidera-adapter-console: the session was not created (%s)\n", r.Reason)
	return exitFailed
}

// printStatus writes the delivery state of each recipient and, for a signal, its
// reply.
func (c *client) printStatus(inv *invocation, s adapterproto.MessageStatusResult) {
	fmt.Fprint(c.out, c.view(inv).Status(time.Now(), s))
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
				fmt.Fprintln(c.out, "No messages waiting.")
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
		if !it.Ack.Acknowledged() {
			fmt.Fprintf(c.env.Stderr, "sidera-adapter-console: message %s part %d was not acknowledged (%s)\n", it.MessageID, it.Part, it.Ack)
		}
	}
	return exitOK
}

// printMessage writes the header of a message, a block of what the channel vouches
// for, and then its body. The body is the one field a peer writes freely: it is shown
// through termout.SanitizeText, so that it cannot act in the owner's terminal, and
// indented, so that a body shaped like a header ("From  node/agent") cannot pass for
// one, since a real header starts in the first column.
func (c *client) printMessage(m adapterproto.PageMessage) {
	fields := []termout.Field{
		{Label: "From", Value: string(m.From)},
		{Label: "Message", Value: string(m.ID)},
	}
	if m.Part != nil {
		fields = append(fields, termout.Field{Label: "Part", Value: fmt.Sprintf("%d of %d", m.Part.Index, m.Part.Total)})
	}
	if m.Feedback {
		fields = append(fields, termout.Field{Label: "Notice", Value: "channel notice, from the node"})
	}
	if m.PossiblyDuplicate {
		fields = append(fields, termout.Field{Label: "Repeat", Value: "possibly shown before"})
	}
	body := termout.Indent(termout.SanitizeText(m.Body), 2)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	fmt.Fprint(c.out, c.out.Block(fields)+"\n"+body+"\n")
}

// directory is `directory`: it prints one page of the endpoints a node shows to
// the owner endpoint, and the command for the next page when there is one. A page
// the node asked could not give is a result, not a failure of the call, and ends
// the command as a failure with its reason.
func (c *client) directory(ctx context.Context, inv *invocation) int {
	resp, err := c.call(ctx, inv.req)
	if err != nil {
		return c.errorf(err)
	}
	res := resp.Directory
	if res.Outcome != adapterproto.DirectoryOK {
		fmt.Fprintf(c.env.Stderr, "sidera-adapter-console: the directory was not given: %s (%s)\n", res.Outcome, res.Reason)
		return exitFailed
	}
	fmt.Fprint(c.out, c.view(inv).Directory(*res, inv.req.Directory.Node))
	return exitOK
}

// peers is `peers`: it prints the nodes the owner endpoint can address, this
// node first, with the name each is known by in `directory --node`.
func (c *client) peers(ctx context.Context, inv *invocation) int {
	resp, err := c.call(ctx, inv.req)
	if err != nil {
		return c.errorf(err)
	}
	fmt.Fprint(c.out, c.view(inv).Peers(*resp.Peers))
	return exitOK
}
