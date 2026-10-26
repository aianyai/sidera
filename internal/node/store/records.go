package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/aianyai/sidera/internal/node/recfile"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// BodyRef names a body file: the SHA-256 of the content, which is the file
// name, and its length.
type BodyRef struct {
	Digest [sha256.Size]byte
	Len    int
}

// BodyRefOf is the reference of a body.
func BodyRefOf(content []byte) BodyRef {
	return BodyRef{Digest: sha256.Sum256(content), Len: len(content)}
}

// Name is the name of the body file: the lowercase hex of the digest.
func (r BodyRef) Name() string { return hex.EncodeToString(r.Digest[:]) }

// OutRecord is the source side record of one message or control signal: the
// sender's node's view of it. It has two shapes, exactly one of which is used.
// The regular shape holds the envelope.Outbound and what is needed to send the
// message again. The lost shape is what is left of a record that was damaged:
// only the discovery time and one delivery record per recipient of the key.
type OutRecord struct {
	Key OutKey

	// Lost is set for the lost shape; Outbound and everything below it are
	// then zero.
	Lost *LostOut

	Outbound envelope.Outbound
	Envelope OutEnvelope
	// Body is nil when the message has no body or when the body was released:
	// after every recipient's receipt is applied, only BodyDigest remains.
	Body       *BodyRef
	BodyDigest delivery.BodyDigest
	// Idempotency is nil when the caller gave no key.
	Idempotency *Idempotency
	// ControlReply is the control reply record as it was received, kept as
	// evidence until the retention of reply facts ends.
	ControlReply []byte
	// Feedback is the feedback decided for this record's deliveries and not yet
	// handed to the owner's endpoint. It is stored with the record that
	// decided it, so that no crash can mark a delivery reported without the
	// feedback being there.
	Feedback []delivery.Item
}

// LostOut is the lost shape of a source record.
type LostOut struct {
	DiscoveredAt time.Time
	// Deliveries holds one record per recipient of the key, in the key's order.
	Deliveries []delivery.Record
}

// OutEnvelope is what the source keeps of the envelope to send it again.
type OutEnvelope struct {
	// To lists every recipient of the message, whatever node each is on.
	To []ident.Address
	// DiscloseRecipients is the sender's choice to let each recipient see the
	// whole list. The digest of the envelope covers it, so a message sent again
	// carries the flag it was first sent with. Whether the message has several
	// recipients is len(To) > 1.
	DiscloseRecipients bool
	ReplyTo            delivery.MessageID
	Thread             string
	Seq                map[ident.EndpointID]uint64
	SentAt             time.Time
	ReportBy           time.Time
	ContentType        string
	// Preset is the parameter of a session creation.
	Preset string
}

// Idempotency is the caller's idempotency key of a message and how long the
// node remembers it.
type Idempotency struct {
	Key         string
	RetainUntil time.Time
}

// Name is the file name of the record.
func (r OutRecord) Name() string { return r.Key.FileName() }

// NeedsBody reports whether the record still needs the bytes of its body: one of
// its deliveries (a recipient's, or the derived one of a session creation) is
// still queued. A lost record has no body.
func (r OutRecord) NeedsBody() bool {
	if r.Lost != nil {
		return false
	}
	for _, t := range r.Outbound.Recipients {
		if t.Delivery.NeedsBody() {
			return true
		}
	}
	return r.Outbound.Derived != nil && r.Outbound.Derived.Delivery.NeedsBody()
}

// OutLostRecord is the notice that a file of out/ whose first line could not be
// read, so that nothing said which message it held, was lost: the one thing that
// is known is the sending endpoint, from the name of the file. It is written when
// the damaged file is settled, holds the one feedback item that tells the endpoint
// until a notice covers it, and is swept once the item is gone.
type OutLostRecord struct {
	Key OutLostKey
	// DiscoveredAt is when the damage was found.
	DiscoveredAt time.Time
	// Feedback is empty or the one item delivery.NewLostUnknownMessageItem builds
	// for the sender, this record and the discovery time: the pending feedback,
	// cleared by the notice that covers it.
	Feedback []delivery.Item
}

// NewOutLostRecord is the out-lost record of the damaged file of key, found at
// at by the node, with its feedback item pending. It is the one place that gives
// the record its shape.
func NewOutLostRecord(key OutLostKey, node ident.NodeID, at time.Time) OutLostRecord {
	r := OutLostRecord{Key: key, DiscoveredAt: at}
	r.Feedback = []delivery.Item{delivery.NewLostUnknownMessageItem(ident.Address{Node: node, Endpoint: key.Sender}, r.Name(), at)}
	return r
}

// Name is the file name of the record. It panics for a record whose key is not
// valid: every path that names a record from outside the store checks the key
// first, so a record that gets here without one is a programming error.
func (r OutLostRecord) Name() string {
	key, err := r.Key.Bytes()
	if err != nil {
		panic(fmt.Sprintf("store: the name of an out-lost record with an invalid key: %v", err))
	}
	return outFileName(KindOutLost, key)
}

// InRecord is the target side record of one message for one endpoint, a
// deduplication record included. A target keeps no replacement of a damaged
// record: the sender learns of the loss when the target answers a pull that it
// holds no record.
type InRecord struct {
	Key TargetKey
	// Rev is the revision of the record as it was read, 0 for a record not
	// written yet. Writing a record stores Rev+1. Nothing but the replay of a
	// prompt or a block reads it, to tell whether a record was written since.
	Rev uint64

	Record delivery.Record
	Dedup  delivery.DedupRecord

	Auth          delivery.Authorization
	ReplyDecision reply.Decision
	Handout       HandoutFields
	AcceptedAt    time.Time
	Body          *BodyRef
	// Derived is set for the delivery of a session's initial message to the
	// session it created.
	Derived *DerivedInfo
	// Covers is set for a system notice: the feedback items it stands for.
	Covers []CoverRef
}

// HandoutFields is what the target needs from the envelope to hand a message
// out.
type HandoutFields struct {
	ContentType string
	ReplyTo     delivery.MessageID
	Thread      string
	// Seq is 0 for a record that has none: a derived delivery or a notice.
	Seq    uint64
	SentAt time.Time
}

// DerivedInfo marks a derived delivery.
type DerivedInfo struct {
	// From is the endpoint the control signal was addressed to.
	From ident.EndpointID
	// AuthRequest is the shape of the authorization request that let the
	// derived delivery in, kept as compact JSON for the second check.
	AuthRequest []byte
}

// CoverRef names one feedback item a notice covers: the source record that
// holds the item, the recipient and the kind of feedback.
type CoverRef struct {
	Out       string // file name of the source record
	Recipient ident.Address
	Kind      delivery.Kind
}

// Name is the file name of the record.
func (r InRecord) Name() string { return r.Key.FileName(KindIn) }

// IntentState is the progress of a session creation.
type IntentState uint8

const (
	IntentCreating    IntentState = iota + 1 // handed to the adapter, outcome not recorded
	IntentCreated                            // the adapter reported a session
	IntentUnconfirmed                        // the outcome could not be established
	IntentFailed                             // the adapter reported a failure
)

// CreateIntent is the creation of a session as a target control record
// remembers it: written before the adapter is called, completed with its result.
type CreateIntent struct {
	Attempt  adapterproto.AttemptID
	Endpoint ident.EndpointID
	State    IntentState
	// NativeRef and Process are set when State is IntentCreated.
	NativeRef string
	Process   *adapterproto.ProcessIdentity
}

// CtlRecord is the target side record of a control signal, with the stored
// control reply and, for a session creation, the creation intent. Like InRecord
// it has no replacement of a damaged one.
type CtlRecord struct {
	Key TargetKey

	Record delivery.Record
	// Dedup holds the stored control reply, once there is one.
	Dedup delivery.DedupRecord

	Kind             envelope.Kind
	Preset           string
	SessionEndpoint  ident.EndpointID
	AcceptedAt       time.Time
	EndpointMaxQueue time.Duration
	SentAt           time.Time
	// Body is the body of the initial message of a session creation.
	Body   *BodyRef
	Intent *CreateIntent
	// ClosedEndpoint is the session endpoint a successful session.close of this
	// record closed; zero otherwise.
	ClosedEndpoint ident.EndpointID
	// Result is the execution result, compact JSON, owned by the engine.
	Result []byte
}

// Name is the file name of the record.
func (r CtlRecord) Name() string { return r.Key.FileName(KindCtl) }

// EndpointStatus is the state of an endpoint record.
type EndpointStatus uint8

const (
	// EndpointCreating is a session endpoint whose creation has not ended. It
	// is preparation: one without a control record is garbage.
	EndpointCreating EndpointStatus = iota + 1
	EndpointOpen
	// EndpointClosed is absorbing: a closed session endpoint is never opened
	// again.
	EndpointClosed
)

// EndpointRecord is the runtime state of an endpoint.
type EndpointRecord struct {
	ID       ident.EndpointID
	Kind     adapterreg.EndpointKind
	Instance string
	// DisplayName is the name people see for the endpoint, as its adapter
	// registered it; empty when it has none. At most adapterproto.MaxDisplayName
	// characters of valid UTF-8.
	DisplayName string
	// Description says what the endpoint is for, as its adapter registered it;
	// empty when it has none. At most adapterproto.MaxDescription characters of
	// valid UTF-8.
	Description string
	Status      EndpointStatus
	// Token is the 32-byte token; nil once the endpoint is closed.
	Token []byte
	// Declaration is the endpoint's declaration, compact JSON, owned by the
	// engine; nil when there is none.
	Declaration []byte
	Session     *SessionInfo
	// CreatedBy is the file name hash, without ".rec", of the control record
	// that created a session endpoint.
	CreatedBy string
}

// SessionInfo is what a session endpoint maps to.
type SessionInfo struct {
	Preset     string
	NativeRef  string
	Process    *adapterproto.ProcessIdentity
	ToolAccess []string
}

// empty reports whether the mapping says nothing. Such a mapping is neither
// written nor read: an endpoint without one has a nil Session.
func (s SessionInfo) empty() bool {
	return s.Preset == "" && s.NativeRef == "" && s.Process == nil && len(s.ToolAccess) == 0
}

// Name is the file name of the record.
func (r EndpointRecord) Name() string {
	return recfile.FileName(string(KindEndpoint), EndpointKeyBytes(r.ID))
}

// PendingEvent is the event a prompt or block applied to the in records its
// head record lists.
type PendingEvent uint8

const (
	PendingNone PendingEvent = iota
	PendingNotified
	PendingBlocked
	PendingUnblocked
)

// PendingItem is one follower of a head record: a target record, the revision
// it had before the head was written and its acceptance time. The pair tells a
// record that has not been written since from one that was, and from a new
// record that took the same key after the old one was removed.
type PendingItem struct {
	Key        TargetKey
	Rev        uint64
	AcceptedAt time.Time
}

// Pending is the set of followers a head record's transaction still owes.
type Pending struct {
	Event PendingEvent
	Cause delivery.Cause
	Items []PendingItem
}

// InFlightPrompt is a prompt handed to the adapter whose result is not yet
// recorded.
type InFlightPrompt struct {
	Attempt adapterproto.AttemptID
	Kind    handoff.PromptKind
	Covers  []TargetKey
}

// AttemptsRecord is the prompt attempts and blocked report of one endpoint.
type AttemptsRecord struct {
	Endpoint ident.EndpointID
	Attempts []handoff.Attempt
	Blocked  handoff.BlockedReport
	InFlight *InFlightPrompt
	Pending  Pending
}

// Name is the file name of the record.
func (r AttemptsRecord) Name() string {
	return recfile.FileName(string(KindAttempts), EndpointKeyBytes(r.Endpoint))
}

// CheckpointRecord is an adapter's checkpoint.
type CheckpointRecord struct {
	Key CheckpointKey
	// State is a compact JSON object, at most 16 KiB.
	State []byte
}

// Name is the file name of the record.
func (r CheckpointRecord) Name() string {
	b, _ := CheckpointKeyBytes(r.Key)
	return recfile.FileName(string(KindCheckpoint), b)
}

// AdminRecord is the result and fingerprint of an administrative operation.
type AdminRecord struct {
	Key         admin.RequestKey
	Fingerprint admin.Fingerprint
	Op          string
	// Result is compact JSON, owned by the administration code.
	Result      []byte
	At          time.Time
	RetainUntil time.Time
}

// Name is the file name of the record.
func (r AdminRecord) Name() string { return recfile.FileName(string(KindAdmin), AdminKeyBytes(r.Key)) }

// AlertAcked is the acknowledgement of an alert.
type AlertAcked struct {
	At        time.Time
	Operation admin.OperationID
}

// AlertRecord is an owner alert.
type AlertRecord struct {
	ID AlertID
	// Kind is a name of the form [a-z][a-z0-9_]{0,63}.
	Kind    string
	At      time.Time
	Detail  string
	Subject string
	Acked   *AlertAcked
}

// Name is the file name of the record.
func (r AlertRecord) Name() string { return recfile.FileName(string(KindAlert), AlertKeyBytes(r.ID)) }

// SeqRecord holds the floors of one sending endpoint: for each target address
// the highest sequence number that may have been used for it, remembered
// because the records that showed it are going to be deleted.
type SeqRecord struct {
	Sender ident.EndpointID
	Floors map[ident.Address]uint64
}

// Name is the file name of the record.
func (r SeqRecord) Name() string {
	return recfile.FileName(string(KindSeq), EndpointKeyBytes(r.Sender))
}
