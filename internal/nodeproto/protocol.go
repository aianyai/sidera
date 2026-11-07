package nodeproto

import (
	"errors"
	"fmt"
	"slices"

	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/internal/record"
)

// The protocol identifiers of the member protocols. Each is one request and one
// answer on one stream, each a frame of its own (AppendFrame, ReadFrame): the
// client does not half-close after its request, so the server cannot take the
// end of the stream for the end of the request, and it keeps the stream until
// the client has taken the answer and closed. They are defined here and nowhere
// else; the pre-authentication access table lists no protocol of this package,
// so each falls under "every other protocol", which only members may use.
const (
	// ProtocolSend carries one envelope to a recipient's node.
	ProtocolSend = "/sidera/send/2.0.0"
	// ProtocolPull asks for the current reports of the deliveries of one message.
	ProtocolPull = "/sidera/pull/2.0.0"
	// ProtocolDirectory asks for a page of the endpoints a node shows the asker,
	// each with the adapter that serves it.
	ProtocolDirectory = "/sidera/directory/3.0.0"
	// PreviousProtocolDirectory is the directory protocol before entries named
	// their adapter. It is framed as the current one is; its entries are the same
	// without the adapter. A node serves it so that a node that has not been
	// upgraded can still ask, and asks it of a node that serves no newer one.
	PreviousProtocolDirectory = "/sidera/directory/2.0.0"
	// ProtocolDialBack asks a member whether it can dial the asker back on the
	// addresses it holds for it. It was framed from its first version and has no
	// legacy identifier.
	ProtocolDialBack = "/sidera/dial-back/1.0.0"
)

// The identifiers a node before 2.0.0 speaks, kept so that nodes upgraded one
// at a time still understand each other. On such a stream the request is
// everything up to the client's half-close (ReadMessage), the answer
// everything up to the end of the stream, and the server releases the stream
// as soon as it has answered. Messages, limits and access are those of the
// current identifier of the protocol. A node serves both and a client offers
// the current identifier first (Offer); the legacy ones are dropped once no
// node that speaks only them remains in use.
const (
	LegacyProtocolSend      = "/sidera/send/1.0.0"
	LegacyProtocolPull      = "/sidera/pull/1.0.0"
	LegacyProtocolDirectory = "/sidera/directory/1.0.0"
)

// offered maps each current identifier to the identifiers a client proposes for
// the protocol, newest first. The last of a list is the legacy one of the
// protocol, if it has one.
var offered = map[string][]string{
	ProtocolSend:      {ProtocolSend, LegacyProtocolSend},
	ProtocolPull:      {ProtocolPull, LegacyProtocolPull},
	ProtocolDirectory: {ProtocolDirectory, PreviousProtocolDirectory, LegacyProtocolDirectory},
	ProtocolDialBack:  {ProtocolDialBack},
}

// Protocols lists every identifier a node serves, the current ones first, in a
// stable order.
func Protocols() []string {
	return []string{
		ProtocolSend, ProtocolPull, ProtocolDirectory, ProtocolDialBack,
		PreviousProtocolDirectory,
		LegacyProtocolSend, LegacyProtocolPull, LegacyProtocolDirectory,
	}
}

// Offer returns the identifiers a client proposes for a call to a protocol, in
// the order of preference: the current identifier, then the older ones the
// protocol has. It is nil for an identifier that is not a current one.
func Offer(protocol string) []string {
	return slices.Clone(offered[protocol])
}

// IsLegacy reports whether the identifier is one of the legacy ones, whose
// streams are not framed.
func IsLegacy(protocol string) bool {
	return protocol == LegacyProtocolSend || protocol == LegacyProtocolPull || protocol == LegacyProtocolDirectory
}

// Limit is the byte limit of the two messages of one protocol: the longest
// request a server reads and the longest answer a client reads. A reader stops
// at the limit and refuses more, before it decodes anything.
type Limit struct {
	Request int
	Answer  int
}

const (
	// requestSlack is what a request needs beyond the envelope it carries: the
	// sender endpoint, the map framing and a margin for a longer sender text.
	requestSlack = 512
	// answerSlack is what an answer needs beyond its records: the outcome, the
	// reason, the framing and the lengths of the lists.
	answerSlack = 512
	// MaxReasonLen is the longest refusal reason name, in bytes.
	MaxReasonLen = 64
	// MaxCursorLen is the longest directory continuation, in bytes. The engine's
	// own cursors are about 130 bytes; the limit leaves room to grow.
	MaxCursorLen = 256
	// MaxDirectoryPage is the most entries one directory answer lists: a node
	// has at most this many endpoints.
	MaxDirectoryPage = publication.MaxEndpointsPerNode
	// MaxRecipients is the most recipients one request names or one answer
	// reports on: the bound of the envelope.
	MaxRecipients = envelope.MaxRecipients

	// maxDirectoryEntry bounds the encoding of one directory entry. Without its
	// adapter it is an endpoint identifier, a kind and up to eight action names, a
	// display name of 128 characters and a description of 1024, four bytes each at
	// the most: 5 KiB. The adapter adds at most 659 bytes: the key, the map head, an
	// instance and an adapter id of 32 bytes each, a version of 64 bytes and a
	// runtime whose name and version are 64 characters of four bytes each (1 + 1 +
	// 2*(1+2+32) + (1+2+64) + (1+1+2*(1+2+256))), so 6 KiB holds an entry.
	maxDirectoryEntry = 6 << 10
	// maxDirectoryRequest holds a sender endpoint, a cursor and a page size.
	maxDirectoryRequest = 512
	// maxPullRequest holds a message identifier and a list of endpoint
	// identifiers.
	maxPullRequest = 2048
	// maxDialBackRequest holds the empty map the request is.
	maxDialBackRequest = 16
	// maxDialBackAnswer holds an outcome, a result, a multiaddr of at most
	// MaxAddressLen bytes and a reason.
	maxDialBackAnswer = 512
)

// LimitOf returns the byte limits of a protocol, the same for its current and
// its older identifiers, and false for an identifier that is not one of this
// package's. On a framed stream the limit bounds the payload of the frame.
//
// A send request holds one envelope. A send or pull answer holds a report per
// recipient and a control reply, each at the most the record limit. A directory
// answer holds a page of entries.
func LimitOf(protocol string) (Limit, bool) {
	records := (MaxRecipients + 1) * record.DefaultMaxInput
	switch protocol {
	case ProtocolSend, LegacyProtocolSend:
		return Limit{Request: envelope.MaxEncoded() + requestSlack, Answer: records + answerSlack}, true
	case ProtocolPull, LegacyProtocolPull:
		return Limit{Request: maxPullRequest, Answer: records + answerSlack}, true
	case ProtocolDirectory, PreviousProtocolDirectory, LegacyProtocolDirectory:
		return Limit{Request: maxDirectoryRequest, Answer: MaxDirectoryPage*maxDirectoryEntry + answerSlack + MaxCursorLen}, true
	case ProtocolDialBack:
		return Limit{Request: maxDialBackRequest, Answer: maxDialBackAnswer}, true
	}
	return Limit{}, false
}

// limit returns the limit of a protocol this package defines.
func limit(protocol string) Limit {
	l, ok := LimitOf(protocol)
	if !ok {
		panic("nodeproto: unknown protocol " + protocol)
	}
	return l
}

// Outcome says how the target handled a request.
type Outcome uint8

// The outcomes. The zero value is not an outcome.
const (
	// OutcomeOK: the request was handled and the answer carries the result.
	OutcomeOK Outcome = iota + 1
	// OutcomeRefused: the target refused the request for a reason it names.
	OutcomeRefused
	// OutcomeFailed: the target could not handle the request and says nothing
	// more; no receipt exists.
	OutcomeFailed
)

func (o Outcome) valid() bool { return o >= OutcomeOK && o <= OutcomeFailed }

// String returns the name of the outcome.
func (o Outcome) String() string {
	switch o {
	case OutcomeOK:
		return "ok"
	case OutcomeRefused:
		return "refused"
	case OutcomeFailed:
		return "failed"
	}
	return fmt.Sprintf("invalid(%d)", uint8(o))
}

// ErrInvalid is wrapped by every refusal of a message whose bytes decode but
// break a rule of the protocol: an unknown outcome, a list over its bound, a
// field that does not belong to the outcome. Bytes that do not decode, are
// not canonical or exceed the limit wrap the errors of package record.
var ErrInvalid = errors.New("invalid node protocol message")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// checkOutcome checks what every answer shares: the outcome is one of three,
// and the reason is present exactly when the target refused, in a form
// checkReason accepts. A result goes only with the outcome ok; each answer
// checks that for its own fields.
func checkOutcome(raw uint64, reason string) (Outcome, error) {
	if raw < uint64(OutcomeOK) || raw > uint64(OutcomeFailed) {
		return 0, invalid("outcome %d", raw)
	}
	o := Outcome(raw)
	if (o == OutcomeRefused) != (reason != "") {
		return 0, invalid("a reason goes with a refused outcome and with no other, outcome %s, reason %q", o, reason)
	}
	if err := checkReason(reason); err != nil {
		return 0, err
	}
	return o, nil
}

// checkReason accepts the names the engine gives its reasons: lower-case
// letters, digits and underscores, so that a hostile target cannot put text
// of its own choosing into a log line.
func checkReason(reason string) error {
	if len(reason) > MaxReasonLen {
		return invalid("reason is %d bytes, at most %d", len(reason), MaxReasonLen)
	}
	for i := 0; i < len(reason); i++ {
		c := reason[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return invalid("reason has the byte %#x at %d", c, i)
		}
	}
	return nil
}
