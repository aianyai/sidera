package handoff

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ExitOK is the exit code of a recv that hands out at least one message. A
// non-zero code can make a runtime show only a small part of the output (about
// 10,000 characters on one of the measured paths), so a recv that delivers must
// never use one.
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

// FeedbackMember says whether a page writes the member `feedback` of a message
// whose sender is the system endpoint. A reader that decodes a page strictly
// refuses a page with a member it does not know, so the caller passes
// WriteFeedbackMember only for a reader that is known to decode it. The choice
// changes the bytes of the page and nothing else: the cut points of a message are
// the same either way.
type FeedbackMember bool

const (
	// OmitFeedbackMember leaves the member out of every message.
	OmitFeedbackMember FeedbackMember = false
	// WriteFeedbackMember writes `feedback: true` on a message from the system
	// endpoint, whole or in every part.
	WriteFeedbackMember FeedbackMember = true
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
//
// cutFor are further budgets that a message is cut for: a message is one part
// only when its page fits b and every one of them, and every part fits all of
// them. The page itself is bounded by b alone. A caller whose endpoint is read
// through several front ends passes the budgets of the others, so that a message
// is cut in the same parts whichever front end reads it: the parts are counted
// and acknowledged one by one, and a part of one cut is not a part of another.
func Pack(queue []Message, lease delivery.LeaseID, b Budget, feedback FeedbackMember, cutFor ...Budget) (Page, error) {
	budgets, err := cutBudgets(b, cutFor)
	if err != nil {
		return Page{}, err
	}
	if err := validateLease(lease); err != nil {
		return Page{}, err
	}
	var shown []wireMessage
	var handed []Handout
	var out []byte
	for i, m := range queue {
		parts, err := splitBody(m, budgets)
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
			return buildPage(m, parts, k, len(queue)-1, lease, b, feedback)
		}
		encoded := encodePage(lease.String(), append(shown, wireOf(m, m.Body, 0, 0, feedback)), len(queue)-i-1)
		if b.Measure(string(encoded)) > b.Limit {
			break
		}
		shown = append(shown, wireOf(m, m.Body, 0, 0, feedback))
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
// budget and cutFor must be the ones Pack used when it handed out the first part.
func Part(m Message, k int, lease delivery.LeaseID, b Budget, feedback FeedbackMember, cutFor ...Budget) (Page, error) {
	budgets, err := cutBudgets(b, cutFor)
	if err != nil {
		return Page{}, err
	}
	if err := validateLease(lease); err != nil {
		return Page{}, err
	}
	parts, err := splitBody(m, budgets)
	if err != nil {
		return Page{}, err
	}
	if k < 1 || k > len(parts) {
		return Page{}, fmt.Errorf("%w: %d of %d", ErrNoSuchPart, k, len(parts))
	}
	return buildPage(m, parts, k, 0, lease, b, feedback)
}

// cutBudgets are the budgets a message is cut for: the one of the page, then the
// others the caller names. Each must be valid.
func cutBudgets(b Budget, cutFor []Budget) ([]Budget, error) {
	budgets := append([]Budget{b}, cutFor...)
	for _, c := range budgets {
		if err := c.validate(); err != nil {
			return nil, err
		}
	}
	return budgets, nil
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
func buildPage(m Message, parts []string, k, more int, lease delivery.LeaseID, b Budget, feedback FeedbackMember) (Page, error) {
	n := len(parts)
	label := k
	if n == 1 {
		label, n = 0, 0 // a message that fits whole carries no part label
	}
	out := encodePage(lease.String(), []wireMessage{wireOf(m, parts[k-1], label, n, feedback)}, more)
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

// splitBody cuts the body of m into the parts of the message, so that each part's
// page fits every one of the budgets. A message whose whole page fits them all is
// a single part.
//
// The cut points depend only on the message's ID, sender and body and on the
// budget. The feedback member follows from the sender, so it is one of those: the
// pages measured here always write it, whether or not the page that is handed out
// does, and a reader that reads the parts of a message through pages with and
// without it sees the same parts. Everything else that varies from
// one recv to the next is sized at its widest instead: the possibly-duplicate
// flag (a message gains it when a lease on it expires, between two recv calls
// for its parts), the lease ID, the count of other queued messages and the part
// numbers. A later recv for another part, which knows none of these as they
// were, then cuts where part 1 did.
func splitBody(m Message, budgets []Budget) ([]string, error) {
	sized := m
	sized.PossiblyDuplicate = true
	reserved := strings.Repeat("x", MaxLeaseLen)
	fits := func(body string, part, parts int) bool {
		out := string(encodePage(reserved, []wireMessage{wireOf(sized, body, part, parts, WriteFeedbackMember)}, maxMore))
		for _, b := range budgets {
			if b.Measure(out) > b.Limit {
				return false
			}
		}
		return true
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
	ID                string `json:"id"`
	From              string `json:"from"`
	PossiblyDuplicate bool   `json:"possibly_duplicate,omitempty"`
	// Feedback marks a message that is the node's own channel feedback: its
	// sender is the system endpoint, which no request can send as. It follows
	// from From alone, and is written only when the page is asked to.
	Feedback bool      `json:"feedback,omitempty"`
	Part     *wirePart `json:"part,omitempty"`
	Body     string    `json:"body"`
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
func wireOf(m Message, body string, part, parts int, feedback FeedbackMember) wireMessage {
	w := wireMessage{
		ID: m.ID, From: m.From.String(), PossiblyDuplicate: m.PossiblyDuplicate,
		Feedback: bool(feedback) && m.From.Endpoint.IsSystem(), Body: body,
	}
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
// \uXXXX sequences that eat the budget. The page is read by a model and may be
// printed in a person's terminal, and the encoder writes DEL, the C1 controls
// and the bidirectional formatting controls as themselves, which a terminal acts
// on or which reorder the text around them; they are written as \uXXXX like the
// C0 controls the encoder already escapes, which changes the bytes and not the
// strings a JSON reader decodes. They are rare, and the budget is checked against
// the encoded output, so the longer escape costs size and not correctness.
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
	return escapePersonText(buf.Bytes())
}

// escapePersonText rewrites every character that adapterproto.RefusedInPersonText
// names, and that the JSON encoder left as itself, as a \uXXXX escape. The encoded
// page is valid UTF-8 in which only string contents are non-ASCII, so a rune from
// U+007F on is always inside a string; the C0 controls, which include the newline
// that ends the page, are the encoder's own.
func escapePersonText(page []byte) []byte {
	var out []byte // allocated at the first character to rewrite
	copied := 0
	for i := 0; i < len(page); {
		r, size := rune(page[i]), 1
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRune(page[i:])
		}
		if r >= 0x7f && adapterproto.RefusedInPersonText(r) {
			out = append(out, page[copied:i]...)
			out = appendJSONEscape(out, r)
			copied = i + size
		}
		i += size
	}
	if out == nil {
		return page
	}
	return append(out, page[copied:]...)
}

// appendJSONEscape appends r as the JSON escape \uXXXX in lowercase hex, or as a
// pair of them for a character beyond U+FFFF, which JSON writes as a UTF-16
// surrogate pair. A single escape for such a character would read back as a
// different one. The digits are written by hand: a page of nothing but escapes is
// the worst case of the budget, and fmt is twice as slow there.
func appendJSONEscape(out []byte, r rune) []byte {
	if r > 0xffff {
		hi, lo := utf16.EncodeRune(r)
		return appendUnit(appendUnit(out, hi), lo)
	}
	return appendUnit(out, r)
}

// appendUnit appends one UTF-16 code unit as \uXXXX.
func appendUnit(out []byte, u rune) []byte {
	const digits = "0123456789abcdef"
	return append(out, '\\', 'u', digits[u>>12], digits[u>>8&0xf], digits[u>>4&0xf], digits[u&0xf])
}
