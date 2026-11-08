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
	c.field("display_name", ValidateDisplayName(p.DisplayName))
	c.field("description", ValidateOptionalDescription(p.Description))
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
	c.ok(len(p.Items) >= 1 && len(p.Items) <= MaxAckItems, "items: %d entries, need 1..%d", len(p.Items), MaxAckItems)
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
	c.ok(len(r.Items) >= 1 && len(r.Items) <= MaxAckItems, "items: %d entries, need 1..%d", len(r.Items), MaxAckItems)
	list(&c, "items", r.Items)
	return c.err
}

// ControlParams are the parameters of a control signal.
type ControlParams struct {
	// Preset is the runtime preset session.create starts.
	Preset string `json:"preset"`
	// Model and Effort choose the new session's model and reasoning effort within
	// what the preset allows; absent takes the preset's own.
	Model  string `json:"model,omitempty"`
	Effort Effort `json:"effort,omitempty"`
}

// Validate checks the preset name and, when present, the model and the effort.
func (p ControlParams) Validate() error {
	var c checker
	c.field("preset", ValidatePresetName(p.Preset))
	checkChoice(&c, p.Model, p.Effort)
	return c.err
}

// Control marks a message as a control signal for the target endpoint instead of
// text for its agent. The body of a session.create message is its initial
// message; an empty body means none. The other signals carry no body.
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
	Endpoint EndpointRef `json:"endpoint"`
	To       []Address   `json:"to"`
	// ContentType is present exactly when Body is not empty.
	ContentType string `json:"content_type,omitempty"`
	// Body is what the message says. A message has a body: it is not empty unless
	// Control is set, where it is the initial message of a session.create (empty
	// means none) and is empty for the other signals, which carry none.
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

// Validate checks the parameters. A message without a control signal has a
// non-empty body, and a control signal other than session.create has an empty
// one, as the envelope the daemon builds from it requires; the content type is
// present exactly when the body is. A control signal has exactly one recipient. Address syntax, the number of recipients that
// is acceptable and the size of the body are judged by the daemon, which refuses
// with CodeInvalidAddress or CodeSendRefused.
func (p SendParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.ok(len(p.To) >= 1 && len(p.To) <= MaxRecipients, "to: %d recipients, need 1..%d", len(p.To), MaxRecipients)
	for i, a := range p.To {
		c.field("to"+index(i), a.Validate())
	}
	c.ok(p.Control != nil || p.Body != "", "body: a message has a body")
	if p.Body != "" {
		c.field("content_type", strLen(p.ContentType, 1, MaxIdentLen))
	} else {
		c.ok(p.ContentType == "", "content_type: a message without a body has no content type")
	}
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
		c.ok(p.Control.Signal == SignalSessionCreate || p.Body == "", "body: %s carries no body", p.Control.Signal)
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

// Limits of the directory.
const (
	// MaxDirectoryLimit is the most entries one page of endpoint.directory holds.
	// A remote directory page can hold up to 256 entries of up to 5 KiB each, which
	// is more than a line of the protocol (1 MiB) can carry, and a description may be
	// 1024 characters of four bytes each (a character is at most six bytes when
	// encoding/json escapes it), so that even 128 entries come close to the line.
	// A hundred leave room: with the longest display name and description too,
	// the largest page is about 480 KiB on the wire and about 700 KiB as
	// encoding/json writes it.
	MaxDirectoryLimit = 100
	// DefaultDirectoryLimit is the page size the node reads when the request names
	// no limit. The node applies it, so a test holds it equal to the node's own.
	DefaultDirectoryLimit = 100
	// MaxCursorLen bounds a directory cursor, an opaque position the target made.
	MaxCursorLen = 256
)

// DirectoryParams asks for a page of the endpoints a node shows to a direct
// endpoint. What it shows is judged by the node asked, for the endpoint that
// asks, as for an agent's session endpoint.
type DirectoryParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	// Node names the node to ask, as a PeersResult names it. Absent means this
	// node. It is opaque to the adapter, and the daemon resolves it.
	Node string `json:"node,omitempty"`
	// Cursor continues a listing: the next_cursor of the page before. It is opaque,
	// is good only for the endpoint and the daemon process it was made for, and
	// grants nothing: every page is judged anew.
	Cursor string `json:"cursor,omitempty"`
	// Limit is the most entries wanted, 1 to MaxDirectoryLimit. Absent means
	// DefaultDirectoryLimit.
	Limit int `json:"limit,omitempty"`
}

// Validate checks the parameters.
func (p DirectoryParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	if p.Node != "" {
		c.field("node", strLen(p.Node, 1, MaxIdentLen))
	}
	if p.Cursor != "" {
		c.field("cursor", strLen(p.Cursor, 1, MaxCursorLen))
	}
	if p.Limit != 0 {
		c.field("limit", intRange(p.Limit, 1, MaxDirectoryLimit))
	}
	return c.err
}

// DirectoryReason says why a directory read did not give a page. It is an open
// word, as the reason of a send-refused error is: a reader that does not know
// one takes the outcome alone. For DirectoryUnavailable it is the name this
// version of the node sends (DirectoryReasons); for DirectoryRefused it is the
// name the target gave its refusal, passed on unchanged. A name once sent is
// never renamed, removed or reused.
type DirectoryReason string

// DirectoryReasonUnavailable is the reason of DirectoryUnavailable: the target
// gave no answer within the time allowed, could not be reached, or failed
// without saying why. The node does not tell these apart.
const DirectoryReasonUnavailable DirectoryReason = "directory_unavailable"

// DirectoryReasons lists the reasons this version of the node sends with
// DirectoryUnavailable, in order. A refusal's reason is the target's and is not
// listed.
func DirectoryReasons() []string { return []string{string(DirectoryReasonUnavailable)} }

// DirectoryEntry describes one endpoint a node shows.
type DirectoryEntry struct {
	// Address is the endpoint's address, '<node>/<endpoint>'.
	Address Address      `json:"address"`
	Kind    EndpointKind `json:"kind"`
	// DisplayName is what the endpoint is called; absent when it has none.
	DisplayName string `json:"display_name,omitempty"`
	// Description is absent when the endpoint has none.
	Description string `json:"description,omitempty"`
	// Accepts are the actions the endpoint accepts. A required list that may be
	// empty.
	Accepts []EndpointAction `json:"accepts"`
	// Adapter says which adapter serves the endpoint; absent for an endpoint no
	// adapter serves, and when the node that showed the entry did not say.
	Adapter *DirectoryAdapter `json:"adapter,omitempty"`
}

// DirectoryAdapter is the adapter instance behind a directory entry: the instance
// that serves the endpoint on its node, the adapter it runs and, when the node
// knows them, the adapter's version and the native runtime it drives. An agent
// that wants a given runtime picks the entry by ID.
type DirectoryAdapter struct {
	// Instance is the name of the adapter instance on the node that shows the
	// entry, which follows the syntax of an adapter identifier.
	Instance string `json:"instance"`
	// ID is the identifier of the adapter the instance runs.
	ID string `json:"id"`
	// Version is the adapter's version as it announced itself when it last
	// connected; absent while it has not connected since the node started.
	Version string `json:"version,omitempty"`
	// Runtime is the native runtime the adapter found when its probe last passed;
	// absent for an endpoint that is no runtime or session endpoint, and while no
	// probe has passed.
	Runtime *RuntimeInfo `json:"runtime,omitempty"`
}

// Validate checks the identifiers, the version and the runtime.
func (a DirectoryAdapter) Validate() error {
	var c checker
	c.field("instance", ValidateAdapterID(a.Instance))
	c.field("id", ValidateAdapterID(a.ID))
	if a.Version != "" {
		c.field("version", ValidateSemVer(a.Version))
	}
	if a.Runtime != nil {
		c.field("runtime", a.Runtime.Validate())
	}
	return c.err
}

// Validate checks the entry.
func (e DirectoryEntry) Validate() error {
	var c checker
	c.field("address", e.Address.Validate())
	c.field("kind", enumOK(e.Kind, endpointKinds))
	c.field("display_name", ValidateOptionalDisplayName(e.DisplayName))
	c.field("description", ValidateOptionalDescription(e.Description))
	c.field("accepts", present(e.Accepts))
	for i, a := range e.Accepts {
		c.field("accepts"+index(i), enumOK(a, endpointActions))
	}
	c.field("accepts", unique(e.Accepts))
	if e.Adapter != nil {
		c.field("adapter", e.Adapter.Validate())
	}
	return c.err
}

// DirectoryResult is the outcome of a directory read and, with DirectoryOK, the
// page.
//
//   - DirectoryOK: Endpoints is the page, in endpoint order, and NextCursor is
//     present when more follow.
//   - DirectoryUnavailable and DirectoryRefused: Reason says why, and the page
//     is empty with no cursor.
type DirectoryResult struct {
	Outcome   DirectoryOutcome `json:"outcome"`
	Reason    DirectoryReason  `json:"reason,omitempty"`
	Endpoints []DirectoryEntry `json:"endpoints"`
	// NextCursor continues the listing; absent at its end.
	NextCursor string `json:"next_cursor,omitempty"`
}

// Validate checks that a reason appears exactly without a page, and that a page
// fits a line.
func (r DirectoryResult) Validate() error {
	var c checker
	c.field("outcome", enumOK(r.Outcome, directoryOutcomes))
	if c.err != nil {
		return c.err
	}
	c.field("endpoints", present(r.Endpoints))
	c.ok(len(r.Endpoints) <= MaxDirectoryLimit, "endpoints: %d entries, at most %d", len(r.Endpoints), MaxDirectoryLimit)
	list(&c, "endpoints", r.Endpoints)
	if r.NextCursor != "" {
		c.field("next_cursor", strLen(r.NextCursor, 1, MaxCursorLen))
	}
	if r.Outcome == DirectoryOK {
		c.ok(r.Reason == "", "reason: a page has none")
	} else {
		c.field("reason", reasonWordOK(string(r.Reason)))
		c.ok(len(r.Endpoints) == 0, "endpoints: %s gives no page", r.Outcome)
		c.ok(r.NextCursor == "", "next_cursor: %s gives no page", r.Outcome)
	}
	return c.err
}

// PeersParams asks which nodes a direct endpoint can address.
type PeersParams struct {
	Endpoint EndpointRef `json:"endpoint"`
}

// Validate checks the endpoint.
func (p PeersParams) Validate() error { return at("endpoint", p.Endpoint.Validate()) }

// PeerNode names a node the daemon's node can address.
type PeerNode struct {
	// Node is the name to put in DirectoryParams.Node. It is opaque.
	Node string `json:"node"`
	// Self is true for the daemon's own node, whose Trust is PeerSelf.
	Self  bool      `json:"self"`
	Trust PeerTrust `json:"trust"`
	// Connected says whether a connection to the node is up now. It is read
	// without waiting and says nothing about whether a send would succeed.
	Connected bool `json:"connected"`
	// Profile is what the node says about itself; absent while the daemon has
	// none, which it does not for a node it has not been connected to since it
	// started.
	Profile *PeerProfile `json:"profile,omitempty"`
}

// PeerProfile is what a node says about itself, so that people and agents can
// tell nodes apart: its name and the machine and the version of Sidera it runs.
// Every text is optional, and absent when the node does not say it. The texts are
// shown to a person, so each follows the rule of a display name; the form the
// node gives a platform ("<goos>/<goarch>") is its own and is not required of a
// reader.
type PeerProfile struct {
	// Name is what people call the node: the name its owner gave it, or its host
	// name.
	Name string `json:"name,omitempty"`
	// HostName is the first label of the machine's host name.
	HostName string `json:"host_name,omitempty"`
	// Platform is the machine's operating system and architecture, written
	// "<goos>/<goarch>", for example "linux/amd64".
	Platform string `json:"platform,omitempty"`
	// System is the operating system and its release, for example "Ubuntu 24.04.1
	// LTS".
	System string `json:"system,omitempty"`
	// SystemBuild is the build or kernel version of the system.
	SystemBuild string `json:"system_build,omitempty"`
	// SideraVersion is the version of Sidera the node runs.
	SideraVersion string `json:"sidera_version,omitempty"`
	// AsOf is when the daemon's node got the profile, in Unix seconds: for the
	// node itself, when it issued it; for another node, when it fetched it.
	AsOf int64 `json:"as_of"`
}

// Validate checks the texts and the time.
func (p PeerProfile) Validate() error {
	var c checker
	c.field("name", ValidateOptionalDisplayName(p.Name))
	c.field("host_name", ValidateOptionalDisplayName(p.HostName))
	c.field("platform", ValidateOptionalDisplayName(p.Platform))
	c.field("system", ValidateOptionalDisplayName(p.System))
	c.field("system_build", ValidateOptionalDisplayName(p.SystemBuild))
	c.field("sidera_version", ValidateOptionalDisplayName(p.SideraVersion))
	c.field("as_of", intRange(p.AsOf, 1, MaxTimestampS))
	return c.err
}

// Validate checks the node, that self and trust agree, and the profile.
func (n PeerNode) Validate() error {
	var c checker
	c.field("node", strLen(n.Node, 1, MaxIdentLen))
	c.field("trust", enumOK(n.Trust, peerTrusts))
	if c.err == nil {
		c.ok(n.Self == (n.Trust == PeerSelf), "trust: %s does not go with self=%v", n.Trust, n.Self)
	}
	if n.Profile != nil {
		c.field("profile", n.Profile.Validate())
	}
	return c.err
}

// PeersResult lists the nodes: this node first, then every node it trusts
// directly, in the order of their names.
type PeersResult struct {
	Nodes []PeerNode `json:"nodes"`
}

// Validate checks the entries.
func (r PeersResult) Validate() error {
	var c checker
	c.field("nodes", present(r.Nodes))
	list(&c, "nodes", r.Nodes)
	return c.err
}

// DeclareParams changes the declaration of a direct endpoint: what it will take
// and whether it shows in directories, as the declare of the Agent interface does
// for a session endpoint. It is a patch: a member that is absent stays as it is.
// The patch must change something, and the node judges that and the list of
// senders (its bound, and a sender named twice), which are the node's rules, with
// ParamsEmptyPatch, ParamsTooManySenders and ParamsDuplicateSender.
type DeclareParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	// Paused: the endpoint takes no new message while it is set.
	Paused *bool `json:"paused,omitempty"`
	// RefuseMultiRecipient: a message with several recipients is refused.
	RefuseMultiRecipient *bool `json:"refuse_multi_recipient,omitempty"`
	// RestrictSenders: only the senders of AcceptFrom may send.
	RestrictSenders *bool `json:"restrict_senders,omitempty"`
	// AcceptFrom replaces the whole list of senders. An explicit empty list clears
	// it; absent leaves it.
	AcceptFrom *[]Address `json:"accept_from,omitempty"`
	// HiddenFromDirectory: the endpoint does not show in the directory.
	HiddenFromDirectory *bool `json:"hidden_from_directory,omitempty"`
}

// Validate checks the endpoint and the addresses.
func (p DeclareParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	if p.AcceptFrom != nil {
		c.field("accept_from", present(*p.AcceptFrom))
		list(&c, "accept_from", *p.AcceptFrom)
	}
	return c.err
}

// Declaration is what an endpoint declares about itself.
type Declaration struct {
	Paused               bool      `json:"paused"`
	RefuseMultiRecipient bool      `json:"refuse_multi_recipient"`
	RestrictSenders      bool      `json:"restrict_senders"`
	AcceptFrom           []Address `json:"accept_from"`
	HiddenFromDirectory  bool      `json:"hidden_from_directory"`
}

// Validate checks the list of senders.
func (d Declaration) Validate() error {
	var c checker
	c.field("accept_from", present(d.AcceptFrom))
	list(&c, "accept_from", d.AcceptFrom)
	return c.err
}

// DeclareResult is the declaration after the patch was applied.
type DeclareResult struct {
	Declaration Declaration `json:"declaration"`
}

// Validate checks the declaration.
func (r DeclareResult) Validate() error { return at("declaration", r.Declaration.Validate()) }

// ParamsReason says which misuse a -32602 error of endpoint.directory or
// endpoint.declare stands for, in the member reason of its data. The node adds
// it where an adapter can act on it; every other invalid-params error carries
// text for a person only.
type ParamsReason string

const (
	// ParamsCursorInvalid: the cursor is malformed, was made for another endpoint
	// or by another process of the daemon. Start the listing again.
	ParamsCursorInvalid ParamsReason = "cursor_invalid"
	// ParamsEmptyPatch: the declaration patch changes nothing.
	ParamsEmptyPatch ParamsReason = "empty_patch"
	// ParamsTooManySenders: accept_from would hold more senders than a node allows.
	ParamsTooManySenders ParamsReason = "too_many_senders"
	// ParamsDuplicateSender: accept_from names a sender twice.
	ParamsDuplicateSender ParamsReason = "duplicate_sender"
)

var paramsReasons = []ParamsReason{ParamsCursorInvalid, ParamsEmptyPatch, ParamsTooManySenders, ParamsDuplicateSender}

// ParamsReasons lists the reasons this version of the node sends in the data of
// an invalid-params error, in order. Like the reason of a node-busy error they
// are words a reader may act on, and a reader that does not know one treats the
// error as a plain invalid-params.
func ParamsReasons() []string { return strs(paramsReasons) }
