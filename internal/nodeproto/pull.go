package nodeproto

import (
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// PullRequest asks for the current reports of the asker's deliveries of one
// message to the listed recipients on the target. The asking node is the one the
// connection authenticated. A request names at least one recipient, and the
// target answers for each of them and for no other.
type PullRequest struct {
	Message    delivery.MessageID
	Recipients []ident.EndpointID
}

type pullRequestWire struct {
	Message    string   `cbor:"1,keyasint"`
	Recipients [][]byte `cbor:"2,keyasint"`
}

// Encode returns the canonical bytes of the request. It refuses what
// DecodePullRequest would.
func (r PullRequest) Encode() ([]byte, error) {
	return encodeChecked(pullRequestWire{Message: string(r.Message), Recipients: endpointsToWire(r.Recipients)}, DecodePullRequest)
}

// DecodePullRequest parses a pull request strictly, within the request limit of
// the protocol. The message identifier follows envelope.ValidMessageID, and the
// recipients are 1 to MaxRecipients distinct endpoints.
func DecodePullRequest(data []byte) (PullRequest, error) {
	var w pullRequestWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolPull).Request); err != nil {
		return PullRequest{}, err
	}
	id := delivery.MessageID(w.Message)
	if err := envelope.ValidMessageID(id); err != nil {
		return PullRequest{}, err
	}
	recipients, err := endpointsFromWire(w.Recipients, 1, MaxRecipients, "recipient")
	if err != nil {
		return PullRequest{}, err
	}
	return PullRequest{Message: id, Recipients: recipients}, nil
}

// PullAnswer is the target's answer to a pull. With the outcome ok, each
// recipient of the request is in at most one of Reports (its signed report, in
// a record the answer does not attribute to a recipient) and NoRecord (the
// target holds no record of that delivery); a recipient in neither has nothing
// to report yet. A refused or failed answer carries what the send answer's
// does.
type PullAnswer struct {
	Outcome      Outcome
	Reports      []record.Record
	NoRecord     []ident.EndpointID
	ControlReply *record.Record
	Reason       string
}

type pullAnswerWire struct {
	Outcome      uint64   `cbor:"1,keyasint"`
	Reports      [][]byte `cbor:"2,keyasint"`
	NoRecord     [][]byte `cbor:"3,keyasint"`
	ControlReply []byte   `cbor:"4,keyasint,omitempty"`
	Reason       string   `cbor:"5,keyasint,omitempty"`
}

// Encode returns the canonical bytes of the answer. It refuses what
// DecodePullAnswer would.
func (a PullAnswer) Encode() ([]byte, error) {
	reports, err := encodeRecords(a.Reports)
	if err != nil {
		return nil, err
	}
	reply, err := encodeOptionalRecord(a.ControlReply)
	if err != nil {
		return nil, err
	}
	return encodeChecked(pullAnswerWire{
		Outcome: uint64(a.Outcome), Reports: reports, NoRecord: endpointsToWire(a.NoRecord),
		ControlReply: reply, Reason: a.Reason,
	}, DecodePullAnswer)
}

// DecodePullAnswer parses a pull answer strictly, within the answer limit of
// the protocol. Reports and no-record entries together are at most
// MaxRecipients, the most a request names, and the no-record endpoints are
// distinct. The records' shape is read, nothing is verified.
func DecodePullAnswer(data []byte) (PullAnswer, error) {
	var w pullAnswerWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolPull).Answer); err != nil {
		return PullAnswer{}, err
	}
	o, err := checkOutcome(w.Outcome, w.Reason)
	if err != nil {
		return PullAnswer{}, err
	}
	if o != OutcomeOK && (len(w.Reports) > 0 || len(w.NoRecord) > 0 || len(w.ControlReply) > 0) {
		return PullAnswer{}, invalid("a %s answer carries no records", o)
	}
	if n := len(w.Reports) + len(w.NoRecord); n > MaxRecipients {
		return PullAnswer{}, invalid("%d reports and no-record entries, at most %d", n, MaxRecipients)
	}
	reports, err := decodeRecords(w.Reports, MaxRecipients, "report")
	if err != nil {
		return PullAnswer{}, err
	}
	noRecord, err := endpointsFromWire(w.NoRecord, 0, MaxRecipients, "no-record endpoint")
	if err != nil {
		return PullAnswer{}, err
	}
	reply, err := decodeOptionalRecord(w.ControlReply, "control reply")
	if err != nil {
		return PullAnswer{}, err
	}
	return PullAnswer{Outcome: o, Reports: reports, NoRecord: noRecord, ControlReply: reply, Reason: w.Reason}, nil
}
