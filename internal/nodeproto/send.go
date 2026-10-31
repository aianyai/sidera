package nodeproto

import (
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// SendRequest carries one envelope from an endpoint of the sending node. The
// envelope is the bytes the sender encoded: the target decodes and judges them
// with package envelope. The sending node is the one the connection
// authenticated; only the endpoint is in the message.
type SendRequest struct {
	Sender   ident.EndpointID
	Envelope []byte
}

type sendRequestWire struct {
	Sender   string `cbor:"1,keyasint"`
	Envelope []byte `cbor:"2,keyasint"`
}

// Encode returns the canonical bytes of the request. It refuses what
// DecodeSendRequest would.
func (r SendRequest) Encode() ([]byte, error) {
	return encodeChecked(sendRequestWire{Sender: r.Sender.String(), Envelope: r.Envelope}, DecodeSendRequest)
}

// DecodeSendRequest parses a send request strictly, within the request limit of
// the protocol. The envelope must be present and at most envelope.MaxEncoded
// bytes; its content is not read.
func DecodeSendRequest(data []byte) (SendRequest, error) {
	var w sendRequestWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolSend).Request); err != nil {
		return SendRequest{}, err
	}
	sender, err := parseEndpointText(w.Sender, "sender")
	if err != nil {
		return SendRequest{}, err
	}
	if n := len(w.Envelope); n < 1 || n > envelope.MaxEncoded() {
		return SendRequest{}, invalid("envelope is %d bytes, want 1..%d", n, envelope.MaxEncoded())
	}
	return SendRequest{Sender: sender, Envelope: w.Envelope}, nil
}

// SendAnswer is the target's answer to a send. With the outcome ok it carries
// the signed status reports for the recipients on the target and, for a control
// signal that already has one, the control reply: the receipt. A refused
// outcome carries the name of the refusal reason and nothing else; a failed one
// carries nothing: no receipt exists.
type SendAnswer struct {
	Outcome      Outcome
	Reports      []record.Record
	ControlReply *record.Record
	Reason       string
}

type sendAnswerWire struct {
	Outcome      uint64   `cbor:"1,keyasint"`
	Reports      [][]byte `cbor:"2,keyasint"`
	ControlReply []byte   `cbor:"3,keyasint,omitempty"`
	Reason       string   `cbor:"4,keyasint,omitempty"`
}

// Encode returns the canonical bytes of the answer. It refuses what
// DecodeSendAnswer would.
func (a SendAnswer) Encode() ([]byte, error) {
	reports, err := encodeRecords(a.Reports)
	if err != nil {
		return nil, err
	}
	reply, err := encodeOptionalRecord(a.ControlReply)
	if err != nil {
		return nil, err
	}
	return encodeChecked(sendAnswerWire{Outcome: uint64(a.Outcome), Reports: reports, ControlReply: reply, Reason: a.Reason}, DecodeSendAnswer)
}

// DecodeSendAnswer parses a send answer strictly, within the answer limit of
// the protocol. It reads the records' shape only; whether a report is for the
// envelope that was sent, and its signature, are the sender's to check.
func DecodeSendAnswer(data []byte) (SendAnswer, error) {
	var w sendAnswerWire
	if err := record.UnmarshalStrict(data, &w, limit(ProtocolSend).Answer); err != nil {
		return SendAnswer{}, err
	}
	o, err := checkOutcome(w.Outcome, w.Reason)
	if err != nil {
		return SendAnswer{}, err
	}
	if o != OutcomeOK && (len(w.Reports) > 0 || len(w.ControlReply) > 0) {
		return SendAnswer{}, invalid("a %s answer carries no records", o)
	}
	reports, err := decodeRecords(w.Reports, MaxRecipients, "report")
	if err != nil {
		return SendAnswer{}, err
	}
	reply, err := decodeOptionalRecord(w.ControlReply, "control reply")
	if err != nil {
		return SendAnswer{}, err
	}
	return SendAnswer{Outcome: o, Reports: reports, ControlReply: reply, Reason: w.Reason}, nil
}
