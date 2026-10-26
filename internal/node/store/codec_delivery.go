package store

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/reply"
)

// ---- tracked deliveries and feedback items

type arrivalJSON struct {
	DeliveredAt *wtime `json:"delivered_at,omitempty"`
	ArrivedAt   *wtime `json:"arrived_at,omitempty"`
}

type trackedJSON struct {
	Address  string       `json:"address"`
	Delivery recordJSON   `json:"delivery"`
	Arrival  *arrivalJSON `json:"arrival,omitempty"`
}

func toTracked(t envelope.Tracked) (trackedJSON, error) {
	if t.Address.IsZero() {
		return trackedJSON{}, errors.New("a tracked delivery without an address")
	}
	rj, err := toRecord(t.Delivery)
	if err != nil {
		return trackedJSON{}, err
	}
	j := trackedJSON{Address: t.Address.String(), Delivery: rj}
	if !t.Arrival.DeliveredAt.IsZero() || !t.Arrival.ArrivedAt.IsZero() {
		j.Arrival = &arrivalJSON{DeliveredAt: tptr(t.Arrival.DeliveredAt), ArrivedAt: tptr(t.Arrival.ArrivedAt)}
	}
	return j, nil
}

func fromTracked(j trackedJSON) (envelope.Tracked, error) {
	a, err := ident.ParseAddress(j.Address)
	if err != nil {
		return envelope.Tracked{}, err
	}
	d, err := fromRecord(j.Delivery)
	if err != nil {
		return envelope.Tracked{}, err
	}
	t := envelope.Tracked{Address: a, Delivery: d}
	if j.Arrival != nil {
		t.Arrival = envelope.Arrival{DeliveredAt: tval(j.Arrival.DeliveredAt), ArrivedAt: tval(j.Arrival.ArrivedAt)}
		if t.Arrival.DeliveredAt.IsZero() && t.Arrival.ArrivedAt.IsZero() {
			return t, errors.New("an empty arrival is not written")
		}
	}
	return t, nil
}

type itemJSON struct {
	Sender    string `json:"sender"`
	Message   string `json:"message"`
	Recipient string `json:"recipient"`
	Kind      string `json:"kind"`
	Cause     string `json:"cause,omitempty"`
	Reject    string `json:"reject,omitempty"`
	State     string `json:"state,omitempty"`
	At        wtime  `json:"at"`
}

func toItem(it delivery.Item) (itemJSON, error) {
	if it.Ref.Sender.IsZero() || it.Ref.Recipient.IsZero() {
		return itemJSON{}, errors.New("a feedback item without addresses")
	}
	if err := envelope.ValidMessageID(it.Ref.Message); err != nil {
		return itemJSON{}, err
	}
	at, err := wreq(it.At, "the time of a feedback item")
	if err != nil {
		return itemJSON{}, err
	}
	j := itemJSON{Sender: it.Ref.Sender.String(), Message: string(it.Ref.Message), Recipient: it.Ref.Recipient.String(), At: at}
	if j.Kind, err = enumName(it.Feedback.Kind, delivery.Kind.String, "feedback kind"); err != nil {
		return j, err
	}
	if it.Feedback.Cause != delivery.CauseNone {
		if j.Cause, err = enumName(it.Feedback.Cause, delivery.Cause.String, "cause"); err != nil {
			return j, err
		}
	}
	if it.Feedback.Reject != delivery.RejectNone {
		if j.Reject, err = enumName(it.Feedback.Reject, delivery.RejectReason.String, "rejection reason"); err != nil {
			return j, err
		}
	}
	if it.Feedback.State != delivery.StateInvalid {
		if j.State, err = enumName(it.Feedback.State, delivery.State.String, "state"); err != nil {
			return j, err
		}
	}
	return j, nil
}

func fromItem(j itemJSON) (delivery.Item, error) {
	var it delivery.Item
	var err error
	if it.Ref.Sender, err = ident.ParseAddress(j.Sender); err != nil {
		return it, err
	}
	if it.Ref.Recipient, err = ident.ParseAddress(j.Recipient); err != nil {
		return it, err
	}
	it.Ref.Message = delivery.MessageID(j.Message)
	if err := envelope.ValidMessageID(it.Ref.Message); err != nil {
		return it, err
	}
	it.At = time.Time(j.At)
	if it.Feedback.Kind, err = enumValue(j.Kind, delivery.Kind.String, "feedback kind"); err != nil {
		return it, err
	}
	if j.Cause != "" {
		if it.Feedback.Cause, err = enumValue(j.Cause, delivery.Cause.String, "cause"); err != nil {
			return it, err
		}
	}
	if j.Reject != "" {
		if it.Feedback.Reject, err = enumValue(j.Reject, delivery.RejectReason.String, "rejection reason"); err != nil {
			return it, err
		}
	}
	if j.State != "" {
		if it.Feedback.State, err = enumValue(j.State, delivery.State.String, "state"); err != nil {
			return it, err
		}
	}
	return it, nil
}

func toItems(items []delivery.Item) ([]itemJSON, error) {
	var out []itemJSON
	for _, it := range items {
		j, err := toItem(it)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

func fromItems(js []itemJSON) ([]delivery.Item, error) {
	var out []delivery.Item
	for _, j := range js {
		it, err := fromItem(j)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// ---- source records

type lostOutJSON struct {
	At         wtime        `json:"at"`
	Deliveries []recordJSON `json:"deliveries"`
}

type seqEntryJSON struct {
	Endpoint string `json:"endpoint"`
	Seq      uint64 `json:"seq"`
}

type idemJSON struct {
	Key         string `json:"key"`
	RetainUntil wtime  `json:"retain_until"`
}

type outJSON struct {
	Lost               *lostOutJSON   `json:"lost,omitempty"`
	Kind               string         `json:"kind,omitempty"`
	BodyNonEmpty       bool           `json:"body_non_empty,omitempty"`
	AcceptBefore       *wtime         `json:"accept_before,omitempty"`
	Recipients         []trackedJSON  `json:"recipients,omitempty"`
	ControlReplyDigest string         `json:"control_reply_digest,omitempty"`
	Derived            *trackedJSON   `json:"derived,omitempty"`
	To                 []string       `json:"to,omitempty"`
	DiscloseRecipients bool           `json:"disclose_recipients,omitempty"`
	ReplyTo            string         `json:"reply_to,omitempty"`
	Thread             string         `json:"thread,omitempty"`
	Seq                []seqEntryJSON `json:"seq,omitempty"`
	SentAt             *wtime         `json:"sent_at,omitempty"`
	ReportBy           *wtime         `json:"report_by,omitempty"`
	ContentType        string         `json:"content_type,omitempty"`
	Preset             string         `json:"preset,omitempty"`
	Body               *bodyRefJSON   `json:"body,omitempty"`
	BodyDigest         string         `json:"body_digest,omitempty"`
	Idempotency        *idemJSON      `json:"idempotency,omitempty"`
	ControlReply       string         `json:"control_reply,omitempty"`
	Feedback           []itemJSON     `json:"feedback,omitempty"`
}

var envKindNames = envelope.Kind.String

func encodeOut(r OutRecord) ([]byte, error) {
	key, err := r.Key.Bytes()
	if err != nil {
		return nil, err
	}
	_ = key
	var j outJSON
	if j.Feedback, err = toItems(r.Feedback); err != nil {
		return nil, err
	}
	if r.Lost != nil {
		if !zeroRegularOut(r) {
			return nil, errors.New("an out record is either lost or regular, not both")
		}
		if len(r.Lost.Deliveries) != len(r.Key.Recipients) {
			return nil, fmt.Errorf("a lost out record needs one delivery per recipient of its key: %d for %d", len(r.Lost.Deliveries), len(r.Key.Recipients))
		}
		at, err := wreq(r.Lost.DiscoveredAt, "the discovery time")
		if err != nil {
			return nil, err
		}
		j.Lost = &lostOutJSON{At: at}
		for _, d := range r.Lost.Deliveries {
			rj, err := toRecord(d)
			if err != nil {
				return nil, err
			}
			if d.Side != delivery.SideSource {
				return nil, errors.New("a source record holds deliveries of the source side")
			}
			j.Lost.Deliveries = append(j.Lost.Deliveries, rj)
		}
		return marshal(j)
	}
	o := r.Outbound
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if o.Message != r.Key.Message || o.From != r.Key.Sender {
		return nil, errors.New("the outbound record is not the one of the key")
	}
	if o.Kind.IsControl() != r.Key.Control {
		return nil, fmt.Errorf("a record of kind %s is a control signal: %v, its key says %v", o.Kind, o.Kind.IsControl(), r.Key.Control)
	}
	if len(o.Recipients) != len(r.Key.Recipients) {
		return nil, errors.New("the recipients of the outbound record are not those of the key")
	}
	for i, t := range o.Recipients {
		if t.Address != r.Key.Recipients[i] {
			return nil, errors.New("the recipients of the outbound record are not those of the key")
		}
		tj, err := toTracked(t)
		if err != nil {
			return nil, err
		}
		j.Recipients = append(j.Recipients, tj)
	}
	j.Kind, err = enumName(o.Kind, envKindNames, "envelope kind")
	if err != nil {
		return nil, err
	}
	j.BodyNonEmpty = o.BodyNonEmpty
	if j.AcceptBefore = tptr(o.AcceptBefore); j.AcceptBefore == nil {
		return nil, errors.New("the delivery deadline is required")
	}
	if !o.ControlReplyDigest.IsZero() {
		j.ControlReplyDigest = o.ControlReplyDigest.String()
	}
	if o.Derived != nil {
		tj, err := toTracked(*o.Derived)
		if err != nil {
			return nil, err
		}
		j.Derived = &tj
	}
	e := r.Envelope
	if !slices.Equal(e.To, r.Key.Recipients) {
		return nil, errors.New("the envelope's recipients are not those of the key")
	}
	for _, a := range e.To {
		j.To = append(j.To, a.String())
	}
	if e.ReplyTo != "" {
		if err := envelope.ValidMessageID(e.ReplyTo); err != nil {
			return nil, err
		}
	}
	j.DiscloseRecipients = e.DiscloseRecipients
	j.ReplyTo, j.Thread = string(e.ReplyTo), e.Thread
	for ep, n := range e.Seq {
		if n == 0 {
			return nil, errors.New("a sequence number of zero")
		}
		if !slices.ContainsFunc(e.To, func(a ident.Address) bool { return a.Endpoint == ep }) {
			return nil, errors.New("a sequence number for an endpoint that is no recipient")
		}
		j.Seq = append(j.Seq, seqEntryJSON{Endpoint: ep.String(), Seq: n})
	}
	slices.SortFunc(j.Seq, func(a, b seqEntryJSON) int { return strings.Compare(a.Endpoint, b.Endpoint) })
	if j.SentAt = tptr(e.SentAt); j.SentAt == nil {
		return nil, errors.New("the send time is required")
	}
	if j.ReportBy = tptr(e.ReportBy); j.ReportBy == nil {
		return nil, errors.New("report_by is required")
	}
	j.ContentType, j.Preset = e.ContentType, e.Preset
	if r.Body != nil && !o.BodyNonEmpty {
		return nil, errors.New("a body reference on a message without a body")
	}
	if j.Body, err = toBodyRef(r.Body); err != nil {
		return nil, err
	}
	j.BodyDigest = hex.EncodeToString(r.BodyDigest[:])
	if r.Idempotency != nil {
		ru, err := wreq(r.Idempotency.RetainUntil, "the idempotency retention")
		if err != nil {
			return nil, err
		}
		if r.Idempotency.Key == "" {
			return nil, errors.New("an empty idempotency key")
		}
		j.Idempotency = &idemJSON{Key: r.Idempotency.Key, RetainUntil: ru}
	}
	if len(r.ControlReply) > 0 {
		if len(r.ControlReply) > record.DefaultMaxInput {
			return nil, errors.New("the control reply is too large")
		}
		j.ControlReply = b64(r.ControlReply)
	}
	return marshal(j)
}

func zeroRegularOut(r OutRecord) bool {
	o := r.Outbound
	return o.Message == "" && o.From.IsZero() && o.Kind == 0 && !o.BodyNonEmpty && o.AcceptBefore.IsZero() && len(o.Recipients) == 0 &&
		o.ControlReplyDigest.IsZero() && o.Derived == nil && len(r.Envelope.To) == 0 && !r.Envelope.DiscloseRecipients && r.Envelope.ReplyTo == "" && r.Envelope.Thread == "" &&
		len(r.Envelope.Seq) == 0 && r.Envelope.SentAt.IsZero() && r.Envelope.ReportBy.IsZero() && r.Envelope.ContentType == "" && r.Envelope.Preset == "" &&
		r.Body == nil && r.BodyDigest == delivery.BodyDigest{} && r.Idempotency == nil && len(r.ControlReply) == 0
}

func decodeOut(key OutKey, body []byte) (OutRecord, error) {
	var j outJSON
	if err := unmarshal(body, &j); err != nil {
		return OutRecord{}, err
	}
	r := OutRecord{Key: key}
	var err error
	if r.Feedback, err = fromItems(j.Feedback); err != nil {
		return r, err
	}
	if j.Lost != nil {
		rest := j
		rest.Lost, rest.Feedback = nil, nil
		if rest.Kind != "" || rest.BodyNonEmpty || rest.AcceptBefore != nil || rest.Recipients != nil || rest.ControlReplyDigest != "" || rest.Derived != nil ||
			rest.To != nil || rest.DiscloseRecipients || rest.ReplyTo != "" || rest.Thread != "" || rest.Seq != nil || rest.SentAt != nil || rest.ReportBy != nil || rest.ContentType != "" ||
			rest.Preset != "" || rest.Body != nil || rest.BodyDigest != "" || rest.Idempotency != nil || rest.ControlReply != "" {
			return r, errors.New("a lost out record holds the parts of a regular one")
		}
		if len(j.Lost.Deliveries) != len(key.Recipients) {
			return r, errors.New("a lost out record needs one delivery per recipient of its key")
		}
		r.Lost = &LostOut{DiscoveredAt: time.Time(j.Lost.At)}
		for _, rj := range j.Lost.Deliveries {
			d, err := fromRecord(rj)
			if err != nil {
				return r, err
			}
			if d.Side != delivery.SideSource {
				return r, errors.New("a source record holds deliveries of the source side")
			}
			r.Lost.Deliveries = append(r.Lost.Deliveries, d)
		}
		return r, nil
	}
	kind, err := enumValue(j.Kind, envKindNames, "envelope kind")
	if err != nil {
		return r, err
	}
	if kind.IsControl() != key.Control {
		return r, errors.New("the kind of the record is not the kind of its file")
	}
	ab, err := tmust(j.AcceptBefore, "accept_before")
	if err != nil {
		return r, err
	}
	r.Outbound = envelope.Outbound{Message: key.Message, From: key.Sender, Kind: kind, BodyNonEmpty: j.BodyNonEmpty, AcceptBefore: ab}
	if len(j.Recipients) != len(key.Recipients) {
		return r, errors.New("the recipients of the record are not those of its key")
	}
	for i, tj := range j.Recipients {
		t, err := fromTracked(tj)
		if err != nil {
			return r, err
		}
		if t.Address != key.Recipients[i] {
			return r, errors.New("the recipients of the record are not those of its key")
		}
		r.Outbound.Recipients = append(r.Outbound.Recipients, t)
	}
	if j.ControlReplyDigest != "" {
		d, err := hexN(j.ControlReplyDigest, 32)
		if err != nil {
			return r, fmt.Errorf("control reply digest: %w", err)
		}
		copy(r.Outbound.ControlReplyDigest[:], d)
		if r.Outbound.ControlReplyDigest.IsZero() {
			return r, errors.New("a control reply digest of zeros")
		}
	}
	if j.Derived != nil {
		t, err := fromTracked(*j.Derived)
		if err != nil {
			return r, err
		}
		r.Outbound.Derived = &t
	}
	if len(j.To) != len(key.Recipients) {
		return r, errors.New("the envelope's recipients are not those of the key")
	}
	for i, s := range j.To {
		a, err := ident.ParseAddress(s)
		if err != nil {
			return r, err
		}
		if a != key.Recipients[i] {
			return r, errors.New("the envelope's recipients are not those of the key")
		}
		r.Envelope.To = append(r.Envelope.To, a)
	}
	r.Envelope.DiscloseRecipients = j.DiscloseRecipients
	r.Envelope.ReplyTo, r.Envelope.Thread = delivery.MessageID(j.ReplyTo), j.Thread
	if j.ReplyTo != "" {
		if err := envelope.ValidMessageID(r.Envelope.ReplyTo); err != nil {
			return r, err
		}
	}
	if len(j.Seq) > 0 {
		r.Envelope.Seq = map[ident.EndpointID]uint64{}
	}
	last := ""
	for _, s := range j.Seq {
		e, err := ident.ParseEndpointID(s.Endpoint)
		if err != nil {
			return r, err
		}
		if s.Endpoint <= last {
			return r, errors.New("the sequence numbers are not in order, once for each endpoint")
		}
		last = s.Endpoint
		if s.Seq == 0 || !slices.ContainsFunc(r.Envelope.To, func(a ident.Address) bool { return a.Endpoint == e }) {
			return r, errors.New("a sequence number that is zero or of an endpoint that is no recipient")
		}
		r.Envelope.Seq[e] = s.Seq
	}
	if r.Envelope.SentAt, err = tmust(j.SentAt, "sent_at"); err != nil {
		return r, err
	}
	if r.Envelope.ReportBy, err = tmust(j.ReportBy, "report_by"); err != nil {
		return r, err
	}
	r.Envelope.ContentType, r.Envelope.Preset = j.ContentType, j.Preset
	if r.Body, err = fromBodyRef(j.Body); err != nil {
		return r, err
	}
	if r.Body != nil && !j.BodyNonEmpty {
		return r, errors.New("a body reference on a message without a body")
	}
	dig, err := hexN(j.BodyDigest, 32)
	if err != nil {
		return r, fmt.Errorf("body digest: %w", err)
	}
	copy(r.BodyDigest[:], dig)
	if j.Idempotency != nil {
		if j.Idempotency.Key == "" {
			return r, errors.New("an empty idempotency key")
		}
		r.Idempotency = &Idempotency{Key: j.Idempotency.Key, RetainUntil: time.Time(j.Idempotency.RetainUntil)}
	}
	if j.ControlReply != "" {
		if r.ControlReply, err = unb64(j.ControlReply); err != nil {
			return r, fmt.Errorf("control reply: %w", err)
		}
		if len(r.ControlReply) > record.DefaultMaxInput {
			return r, errors.New("the control reply is too large")
		}
	}
	if err := r.Outbound.Validate(); err != nil {
		return r, err
	}
	return r, nil
}

// ---- target records

type handoutJSON struct {
	ContentType string `json:"content_type,omitempty"`
	ReplyTo     string `json:"reply_to,omitempty"`
	Thread      string `json:"thread,omitempty"`
	Seq         uint64 `json:"seq,omitempty"`
	SentAt      wtime  `json:"sent_at"`
}

type replyJSON struct {
	Reason string `json:"reason"`
	Until  *wtime `json:"until,omitempty"`
}

type derivedJSON struct {
	From        string          `json:"from"`
	AuthRequest json.RawMessage `json:"auth_request"`
}

type coverJSON struct {
	Out       string `json:"out"`
	Recipient string `json:"recipient"`
	Kind      string `json:"kind"`
}

type inJSON struct {
	Rev        *uint64      `json:"rev,omitempty"`
	Record     recordJSON   `json:"record"`
	Dedup      dedupJSON    `json:"dedup"`
	Auth       *authJSON    `json:"auth,omitempty"`
	Reply      *replyJSON   `json:"reply,omitempty"`
	Handout    *handoutJSON `json:"handout,omitempty"`
	AcceptedAt *wtime       `json:"accepted_at,omitempty"`
	Body       *bodyRefJSON `json:"body,omitempty"`
	Derived    *derivedJSON `json:"derived,omitempty"`
	Covers     []coverJSON  `json:"covers,omitempty"`
}

func toHandout(h HandoutFields) (*handoutJSON, error) {
	at, err := wreq(h.SentAt, "the send time")
	if err != nil {
		return nil, err
	}
	if h.ReplyTo != "" {
		if err := envelope.ValidMessageID(h.ReplyTo); err != nil {
			return nil, err
		}
	}
	return &handoutJSON{ContentType: h.ContentType, ReplyTo: string(h.ReplyTo), Thread: h.Thread, Seq: h.Seq, SentAt: at}, nil
}

func fromHandout(j *handoutJSON) (HandoutFields, error) {
	h := HandoutFields{ContentType: j.ContentType, ReplyTo: delivery.MessageID(j.ReplyTo), Thread: j.Thread, Seq: j.Seq, SentAt: time.Time(j.SentAt)}
	if h.ReplyTo != "" {
		if err := envelope.ValidMessageID(h.ReplyTo); err != nil {
			return h, err
		}
	}
	return h, nil
}

func outFileHash(name string) (string, error) {
	h, ok := strings.CutSuffix(name, ".rec")
	if !ok || len(h) != 64 {
		return "", fmt.Errorf("%q is not the name of a record file", name)
	}
	if _, err := hexN(h, 32); err != nil {
		return "", err
	}
	return h, nil
}

// checkVerdictParts is the one statement of which verdicts a regular in record
// holds, read by encoding and decoding alike. A record of exempt traffic, a
// notice of the system endpoint, is not judged by the policy and a system
// endpoint cannot be replied to, so it holds neither the verdict of the
// authorization check nor the reply permission. Every other record holds both,
// so that one which lost them in storage cannot pass for one that never had
// them. The exemption is the record's own (delivery.Record.Exempt); the sender
// must agree with it, because the system endpoint is the only sender of exempt
// traffic to an endpoint and is never a sender the policy judges.
func checkVerdictParts(key TargetKey, exempt, hasAuth, hasReply bool) error {
	switch {
	case exempt != key.Sender.IsSystem():
		return errExemptSender
	case exempt && (hasAuth || hasReply):
		return errExemptVerdict
	case !exempt && !(hasAuth && hasReply):
		return errMissingVerdict
	}
	return nil
}

var (
	errExemptSender   = errors.New("a record is exempt from the policy exactly when the system endpoint sent it")
	errExemptVerdict  = errors.New("a record of exempt traffic holds no verdict of the authorization check and no reply permission")
	errMissingVerdict = errors.New("a record of traffic the policy judges holds the verdict of the authorization check and the reply permission")
)

func encodeIn(r InRecord) ([]byte, error) {
	if _, err := r.Key.Bytes(); err != nil {
		return nil, err
	}
	var j inJSON
	rj, err := toRecord(r.Record)
	if err != nil {
		return nil, err
	}
	if r.Record.Side != delivery.SideTarget || r.Record.Control {
		return nil, errors.New("an in record holds a delivery of the target side that is not a control signal")
	}
	j.Record = rj
	if j.Dedup, err = toDedup(r.Dedup, r.Key); err != nil {
		return nil, err
	}
	if r.Dedup.HasReply {
		return nil, errors.New("an in record does not store a control reply")
	}
	if r.Rev < 1 {
		return nil, errors.New("an in record has a revision of at least 1")
	}
	rev := r.Rev
	j.Rev = &rev
	if err := checkVerdictParts(r.Key, r.Record.Exempt, r.Auth != (delivery.Authorization{}), r.ReplyDecision != (reply.Decision{})); err != nil {
		return nil, err
	}
	if r.Auth != (delivery.Authorization{}) {
		if j.Auth, err = toAuth(r.Auth); err != nil {
			return nil, err
		}
	}
	if r.ReplyDecision != (reply.Decision{}) {
		rn, err := enumName(r.ReplyDecision.Reason, reply.Reason.String, "reply reason")
		if err != nil {
			return nil, err
		}
		j.Reply = &replyJSON{Reason: rn, Until: tptr(r.ReplyDecision.Until)}
	}
	if j.Handout, err = toHandout(r.Handout); err != nil {
		return nil, err
	}
	if j.AcceptedAt = tptr(r.AcceptedAt); j.AcceptedAt == nil {
		return nil, errors.New("the acceptance time is required")
	}
	if j.Body, err = toBodyRef(r.Body); err != nil {
		return nil, err
	}
	if r.Derived != nil {
		if r.Derived.From.IsZero() {
			return nil, errors.New("a derived delivery without the endpoint it derives from")
		}
		if err := compactObject(r.Derived.AuthRequest, "the authorization request", 0); err != nil {
			return nil, err
		}
		j.Derived = &derivedJSON{From: r.Derived.From.String(), AuthRequest: json.RawMessage(r.Derived.AuthRequest)}
	}
	for _, c := range r.Covers {
		h, err := outFileHash(c.Out)
		if err != nil {
			return nil, err
		}
		if c.Recipient.IsZero() {
			return nil, errors.New("a cover without a recipient")
		}
		kn, err := enumName(c.Kind, delivery.Kind.String, "feedback kind")
		if err != nil {
			return nil, err
		}
		j.Covers = append(j.Covers, coverJSON{Out: h, Recipient: c.Recipient.String(), Kind: kn})
	}
	return marshal(j)
}

func decodeIn(key TargetKey, body []byte) (InRecord, error) {
	var j inJSON
	if err := unmarshal(body, &j); err != nil {
		return InRecord{}, err
	}
	r := InRecord{Key: key}
	var err error
	if r.Record, err = fromRecord(j.Record); err != nil {
		return r, err
	}
	if r.Record.Side != delivery.SideTarget || r.Record.Control {
		return r, errors.New("an in record holds a delivery of the target side that is not a control signal")
	}
	if r.Dedup, err = fromDedup(j.Dedup, key); err != nil {
		return r, err
	}
	if r.Dedup.HasReply {
		return r, errors.New("an in record does not store a control reply")
	}
	if j.Rev == nil || *j.Rev < 1 {
		return r, errors.New("an in record has a revision of at least 1")
	}
	r.Rev = *j.Rev
	if j.Handout == nil || j.AcceptedAt == nil {
		return r, errors.New("an in record needs its handout fields and acceptance time")
	}
	if err := checkVerdictParts(key, r.Record.Exempt, j.Auth != nil, j.Reply != nil); err != nil {
		return r, err
	}
	if j.Auth != nil {
		if r.Auth, err = fromAuth(j.Auth); err != nil {
			return r, err
		}
	}
	if j.Reply != nil {
		if r.ReplyDecision.Reason, err = enumValue(j.Reply.Reason, reply.Reason.String, "reply reason"); err != nil {
			return r, err
		}
		r.ReplyDecision.Until = tval(j.Reply.Until)
	}
	if r.Handout, err = fromHandout(j.Handout); err != nil {
		return r, err
	}
	r.AcceptedAt = time.Time(*j.AcceptedAt)
	if r.Body, err = fromBodyRef(j.Body); err != nil {
		return r, err
	}
	if j.Derived != nil {
		from, err := ident.ParseEndpointID(j.Derived.From)
		if err != nil {
			return r, err
		}
		if err := compactObject(j.Derived.AuthRequest, "the authorization request", 0); err != nil {
			return r, err
		}
		r.Derived = &DerivedInfo{From: from, AuthRequest: j.Derived.AuthRequest}
	}
	for _, c := range j.Covers {
		if _, err := hexN(c.Out, 32); err != nil {
			return r, err
		}
		rcp, err := ident.ParseAddress(c.Recipient)
		if err != nil {
			return r, err
		}
		kind, err := enumValue(c.Kind, delivery.Kind.String, "feedback kind")
		if err != nil {
			return r, err
		}
		r.Covers = append(r.Covers, CoverRef{Out: c.Out + ".rec", Recipient: rcp, Kind: kind})
	}
	return r, nil
}
