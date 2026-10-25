package adapterproto

import (
	"fmt"
	"regexp"
)

// MaxPart bounds a part number.
const MaxPart = 1_000_000

// maxAckItems bounds the entries of one acknowledgement.
const maxAckItems = 1024

var partLabelRE = regexp.MustCompile(partLabelPattern)

const partLabelPattern = `^part [0-9]+/[0-9]+$`

// PageNext points at the following part of a message that is handed out in
// parts. It is neutral to the front end: the command line and the MCP tool each
// turn it into their own call.
type PageNext struct {
	Msg  MessageID `json:"msg"`
	Part int       `json:"part"`
}

// Validate checks the pointer.
func (n PageNext) Validate() error {
	var c checker
	c.field("msg", n.Msg.Validate())
	c.field("part", intRange(n.Part, 1, MaxPart))
	return c.err
}

// PagePart labels a message that is handed out in parts.
type PagePart struct {
	Index int    `json:"index"`
	Total int    `json:"total"`
	Label string `json:"label"`
	// Next is absent on the last part.
	Next *PageNext `json:"next,omitempty"`
}

// Validate checks the label against the numbers.
func (p PagePart) Validate() error {
	var c checker
	c.field("index", intRange(p.Index, 1, MaxPart))
	c.field("total", intRange(p.Total, 1, MaxPart))
	c.field("label", matches(partLabelRE, p.Label))
	if p.Next != nil {
		c.field("next", p.Next.Validate())
	}
	if c.err != nil {
		return c.err
	}
	c.beyond(p.Index <= p.Total, "index: %d is beyond total %d", p.Index, p.Total)
	c.beyond(p.Label == fmt.Sprintf("part %d/%d", p.Index, p.Total), "label: %q does not read part %d/%d", p.Label, p.Index, p.Total)
	c.beyond((p.Next == nil) == (p.Index == p.Total), "next: present exactly on every part but the last")
	if p.Next != nil {
		c.beyond(p.Next.Part == p.Index+1, "next.part: %d is not the part after %d", p.Next.Part, p.Index)
	}
	return c.err
}

// PageMessage is one message, or one part of a message, on a page. The channel
// vouches for ID and From; Body is what the sender wrote, or a part of it, and is
// never empty: a message has a body.
type PageMessage struct {
	ID   MessageID `json:"id"`
	From Address   `json:"from"`
	// PossiblyDuplicate marks a part that was handed out before and never
	// acknowledged.
	PossiblyDuplicate bool      `json:"possibly_duplicate,omitempty"`
	Part              *PagePart `json:"part,omitempty"`
	Body              string    `json:"body"`
}

// Validate checks the message, and that a part's next pointer names this message.
func (m PageMessage) Validate() error {
	var c checker
	c.field("id", m.ID.Validate())
	c.field("from", m.From.Validate())
	c.ok(m.Body != "", "body: a message has a body")
	if m.Part != nil {
		c.field("part", m.Part.Validate())
		if c.err == nil && m.Part.Next != nil {
			c.beyond(m.Part.Next.Msg == m.ID, "part.next.msg: %q is not this message", string(m.Part.Next.Msg))
		}
	}
	return c.err
}

// RecvPage is what one recv hands out, in the JSON shape the recv command prints:
// whole messages that fit the output budget, or one part of an oversized
// message. Lease is the page identifier and the thing an acknowledgement names.
type RecvPage struct {
	Lease    LeaseRef      `json:"lease"`
	Messages []PageMessage `json:"messages"`
	// More is the number of queued messages not on this page.
	More int `json:"more"`
}

// Validate checks the page.
func (p RecvPage) Validate() error {
	var c checker
	c.field("lease", p.Lease.Validate())
	c.field("messages", present(p.Messages))
	list(&c, "messages", p.Messages)
	c.field("more", intRange(p.More, 0, 1<<31-1))
	return c.err
}

// RecvObservedParams reports the result of a recv tool call as the model received
// it, read from the runtime's durable record. The adapter sends it only when the
// whole result is there; the daemon decides whether that proves the model saw
// the page, and acknowledges on the agent's behalf if so.
type RecvObservedParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	// Lease is the page identifier the result carries.
	Lease    LeaseRef `json:"lease"`
	Frontend Frontend `json:"frontend"`
	// Observed is the text of the tool result, unchanged.
	Observed string `json:"observed"`
}

// Validate checks the parameters.
func (p RecvObservedParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("lease", p.Lease.Validate())
	c.field("frontend", enumOK(p.Frontend, frontends))
	return c.err
}

// PartRef names one message or one part of it.
type PartRef struct {
	MessageID MessageID `json:"message_id"`
	Part      int       `json:"part"`
}

// Validate checks the reference.
func (r PartRef) Validate() error {
	var c checker
	c.field("message_id", r.MessageID.Validate())
	c.field("part", intRange(r.Part, 1, MaxPart))
	return c.err
}

// PartAck is the outcome of acknowledging one part.
type PartAck struct {
	MessageID MessageID `json:"message_id"`
	Part      int       `json:"part"`
	Ack       ItemAck   `json:"ack"`
}

// Validate checks the entry.
func (a PartAck) Validate() error {
	var c checker
	c.field("message_id", a.MessageID.Validate())
	c.field("part", intRange(a.Part, 1, MaxPart))
	c.field("ack", enumOK(a.Ack, itemAcks))
	return c.err
}

// RecvObservedResult is the daemon's decision. When Verdict is allowed, Items
// holds the outcome for every part of the page; when refused it is empty and
// Reason says why, so the adapter can leave the page for an explicit ack.
type RecvObservedResult struct {
	Verdict AckVerdict `json:"verdict"`
	Reason  AckReason  `json:"reason"`
	Items   []PartAck  `json:"items"`
}

// Validate checks that verdict, reason and items agree.
func (r RecvObservedResult) Validate() error {
	var c checker
	c.field("verdict", enumOK(r.Verdict, ackVerdicts))
	c.field("reason", enumOK(r.Reason, ackReasons))
	c.field("items", present(r.Items))
	c.ok(len(r.Items) <= maxAckItems, "items: %d entries, at most %d", len(r.Items), maxAckItems)
	list(&c, "items", r.Items)
	if c.err != nil {
		return c.err
	}
	switch r.Verdict {
	case AckAllowed:
		c.ok(r.Reason == AckSeenInFull, "reason: an allowed acknowledgement was seen in full")
		c.ok(len(r.Items) >= 1, "items: an allowed acknowledgement settles at least one part")
	case AckRefused:
		c.field("reason", enumIn(r.Reason, AckReplaced, AckTruncated, AckContentMismatch, AckUnknownLease))
		c.ok(len(r.Items) == 0, "items: a refused acknowledgement settles nothing")
	}
	return c.err
}
