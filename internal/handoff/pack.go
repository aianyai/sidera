package handoff

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
)

// ExitOK is the exit code of a recv that hands out at least one message. A
// non-zero code would make Claude show only about 10,000 characters of the
// output, so a recv that delivers must never use one.
const ExitOK = 0

// Message is a queued message as recv hands it out. The caller guarantees that
// From is the sender the channel authenticated and that Body is valid UTF-8.
type Message struct {
	ID   string
	From ident.Address
	Body string
	// PossiblyDuplicate marks the part this page hands out as one that was
	// handed out before and not acknowledged: the delivery side sets it from
	// delivery.Record.PossibleDuplicate(part), for the part Pack hands out
	// (NextPart, or 1) or the part k that Part is asked for.
	PossiblyDuplicate bool
	// NextPart is the part the next plain recv hands out when the message is
	// larger than one page: the delivery side sets it from
	// delivery.Record.NextPart(), the first part not yet acknowledged. 0 and 1
	// both mean from the start, so a message that was never handed out, or fits
	// whole, needs no value.
	NextPart int
}

// Handout is a message, or one part of a message, that the model has been
// given in full. Only what is listed here may be leased and acknowledged.
// A message handed out whole has Part and Parts both 1.
type Handout struct {
	ID    string
	Part  int
	Parts int
}

// Page is the result of one recv.
type Page struct {
	// Output is what recv prints: one JSON document and a newline.
	Output []byte
	// HandedOut lists what Output contains, in order.
	HandedOut []Handout
	// Remaining is the number of queued messages not in Output. A message
	// whose later parts are still to be fetched is in Output, not counted here.
	Remaining int
	// ExitCode is the exit status of recv.
	ExitCode int
}

// MaxLeaseLen is the length in bytes of the lease ID a page carries: the text
// form of a delivery.LeaseID. The cut points of a message reserve room for it,
// so they do not depend on which lease the page carries.
const MaxLeaseLen = delivery.LeaseIDTextLen

var (
	// ErrLeaseUnset: the lease ID is the zero value, which names no lease.
	ErrLeaseUnset = errors.New("handoff: lease ID is unset")
	// ErrBudgetTooSmall: not even one rune of a body fits next to the fields
	// that surround it.
	ErrBudgetTooSmall = errors.New("handoff: budget too small for one message part")
	// ErrNoSuchPart: the message has no part with the requested number.
	ErrNoSuchPart = errors.New("handoff: no such message part")
)

// Pack builds the output of recv for the messages queued for an endpoint, in
// queue order, within the budget. lease is the identifier the caller generated
// for this recv, the one it passes to delivery.Record.Handout for every entry of
// Page.HandedOut; the page carries it as MaxLeaseLen lowercase hex characters,
// which JSON does not escape. The zero LeaseID is refused with ErrLeaseUnset,
// also for an empty queue: the caller generates the identifier before it packs,
// so a page always has one and its wire shape does not vary.
//
// It takes whole messages while they fit. The first message that does not fit
// ends the page and stays queued for the next recv; the order of the queue is
// never changed to squeeze in a smaller message, because that would let small
// messages starve a large one. When the first message alone is larger than the
// budget, the page holds only one part of it, labelled "part k/n": the part
// numbered Message.NextPart, or part 1 when that is 0 or 1. Later parts are
// fetched with Part. Every part fits the budget.
//
// The budget is checked against the encoded output, not against the bodies.
func Pack(queue []Message, lease delivery.LeaseID, b Budget) (Page, error) {
	if err := b.validate(); err != nil {
		return Page{}, err
	}
	if err := validateLease(lease); err != nil {
		return Page{}, err
	}
	var shown []wireMessage
	var handed []Handout
	var out []byte
	for i, m := range queue {
		parts, err := splitBody(m, b)
		if err != nil {
			return Page{}, err
		}
		k, err := startPart(m, len(parts))
		if err != nil {
			return Page{}, err
		}
		if len(parts) > 1 {
			if len(shown) > 0 {
				break
			}
			return buildPage(m, parts, k, len(queue)-1, lease, b)
		}
		encoded := encodePage(lease.String(), append(shown, wireOf(m, m.Body, 0, 0)), len(queue)-i-1)
		if b.Measure(string(encoded)) > b.Limit {
			break
		}
		shown = append(shown, wireOf(m, m.Body, 0, 0))
		out = encoded
		handed = append(handed, Handout{ID: m.ID, Part: 1, Parts: 1})
	}
	if len(queue) == 0 {
		out = encodePage(lease.String(), nil, 0)
	} else if len(shown) == 0 {
		// A message that fits whole always fits an empty page; this is
		// reached only by a queue longer than maxMore.
		return Page{}, ErrBudgetTooSmall
	}
	return Page{Output: out, HandedOut: handed, Remaining: len(queue) - len(shown), ExitCode: ExitOK}, nil
}

// Part builds the output of recv --msg <id> --part k: part k, counted from 1,
// of a message. A message that fits whole has the single part 1. The parts of
// a message are fixed by its ID, sender and body and by the budget, so the
// budget must be the one Pack used when it handed out the first part.
func Part(m Message, k int, lease delivery.LeaseID, b Budget) (Page, error) {
	if err := b.validate(); err != nil {
		return Page{}, err
	}
	if err := validateLease(lease); err != nil {
		return Page{}, err
	}
	parts, err := splitBody(m, b)
	if err != nil {
		return Page{}, err
	}
	if k < 1 || k > len(parts) {
		return Page{}, fmt.Errorf("%w: %d of %d", ErrNoSuchPart, k, len(parts))
	}
	return buildPage(m, parts, k, 0, lease, b)
}

func validateLease(lease delivery.LeaseID) error {
	if lease.IsZero() {
		return ErrLeaseUnset
	}
	return nil
}

// startPart returns the part of m that a plain recv hands out, of n parts.
func startPart(m Message, n int) (int, error) {
	k := max(m.NextPart, 1)
	if m.NextPart < 0 || k > n {
		return 0, fmt.Errorf("%w: next part %d of %d for message %s", ErrNoSuchPart, m.NextPart, n, m.ID)
	}
	return k, nil
}

// buildPage encodes a page holding part k of a message and checks that it fits.
func buildPage(m Message, parts []string, k, more int, lease delivery.LeaseID, b Budget) (Page, error) {
	n := len(parts)
	label := k
	if n == 1 {
		label, n = 0, 0 // a message that fits whole carries no part label
	}
	out := encodePage(lease.String(), []wireMessage{wireOf(m, parts[k-1], label, n)}, more)
	if b.Measure(string(out)) > b.Limit {
		// splitBody reserves room for every number it can foresee; this is
		// reached only by a queue longer than maxMore.
		return Page{}, ErrBudgetTooSmall
	}
	return Page{
		Output:    out,
		HandedOut: []Handout{{ID: m.ID, Part: k, Parts: len(parts)}},
		Remaining: more,
		ExitCode:  ExitOK,
	}, nil
}

// maxMore is the largest "more" count that splitBody reserves room for. It
// keeps the boundaries between parts independent of the queue length, which a
// later recv --part would not know.
const maxMore = 999_999

// splitBody cuts the body of m into the parts of the message. A message whose
// whole page fits is a single part.
//
// The cut points depend only on the message's ID, sender and body and on the
// budget. Everything else that varies from one recv to the next is sized at its
// widest instead: the possibly-duplicate flag (a message gains it when a
// lease on it expires, between two recv calls for its parts), the lease ID,
// the count of other queued messages and the part numbers. A later recv for
// another part, which knows none of these as they were, then cuts where part 1
// did.
func splitBody(m Message, b Budget) ([]string, error) {
	sized := m
	sized.PossiblyDuplicate = true
	reserved := strings.Repeat("x", MaxLeaseLen)
	fits := func(body string, part, parts int) bool {
		out := encodePage(reserved, []wireMessage{wireOf(sized, body, part, parts)}, maxMore)
		return b.Measure(string(out)) <= b.Limit
	}
	if fits(m.Body, 0, 0) {
		return []string{m.Body}, nil
	}

	// The part label grows with the number of parts, which is known only after
	// cutting. Cut with the widest label for d digits (a part before the last,
	// which also carries the hint for the next one), and widen d until the
	// result has no more than d digits of parts.
	starts := runeStarts(m.Body)
	for digits := 1; ; {
		widest := pow10(digits) - 1
		var parts []string
		for at := 0; at < len(starts)-1; {
			// The largest end such that body[starts[at]:starts[end]] fits.
			end := at + sort.Search(len(starts)-1-at, func(j int) bool {
				return !fits(m.Body[starts[at]:starts[at+j+1]], widest-1, widest)
			})
			if end == at {
				return nil, ErrBudgetTooSmall
			}
			parts = append(parts, m.Body[starts[at]:starts[end]])
			at = end
		}
		if len(parts) == 0 {
			return nil, ErrBudgetTooSmall // an empty body that does not fit
		}
		if need := len(strconv.Itoa(len(parts))); need > digits {
			digits = need
			continue
		}
		return parts, nil
	}
}

func pow10(n int) int {
	p := 1
	for ; n > 0; n-- {
		p *= 10
	}
	return p
}

// wirePage is the JSON document recv prints.
type wirePage struct {
	// Lease is the page identifier the caller generated for this recv.
	Lease    string        `json:"lease"`
	Messages []wireMessage `json:"messages"`
	// More is the number of queued messages not in Messages.
	More int `json:"more"`
}

// wireMessage separates what the channel vouches for (id, from) from what the
// sender wrote (body), so a body cannot pass itself off as a header.
type wireMessage struct {
	ID                string    `json:"id"`
	From              string    `json:"from"`
	PossiblyDuplicate bool      `json:"possibly_duplicate,omitempty"`
	Part              *wirePart `json:"part,omitempty"`
	Body              string    `json:"body"`
}

// wirePart labels a message that is handed out in parts.
type wirePart struct {
	Index int    `json:"index"`
	Total int    `json:"total"`
	Label string `json:"label"`
	// Next points at the following part; absent on the last part.
	Next *wireNext `json:"next,omitempty"`
}

// wireNext names the call that fetches a part. It is front-end neutral: the
// CLI and the MCP tool each map it to their own call.
type wireNext struct {
	Msg  string `json:"msg"`
	Part int    `json:"part"`
}

// wireOf builds the wire form of m with the given body. A part number of 0
// means the message is not split.
func wireOf(m Message, body string, part, parts int) wireMessage {
	w := wireMessage{ID: m.ID, From: m.From.String(), PossiblyDuplicate: m.PossiblyDuplicate, Body: body}
	if part > 0 {
		w.Part = &wirePart{Index: part, Total: parts, Label: fmt.Sprintf("part %d/%d", part, parts)}
		if part < parts {
			w.Part.Next = &wireNext{Msg: m.ID, Part: part + 1}
		}
	}
	return w
}

// encodePage encodes a page without HTML escaping and with non-ASCII text as
// is, so that Chinese text and characters such as < and & are not inflated into
// \uXXXX sequences that eat the budget. Go still escapes U+2028 and U+2029,
// which JavaScript hosts treat as line terminators; they are rare enough not to
// matter for the budget, which is checked against the encoded output anyway.
func encodePage(lease string, msgs []wireMessage, more int) []byte {
	if msgs == nil {
		msgs = []wireMessage{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(wirePage{Lease: lease, Messages: msgs, More: more}); err != nil {
		panic(err) // the page holds only strings, ints and bools
	}
	return buf.Bytes()
}
