package adapterproto

// Limits of the direct-endpoint methods.
const (
	// MaxDisplayName is the most characters (Unicode code points) of the display
	// name an adapter registers a direct endpoint under. A name is at least one
	// character.
	MaxDisplayName = 128
	// MaxDescription is the most characters of the description of a registered
	// endpoint. A description that is present is at least one character.
	MaxDescription = 1024
	// MaxWaitSeconds bounds how long endpoint.recv may wait for a message.
	MaxWaitSeconds = 30
	// MaxRecipients bounds the recipients of one message.
	MaxRecipients          = 50
	maxAcceptBeforeSeconds = 1<<31 - 1
)

// RegisterParams registers a direct endpoint. Registering the same key again
// returns the same endpoint: a key is stable and chosen by the adapter.
type RegisterParams struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
}

// Validate checks the parameters.
func (p RegisterParams) Validate() error {
	var c checker
	c.field("key", strLen(p.Key, 1, MaxIdentLen))
	c.field("display_name", strLen(p.DisplayName, 1, MaxDisplayName))
	if p.Description != "" {
		c.field("description", strLen(p.Description, 1, MaxDescription))
	}
	return c.err
}

// RegisterResult names the endpoint.
type RegisterResult struct {
	Endpoint EndpointRef `json:"endpoint"`
}

// Validate checks the endpoint.
func (r RegisterResult) Validate() error { return at("endpoint", r.Endpoint.Validate()) }

// UnregisterParams closes a direct endpoint.
type UnregisterParams struct {
	Endpoint EndpointRef `json:"endpoint"`
}

// Validate checks the endpoint.
func (p UnregisterParams) Validate() error { return at("endpoint", p.Endpoint.Validate()) }

// PendingParams tells the adapter that messages wait for a direct endpoint. It is
// level-triggered and may be coalesced: it says there is something to read, not
// how many notifications were sent.
type PendingParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	Count    int         `json:"count"`
}

// Validate checks the parameters.
func (p PendingParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("count", intRange(p.Count, 1, 1<<31-1))
	return c.err
}

// RecvParams reads the messages waiting for a direct endpoint.
type RecvParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	// WaitS is how long to wait for a message when none is queued, in seconds,
	// 0 to MaxWaitSeconds. Absent means 0.
	WaitS *int `json:"wait_s,omitempty"`
	// MessageID and Part fetch one specific part of a message, as the page's
	// next pointer names it. They go together.
	MessageID MessageID `json:"message_id,omitempty"`
	Part      int       `json:"part,omitempty"`
}

// Validate checks the parameters.
func (p RecvParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	if p.WaitS != nil {
		c.field("wait_s", intRange(*p.WaitS, 0, MaxWaitSeconds))
	}
	if p.MessageID != "" {
		c.field("message_id", p.MessageID.Validate())
	}
	if p.Part != 0 {
		c.field("part", intRange(p.Part, 1, MaxPart))
	}
	c.ok((p.MessageID == "") == (p.Part == 0), "message_id and part go together")
	return c.err
}

// RecvResult is one page.
type RecvResult struct {
	Page RecvPage `json:"page"`
}

// Validate checks the page.
func (r RecvResult) Validate() error { return at("page", r.Page.Validate()) }

// AckParams acknowledges parts of a page that the adapter has shown to the
// person.
type AckParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	Lease    LeaseRef    `json:"lease"`
	Items    []PartRef   `json:"items"`
}

// Validate checks the parameters.
func (p AckParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("lease", p.Lease.Validate())
	c.ok(len(p.Items) >= 1 && len(p.Items) <= maxAckItems, "items: %d entries, need 1..%d", len(p.Items), maxAckItems)
	list(&c, "items", p.Items)
	return c.err
}

// AckResult has one entry per acknowledged part.
type AckResult struct {
	Items []PartAck `json:"items"`
}

// Validate checks the entries.
func (r AckResult) Validate() error {
	var c checker
	c.ok(len(r.Items) >= 1 && len(r.Items) <= maxAckItems, "items: %d entries, need 1..%d", len(r.Items), maxAckItems)
	list(&c, "items", r.Items)
	return c.err
}

// ControlParams are the parameters of a control signal.
type ControlParams struct {
	// Preset is the runtime preset session.create starts.
	Preset string `json:"preset"`
}

// Validate checks the preset name.
func (p ControlParams) Validate() error { return at("preset", ValidatePresetName(p.Preset)) }

// Control marks a message as a control signal for the target endpoint instead of
// text for its agent. The body of a session.create message is its initial
// message; an empty body means none.
type Control struct {
	Signal ControlSignal `json:"signal"`
	// Params is present for session.create and absent for the other signals.
	Params *ControlParams `json:"params,omitempty"`
}

// Validate checks that the parameters fit the signal.
func (c Control) Validate() error {
	var ck checker
	ck.field("signal", enumOK(c.Signal, controlSignals))
	if ck.err != nil {
		return ck.err
	}
	if c.Signal == SignalSessionCreate {
		ck.ok(c.Params != nil, "params: session.create names its preset")
		if c.Params != nil {
			ck.field("params", c.Params.Validate())
		}
	} else {
		ck.ok(c.Params == nil, "params: only session.create takes parameters")
	}
	return ck.err
}

// SendParams sends a message from a direct endpoint.
type SendParams struct {
	Endpoint    EndpointRef `json:"endpoint"`
	To          []Address   `json:"to"`
	ContentType string      `json:"content_type"`
	// Body may be empty.
	Body    string    `json:"body"`
	ReplyTo MessageID `json:"reply_to,omitempty"`
	Thread  string    `json:"thread,omitempty"`
	// DiscloseRecipients lets each recipient see the others.
	DiscloseRecipients bool `json:"disclose_recipients,omitempty"`
	// AcceptBeforeS is how long the message may wait for delivery, in seconds.
	// Absent means the channel's default.
	AcceptBeforeS  int64    `json:"accept_before_s,omitempty"`
	Control        *Control `json:"control,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

// Validate checks the parameters. A control signal has exactly one recipient. Address syntax, the number of recipients that
// is acceptable and the size of the body are judged by the daemon, which refuses
// with CodeInvalidAddress or CodeSendRefused.
func (p SendParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.ok(len(p.To) >= 1 && len(p.To) <= MaxRecipients, "to: %d recipients, need 1..%d", len(p.To), MaxRecipients)
	for i, a := range p.To {
		c.field("to"+index(i), a.Validate())
	}
	c.field("content_type", strLen(p.ContentType, 1, MaxIdentLen))
	if p.ReplyTo != "" {
		c.field("reply_to", p.ReplyTo.Validate())
	}
	if p.Thread != "" {
		c.field("thread", strLen(p.Thread, 1, MaxIdentLen))
	}
	if p.AcceptBeforeS != 0 {
		c.field("accept_before_s", intRange(p.AcceptBeforeS, 1, maxAcceptBeforeSeconds))
	}
	if p.Control != nil {
		c.ok(len(p.To) == 1, "to: a control signal has exactly one recipient, got %d", len(p.To))
		c.field("control", p.Control.Validate())
	}
	if p.IdempotencyKey != "" {
		c.field("idempotency_key", strLen(p.IdempotencyKey, 1, MaxIdentLen))
	}
	return c.err
}

// SendResult names the message the channel created.
type SendResult struct {
	MessageID MessageID `json:"message_id"`
}

// Validate checks the identifier.
func (r SendResult) Validate() error { return at("message_id", r.MessageID.Validate()) }

// MessageStatusParams asks for the delivery state of a message sent earlier.
type MessageStatusParams struct {
	Endpoint  EndpointRef `json:"endpoint"`
	MessageID MessageID   `json:"message_id"`
}

// Validate checks the parameters.
func (p MessageStatusParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("message_id", p.MessageID.Validate())
	return c.err
}

// RecipientStatus is the state of the delivery to one recipient.
type RecipientStatus struct {
	To    Address       `json:"to"`
	State DeliveryState `json:"state"`
	// Reason is present exactly for a rejected delivery.
	Reason RejectReason `json:"reason,omitempty"`
}

// Validate checks that a reason appears exactly with a rejection.
func (r RecipientStatus) Validate() error {
	var c checker
	c.field("to", r.To.Validate())
	c.field("state", enumOK(r.State, deliveryStates))
	if c.err != nil {
		return c.err
	}
	if r.State == DeliveryRejected {
		c.field("reason", enumOK(r.Reason, rejectReasons))
	} else {
		c.ok(r.Reason == "", "reason: only a rejected delivery has one")
	}
	return c.err
}

// MessageStatusResult is the state of each recipient's delivery and, for a
// message that carried a control signal, the reply once there is one.
type MessageStatusResult struct {
	Recipients   []RecipientStatus `json:"recipients"`
	ControlReply *ControlReply     `json:"control_reply,omitempty"`
}

// Validate checks the entries.
func (r MessageStatusResult) Validate() error {
	var c checker
	c.ok(len(r.Recipients) >= 1 && len(r.Recipients) <= MaxRecipients, "recipients: %d entries, need 1..%d", len(r.Recipients), MaxRecipients)
	list(&c, "recipients", r.Recipients)
	if r.ControlReply != nil {
		c.field("control_reply", r.ControlReply.Validate())
	}
	return c.err
}
