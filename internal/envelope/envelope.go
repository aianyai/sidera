package envelope

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/frame"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Type is the type string of the envelope. It carries the version: an
// incompatible change is a new type string.
const Type = "sidera/envelope/1"

// Limits on the free-form fields. They bound what a hostile peer can make a
// node store and parse.
const (
	// MaxIDLen is the longest message id or thread, in bytes.
	MaxIDLen = 128
	// MaxContentTypeLen is the longest content type, in bytes.
	MaxContentTypeLen = 128
	// MaxEvidence is the most evidence records an envelope carries: a control
	// reply and a status report.
	MaxEvidence = 2
)

// MaxBody is the longest body in bytes. Sender and receiver must agree on it,
// which is why it is a protocol constant and not a node setting: a body the
// sender may send must be one every receiver accepts. 64 KiB holds a long code
// excerpt or log in one message and is about eight parts at the default recv
// output budget; larger content is passed on by the agent some other way.
// 256 KiB would be about 29 parts, and 16 KiB is too small for common
// excerpts.
const MaxBody = 64 * 1024

// MaxRecipients is the most recipients one envelope lists. It is the bound the
// adapter protocol already applies to one message, and the envelope reads it
// from there so that the two can never differ: a message an adapter may send
// must fit in the envelope. 50 lets one message reach every endpoint of a node
// at the scale the channel is sized for, about 50 endpoints; a node may publish
// up to publication.MaxEndpointsPerNode (256), so this is a sizing choice, not
// a promise that one message can address every endpoint. Each recipient costs
// about 96 bytes on the wire, so the most an envelope can list adds under 5
// KiB, and the body is stored once however many recipients there are.
const MaxRecipients = adapterproto.MaxRecipients

// Limits are the parameters of decoding and encoding. A zero value is refused
// rather than read as "no limit". They are parameters so that a caller can
// tighten them; DefaultLimits returns the protocol values.
type Limits struct {
	// MaxBody is the longest body in bytes. It is the envelope-wide limit; the
	// per-endpoint size caps of the policy are separate and judged by authz.
	MaxBody int
	// MaxRecipients is the most recipients one envelope lists.
	MaxRecipients int
}

// DefaultLimits returns the protocol values. A caller may tighten either limit;
// a receiver that sets a limit above the protocol value would accept messages
// that other nodes refuse.
func DefaultLimits() Limits {
	return Limits{MaxBody: MaxBody, MaxRecipients: MaxRecipients}
}

func (l Limits) check() error {
	if l.MaxBody <= 0 || l.MaxRecipients <= 0 {
		return fmt.Errorf("%w: body %d, recipients %d", ErrLimits, l.MaxBody, l.MaxRecipients)
	}
	return nil
}

// Bytes the wire form of one recipient needs at most: an address, a sequence
// entry and the map framing around them.
const perRecipientBytes = 96

// fixedBytes covers the fields other than recipients, evidence and body: ids,
// content type, times, flags and map framing.
const fixedBytes = 1024

// inputLimit is the longest encoded envelope the limits allow. It is derived,
// so that the size check can come first, before any parsing.
func (l Limits) inputLimit() int {
	return fixedBytes + l.MaxBody + l.MaxRecipients*perRecipientBytes + MaxEvidence*(record.DefaultMaxInput+16)
}

// Envelope is one message as it travels from a sender's node to one target
// node. The sender is not part of it: the receiving node knows it from the
// connection. The envelope is not signed; the status reports and control
// replies it carries as evidence are.
//
// One envelope goes to one target node. To lists the recipients on that node
// and, when the sender discloses its recipients, the others too. Sequence
// numbers and the multi-recipient flag are the sender's, one per recipient.
type Envelope struct {
	// ID is the sender-generated message identifier, the same for every retry,
	// path and recipient.
	ID delivery.MessageID
	// To lists the recipients. Without DiscloseRecipients, only those on the
	// target node.
	To []ident.Address
	// MultiRecipient says that the message as sent has more than one recipient,
	// whatever To shows after trimming. A target that refuses multi-recipient
	// messages reads this and not len(To).
	MultiRecipient bool
	// DiscloseRecipients lets each recipient see the whole list. It is the
	// sender's choice and says nothing about how many recipients there are.
	DiscloseRecipients bool
	Kind               Kind
	// ReplyTo is the message this one answers; empty if none, and always empty
	// for a control signal. The channel reads it only to judge reply permission.
	ReplyTo delivery.MessageID
	// Thread is a grouping the sender chooses; the channel does not interpret
	// it. Empty if none.
	Thread string
	// Evidence is what a reply carries to prove that the message it answers
	// arrived: at most one control reply, then at most one status report, in
	// that order, each signed by the node that was the target of ReplyTo. A
	// control signal carries none.
	Evidence []record.Record
	// Seq is the sender's sequence number for each recipient, from 1. For the
	// recipients only; the channel does not order by it.
	Seq          map[ident.EndpointID]uint64
	SentAt       time.Time
	AcceptBefore time.Time
	// ReportBy must be delivery.ReportBy(AcceptBefore). The cap on
	// AcceptBefore is judged by delivery.Intake, not here.
	ReportBy    time.Time
	ContentType string
	Body        []byte
	// Preset is the parameter of control.session.create: the only params any
	// kind has. Required for that kind, absent for all others.
	Preset string
}

type addressWire struct {
	Node     []byte `cbor:"1,keyasint"`
	Endpoint []byte `cbor:"2,keyasint"`
}

type seqWire struct {
	Endpoint []byte `cbor:"1,keyasint"`
	Seq      uint64 `cbor:"2,keyasint"`
}

type paramsWire struct {
	Preset string `cbor:"1,keyasint"`
}

type envelopeWire struct {
	Type               string        `cbor:"1,keyasint"`
	ID                 string        `cbor:"2,keyasint"`
	To                 []addressWire `cbor:"3,keyasint"`
	MultiRecipient     bool          `cbor:"4,keyasint"`
	DiscloseRecipients bool          `cbor:"5,keyasint"`
	Kind               uint64        `cbor:"6,keyasint"`
	ReplyTo            string        `cbor:"7,keyasint,omitempty"`
	Thread             string        `cbor:"8,keyasint,omitempty"`
	Evidence           [][]byte      `cbor:"9,keyasint,omitempty"`
	Seq                []seqWire     `cbor:"10,keyasint"`
	SentAt             int64         `cbor:"11,keyasint"`
	AcceptBefore       int64         `cbor:"12,keyasint"`
	ReportBy           int64         `cbor:"13,keyasint"`
	ContentType        string        `cbor:"14,keyasint,omitempty"`
	Body               []byte        `cbor:"15,keyasint,omitempty"`
	Params             *paramsWire   `cbor:"16,keyasint,omitempty"`
}

// checkToken accepts 1 to MaxIDLen bytes of visible ASCII (0x21 to 0x7e): ids
// and threads are chosen by the peer, are printed and logged, and carry no
// meaning the channel reads.
func checkToken(s string) error {
	if len(s) < 1 || len(s) > MaxIDLen {
		return fmt.Errorf("%d bytes, want 1..%d", len(s), MaxIDLen)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return fmt.Errorf("byte %d is not visible ASCII", i)
		}
	}
	return nil
}

// checkContentType accepts printable ASCII (0x20 to 0x7e), as a media type
// with parameters is written.
func checkContentType(s string) error {
	if len(s) < 1 || len(s) > MaxContentTypeLen {
		return fmt.Errorf("%d bytes, want 1..%d", len(s), MaxContentTypeLen)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return fmt.Errorf("byte %d is not printable ASCII", i)
		}
	}
	return nil
}

func (e Envelope) toWire() (envelopeWire, error) {
	kind, err := e.Kind.toWire()
	if err != nil {
		return envelopeWire{}, err
	}
	sent, err := record.TimeToWire(e.SentAt)
	if err != nil {
		return envelopeWire{}, fmt.Errorf("sent_at: %w", err)
	}
	accept, err := record.TimeToWire(e.AcceptBefore)
	if err != nil {
		return envelopeWire{}, fmt.Errorf("accept_before: %w", err)
	}
	report, err := record.TimeToWire(e.ReportBy)
	if err != nil {
		return envelopeWire{}, fmt.Errorf("report_by: %w", err)
	}
	w := envelopeWire{
		Type: Type, ID: string(e.ID), MultiRecipient: e.MultiRecipient, DiscloseRecipients: e.DiscloseRecipients,
		Kind: kind, ReplyTo: string(e.ReplyTo), Thread: e.Thread,
		SentAt: sent, AcceptBefore: accept, ReportBy: report,
		ContentType: e.ContentType, Body: e.Body,
	}
	w.To = make([]addressWire, len(e.To))
	for i, a := range e.To {
		w.To[i] = addressWire{Node: a.Node.Bytes(), Endpoint: a.Endpoint.Bytes()}
	}
	for _, r := range e.Evidence {
		b, err := r.Encode()
		if err != nil {
			return envelopeWire{}, fmt.Errorf("%w: %w", ErrEvidence, err)
		}
		w.Evidence = append(w.Evidence, b)
	}
	w.Seq = make([]seqWire, 0, len(e.Seq))
	for ep, n := range e.Seq {
		w.Seq = append(w.Seq, seqWire{Endpoint: ep.Bytes(), Seq: n})
	}
	slices.SortFunc(w.Seq, func(a, b seqWire) int { return bytes.Compare(a.Endpoint, b.Endpoint) })
	if e.Preset != "" {
		w.Params = &paramsWire{Preset: e.Preset}
	}
	return w, nil
}

// fromWire is the one place that refuses an invalid envelope: encoding runs
// the bytes it wrote back through it.
func fromWire(w envelopeWire, lim Limits) (Envelope, error) {
	if w.Type != Type {
		return Envelope{}, fmt.Errorf("%w: %q", ErrType, w.Type)
	}
	if err := checkToken(w.ID); err != nil {
		return Envelope{}, fmt.Errorf("%w: id: %v", ErrID, err)
	}
	kind, err := kindFromWire(w.Kind)
	if err != nil {
		return Envelope{}, err
	}
	e := Envelope{
		ID: delivery.MessageID(w.ID), MultiRecipient: w.MultiRecipient, DiscloseRecipients: w.DiscloseRecipients,
		Kind: kind, ReplyTo: delivery.MessageID(w.ReplyTo), Thread: w.Thread,
		ContentType: w.ContentType, Body: w.Body,
	}
	if w.ReplyTo != "" {
		if err := checkToken(w.ReplyTo); err != nil {
			return Envelope{}, fmt.Errorf("%w: reply_to: %v", ErrID, err)
		}
	}
	if w.Thread != "" {
		if err := checkToken(w.Thread); err != nil {
			return Envelope{}, fmt.Errorf("%w: thread: %v", ErrID, err)
		}
	}
	if err := fromWireRecipients(&e, w, lim); err != nil {
		return Envelope{}, err
	}
	if err := fromWireSeq(&e, w); err != nil {
		return Envelope{}, err
	}
	if e.SentAt, err = record.TimeFromWire(w.SentAt); err != nil {
		return Envelope{}, fmt.Errorf("sent_at: %w", err)
	}
	if e.AcceptBefore, err = record.TimeFromWire(w.AcceptBefore); err != nil {
		return Envelope{}, fmt.Errorf("accept_before: %w", err)
	}
	if e.ReportBy, err = record.TimeFromWire(w.ReportBy); err != nil {
		return Envelope{}, fmt.Errorf("report_by: %w", err)
	}
	if !e.ReportBy.Equal(delivery.ReportBy(e.AcceptBefore)) {
		return Envelope{}, fmt.Errorf("%w: accept_before %d, report_by %d", ErrReportBy, w.AcceptBefore, w.ReportBy)
	}
	if err := fromWireContent(&e, w, lim); err != nil {
		return Envelope{}, err
	}
	if err := fromWireParams(&e, w); err != nil {
		return Envelope{}, err
	}
	for i, b := range w.Evidence {
		r, err := record.Decode(b, record.DefaultMaxInput)
		if err != nil {
			return Envelope{}, fmt.Errorf("%w: evidence %d: %w", ErrEvidence, i, err)
		}
		e.Evidence = append(e.Evidence, r)
	}
	if err := CheckEvidence(e.Evidence); err != nil {
		return Envelope{}, err
	}
	switch {
	case len(e.Evidence) > 0 && e.ReplyTo == "":
		return Envelope{}, fmt.Errorf("%w: evidence without reply_to", ErrEvidence)
	case e.Kind.IsControl() && e.ReplyTo != "":
		return Envelope{}, fmt.Errorf("%w: a control signal answers no message", ErrReplyTo)
	}
	return e, nil
}

func fromWireRecipients(e *Envelope, w envelopeWire, lim Limits) error {
	if n := len(w.To); n < 1 || n > lim.MaxRecipients {
		return fmt.Errorf("%w: %d recipients, want 1..%d", ErrRecipients, n, lim.MaxRecipients)
	}
	eps := make(map[ident.EndpointID]struct{}, len(w.To))
	for i, a := range w.To {
		node, err := ident.NodeIDFromBytes(a.Node)
		if err != nil {
			return fmt.Errorf("%w: recipient %d node: %w", ErrRecipients, i, err)
		}
		ep, err := ident.EndpointIDFromBytes(a.Endpoint)
		if err != nil {
			return fmt.Errorf("%w: recipient %d endpoint: %w", ErrRecipients, i, err)
		}
		if ep.IsSystem() {
			return fmt.Errorf("%w: recipient %d", ErrSystemRecipient, i)
		}
		// Sequence numbers are keyed by endpoint alone, so no two recipients may
		// share an endpoint identifier, even on different nodes. That also keeps
		// every address in the list once.
		if _, dup := eps[ep]; dup {
			return fmt.Errorf("%w: recipient %d repeats an endpoint", ErrRecipients, i)
		}
		eps[ep] = struct{}{}
		e.To = append(e.To, ident.Address{Node: node, Endpoint: ep})
	}
	switch {
	case len(e.To) > 1 && !e.MultiRecipient:
		return fmt.Errorf("%w: several recipients without the multi-recipient flag", ErrRecipients)
	case e.Kind.IsControl() && (len(e.To) != 1 || e.MultiRecipient):
		return fmt.Errorf("%w: a control signal has exactly one recipient", ErrRecipients)
	case len(e.To) == 1 && e.MultiRecipient && e.DiscloseRecipients:
		// Disclosed, To is the whole list, so one entry is the whole message.
		return fmt.Errorf("%w: the multi-recipient flag with a single disclosed recipient", ErrRecipients)
	}
	return nil
}

func fromWireSeq(e *Envelope, w envelopeWire) error {
	if len(w.Seq) < 1 || len(w.Seq) > len(e.To) {
		return fmt.Errorf("%w: %d entries for %d recipients", ErrSeq, len(w.Seq), len(e.To))
	}
	known := make(map[ident.EndpointID]struct{}, len(e.To))
	for _, a := range e.To {
		known[a.Endpoint] = struct{}{}
	}
	e.Seq = make(map[ident.EndpointID]uint64, len(w.Seq))
	var prev []byte
	for i, s := range w.Seq {
		ep, err := ident.EndpointIDFromBytes(s.Endpoint)
		if err != nil {
			return fmt.Errorf("%w: entry %d: %w", ErrSeq, i, err)
		}
		if i > 0 && bytes.Compare(prev, s.Endpoint) >= 0 {
			return fmt.Errorf("%w: entry %d repeated or out of order", ErrSeq, i)
		}
		prev = s.Endpoint
		if _, ok := known[ep]; !ok {
			return fmt.Errorf("%w: entry %d is not a recipient", ErrSeq, i)
		}
		if err := record.RequireCounter(s.Seq); err != nil {
			return fmt.Errorf("%w: entry %d: %w", ErrSeq, i, err)
		}
		e.Seq[ep] = s.Seq
	}
	return nil
}

func fromWireContent(e *Envelope, w envelopeWire, lim Limits) error {
	hasBody := len(w.Body) > 0
	if hasBody != (w.ContentType != "") {
		return fmt.Errorf("%w: content type and body are given together or not at all", ErrBody)
	}
	if hasBody {
		if err := checkContentType(w.ContentType); err != nil {
			return fmt.Errorf("%w: content type: %v", ErrBody, err)
		}
		if len(w.Body) > lim.MaxBody {
			return fmt.Errorf("%w: %d bytes, limit %d", ErrBodyTooLarge, len(w.Body), lim.MaxBody)
		}
	}
	switch {
	case e.Kind == KindMessage && !hasBody:
		return fmt.Errorf("%w: a message has a body", ErrBody)
	case e.Kind.IsControl() && e.Kind != KindSessionCreate && hasBody:
		return fmt.Errorf("%w: %s has no body", ErrBody, e.Kind)
	}
	return nil
}

func fromWireParams(e *Envelope, w envelopeWire) error {
	if e.Kind != KindSessionCreate {
		if w.Params != nil {
			return fmt.Errorf("%w: %s has no params", ErrParams, e.Kind)
		}
		return nil
	}
	if w.Params == nil {
		return fmt.Errorf("%w: %s needs a preset", ErrParams, e.Kind)
	}
	if err := adapterproto.ValidatePresetName(w.Params.Preset); err != nil {
		return fmt.Errorf("%w: preset: %w", ErrParams, err)
	}
	e.Preset = w.Params.Preset
	return nil
}

// Encode returns the deterministic CBOR encoding of the envelope. It refuses
// what Decode would refuse, so that what this package writes is what every
// receiver accepts.
func (e Envelope) Encode(lim Limits) ([]byte, error) {
	w, err := e.toWire()
	if err != nil {
		return nil, err
	}
	b, err := record.MarshalCanonical(w)
	if err != nil {
		return nil, err
	}
	if _, err := Decode(b, lim); err != nil {
		return nil, err
	}
	return b, nil
}

// Decode parses an envelope strictly: the input is limited in size first, then
// decoded with the hardened options, checked against every rule of the format,
// and finally compared with its own re-encoding.
func Decode(data []byte, lim Limits) (Envelope, error) {
	if err := lim.check(); err != nil {
		return Envelope{}, err
	}
	var w envelopeWire
	if err := record.UnmarshalStrict(data, &w, lim.inputLimit()); err != nil {
		return Envelope{}, err
	}
	return fromWire(w, lim)
}

// CheckEvidence checks the shape of an evidence list: at most one control reply
// then at most one status report, in that order, and no other record type. That
// is also at most MaxEvidence records. It checks no signature; the verifier
// checks the evidence against the sender's own records.
func CheckEvidence(evidence []record.Record) error {
	rank := 0
	for i, r := range evidence {
		var at int
		switch r.Type {
		case ControlReplyType:
			at = 1
		case StatusReportType:
			at = 2
		default:
			return fmt.Errorf("%w: evidence %d is a %q", ErrEvidence, i, r.Type)
		}
		if at <= rank {
			return fmt.Errorf("%w: evidence %d repeats a type or is out of order", ErrEvidence, i)
		}
		rank = at
	}
	return nil
}

// RecipientsOn returns the endpoints of To that are on node, in order.
func (e Envelope) RecipientsOn(node ident.NodeID) []ident.EndpointID {
	var out []ident.EndpointID
	for _, a := range e.To {
		if a.Node == node {
			out = append(out, a.Endpoint)
		}
	}
	return out
}

// ValidateFor checks, for the node that receives the envelope, what Decode
// cannot know: that the envelope has a recipient on this node, that it lists
// only recipients on this node unless the sender discloses them, and that its
// sequence numbers are exactly those of this node's recipients.
func (e Envelope) ValidateFor(local ident.NodeID) error {
	if local.IsZero() {
		return fmt.Errorf("%w: no local node", ErrRecipients)
	}
	mine := e.RecipientsOn(local)
	switch {
	case len(mine) == 0:
		return fmt.Errorf("%w: no recipient on this node", ErrRecipients)
	case !e.DiscloseRecipients && len(mine) != len(e.To):
		return fmt.Errorf("%w: recipients on other nodes without disclosure", ErrRecipients)
	case len(e.Seq) != len(mine):
		return fmt.Errorf("%w: %d entries for %d recipients on this node", ErrSeq, len(e.Seq), len(mine))
	}
	for _, ep := range mine {
		if _, ok := e.Seq[ep]; !ok {
			return fmt.Errorf("%w: no entry for a recipient on this node", ErrSeq)
		}
	}
	return nil
}

// TrimmedFor returns the envelope as the sender sends it to node: the
// recipients on other nodes are left out unless the sender discloses them, and
// only this node's recipients keep a sequence number. The multi-recipient flag
// stays as the full envelope has it, so a target that refuses multi-recipient
// messages can still tell.
func (e Envelope) TrimmedFor(node ident.NodeID) Envelope {
	out := e
	mine := e.RecipientsOn(node)
	if !e.DiscloseRecipients {
		out.To = nil
		for _, a := range e.To {
			if a.Node == node {
				out.To = append(out.To, a)
			}
		}
	} else {
		out.To = slices.Clone(e.To)
	}
	out.Seq = make(map[ident.EndpointID]uint64, len(mine))
	for _, ep := range mine {
		if n, ok := e.Seq[ep]; ok {
			out.Seq[ep] = n
		}
	}
	out.Evidence = slices.Clone(e.Evidence)
	return out
}

// requestDomain is the digest domain of the request digest.
var requestDomain = frame.Domain("sidera/request-digest/1")

// Digest returns the digest the target stores for deduplication. It covers what
// a retry of the same request must repeat exactly: the kind, the params, the
// content type, the body, the two deadlines, and what the sender chose about
// the message's place in a conversation and who sees whom (reply_to, thread,
// the multi-recipient flag, disclose_recipients and, when recipients are
// disclosed, the whole recipient list in the order the sender gave it). A retry
// that changes any of them is another request under the same id, and the target
// must not take it for the one it already holds. It leaves out what may differ
// between attempts of one request: the recipient list when it is not disclosed,
// because each path carries the list trimmed for its node, the sequence
// numbers, the send time and the evidence. A request with the same id and
// another digest is a conflict (delivery.CheckDedup).
func (e Envelope) Digest() delivery.BodyDigest {
	kind, _ := e.Kind.toWire()
	var flags byte
	if e.MultiRecipient {
		flags |= 1
	}
	if e.DiscloseRecipients {
		flags |= 2
	}
	return delivery.BodyDigest(sha256.Sum256(frame.Frame(requestDomain,
		[]byte{byte(kind)}, []byte(e.Preset), []byte(e.ContentType), e.Body,
		[]byte(e.ReplyTo), []byte(e.Thread), []byte{flags}, e.disclosedRecipients(),
		unixBE(e.AcceptBefore), unixBE(e.ReportBy))))
}

// disclosedRecipients returns the recipient list as the request digest covers it:
// each address as node bytes then endpoint bytes, in the order of To, when the
// sender discloses the recipients, and nothing otherwise. Without disclosure To
// is the list trimmed for one path and differs between attempts of one request.
func (e Envelope) disclosedRecipients() []byte {
	if !e.DiscloseRecipients {
		return nil
	}
	var b []byte
	for _, a := range e.To {
		b = append(b, a.Node.Bytes()...)
		b = append(b, a.Endpoint.Bytes()...)
	}
	return b
}

func unixBE(t time.Time) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(t.Unix()))
}
