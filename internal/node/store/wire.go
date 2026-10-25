package store

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The body of a record is JSON written by encoding/json from the structs of
// this package and read with strictjson. A body is accepted only if encoding
// what it decodes to gives its bytes back: no two files mean the same record,
// and a bit of rot that leaves valid JSON cannot pass as another record.

var errNotCanonical = errors.New("the body is not the canonical encoding of the record it holds")

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// unmarshal decodes body strictly into v, a pointer to a wire struct, and
// checks that it is canonical.
func unmarshal(body []byte, v any) error {
	if err := strictjson.Decode(body, v); err != nil {
		return err
	}
	again, err := marshal(v)
	if err != nil {
		return err
	}
	if !bytes.Equal(again, body) {
		return errNotCanonical
	}
	return nil
}

// wtime is a time in the text form RFC 3339 in UTC with a Z, fractions of a
// second without trailing zeros. It is never the zero time: a time that is not
// there is a pointer that is nil.
type wtime time.Time

func (t wtime) MarshalJSON() ([]byte, error) {
	tt := time.Time(t)
	if tt.IsZero() {
		return nil, errors.New("the zero time is not written")
	}
	return json.Marshal(tt.UTC().Format(time.RFC3339Nano))
}

func (t *wtime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	tt, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	if tt.IsZero() || s != tt.UTC().Format(time.RFC3339Nano) {
		return fmt.Errorf("time %q is not in the canonical form", s)
	}
	*t = wtime(tt.UTC())
	return nil
}

func tptr(t time.Time) *wtime {
	if t.IsZero() {
		return nil
	}
	w := wtime(t.UTC())
	return &w
}

func tval(p *wtime) time.Time {
	if p == nil {
		return time.Time{}
	}
	return time.Time(*p)
}

func tmust(p *wtime, what string) (time.Time, error) {
	if p == nil {
		return time.Time{}, fmt.Errorf("%s is required", what)
	}
	return time.Time(*p), nil
}

func wreq(t time.Time, what string) (wtime, error) {
	if t.IsZero() {
		return wtime{}, fmt.Errorf("%s is required", what)
	}
	return wtime(t.UTC()), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, err
	}
	if base64.RawURLEncoding.EncodeToString(b) != s { // the decoder skips carriage returns and newlines
		return nil, errors.New("not in the canonical form")
	}
	return b, nil
}

func hexN(s string, n int) ([]byte, error) {
	out := make([]byte, n)
	if err := parseHex(out, s); err != nil {
		return nil, err
	}
	return out, nil
}

// compactObject checks that raw is a compact JSON object.
func compactObject(raw []byte, what string, max int) error {
	if len(raw) == 0 || raw[0] != '{' {
		return fmt.Errorf("%s is not a JSON object", what)
	}
	if max > 0 && len(raw) > max {
		return fmt.Errorf("%s is %d bytes, over %d", what, len(raw), max)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil || !bytes.Equal(buf.Bytes(), raw) {
		return fmt.Errorf("%s is not compact JSON", what)
	}
	return nil
}

// enumName is the name of a value of an enumeration of a contract package, from
// its own String method. A value with no name is refused, so that nothing is
// stored that a reader could not turn back.
func enumName[T ~uint8](v T, str func(T) string, what string) (string, error) {
	n := str(v)
	if n == "" || strings.HasPrefix(n, "invalid") {
		return "", fmt.Errorf("%s %d has no name", what, uint8(v))
	}
	return n, nil
}

// enumValue is the inverse of enumName: the value whose String is name.
func enumValue[T ~uint8](name string, str func(T) string, what string) (T, error) {
	for v := 0; v < 64; v++ {
		if n := str(T(v)); n == name && !strings.HasPrefix(n, "invalid") {
			return T(v), nil
		}
	}
	return 0, fmt.Errorf("%s %q is not one of the names", what, name)
}

// mapName and mapValue are the same for an enumeration that has no String.
type names[T comparable] []struct {
	v T
	n string
}

func (t names[T]) name(v T, what string) (string, error) {
	for _, e := range t {
		if e.v == v {
			return e.n, nil
		}
	}
	return "", fmt.Errorf("%s %v has no name", what, v)
}

func (t names[T]) value(n, what string) (T, error) {
	for _, e := range t {
		if e.n == n {
			return e.v, nil
		}
	}
	var zero T
	return zero, fmt.Errorf("%s %q is not one of the names", what, n)
}

var sideNames = names[delivery.Side]{{delivery.SideSource, "source"}, {delivery.SideTarget, "target"}}

var authNames = names[delivery.AuthorizationKind]{
	{delivery.AuthorizationAllow, "allow"}, {delivery.AuthorizationSuspend, "suspend"}, {delivery.AuthorizationDeny, "deny"},
}

// ---- body references, handout fields, covers

type bodyRefJSON struct {
	SHA256 string `json:"sha256"`
	Len    int    `json:"len"`
}

func toBodyRef(r *BodyRef) (*bodyRefJSON, error) {
	if r == nil {
		return nil, nil
	}
	if r.Len < 0 || r.Len > envelope.MaxBody {
		return nil, fmt.Errorf("body reference of %d bytes, want 0 to %d", r.Len, envelope.MaxBody)
	}
	return &bodyRefJSON{SHA256: hex.EncodeToString(r.Digest[:]), Len: r.Len}, nil
}

func fromBodyRef(j *bodyRefJSON) (*BodyRef, error) {
	if j == nil {
		return nil, nil
	}
	if j.Len < 0 || j.Len > envelope.MaxBody {
		return nil, fmt.Errorf("body reference of %d bytes, want 0 to %d", j.Len, envelope.MaxBody)
	}
	d, err := hexN(j.SHA256, 32)
	if err != nil {
		return nil, fmt.Errorf("body digest: %w", err)
	}
	r := BodyRef{Len: j.Len}
	copy(r.Digest[:], d)
	return &r, nil
}

// ---- the delivery record

type leaseJSON struct {
	Handouts  int      `json:"handouts,omitempty"`
	IDs       []string `json:"ids,omitempty"`
	Active    bool     `json:"active,omitempty"`
	IssuedAt  *wtime   `json:"issued_at,omitempty"`
	ExpiresAt *wtime   `json:"expires_at,omitempty"`
	Chunks    int      `json:"chunks,omitempty"`
	HandedOut []bool   `json:"handed_out,omitempty"`
	Acked     []bool   `json:"acked,omitempty"`
}

type recordJSON struct {
	Side            string     `json:"side"`
	State           string     `json:"state"`
	StateAt         *wtime     `json:"state_at"`
	Sub             string     `json:"sub,omitempty"`
	Reject          string     `json:"reject,omitempty"`
	Unknown         string     `json:"unknown,omitempty"`
	Lost            bool       `json:"lost,omitempty"`
	Control         bool       `json:"control,omitempty"`
	Exempt          bool       `json:"exempt,omitempty"`
	AcceptBefore    *wtime     `json:"accept_before,omitempty"`
	ReportBy        *wtime     `json:"report_by,omitempty"`
	BacklogDeadline *wtime     `json:"backlog_deadline,omitempty"`
	Reported        []string   `json:"reported,omitempty"`
	Blocked         bool       `json:"blocked,omitempty"`
	BlockedCause    string     `json:"blocked_cause,omitempty"`
	Lease           *leaseJSON `json:"lease,omitempty"`
}

func leaseIsZero(l delivery.Lease) bool {
	return l.Handouts == 0 && len(l.IDs) == 0 && !l.Active && l.IssuedAt.IsZero() && l.ExpiresAt.IsZero() &&
		l.Chunks == 0 && len(l.HandedOut) == 0 && len(l.Acked) == 0
}

func toRecord(r delivery.Record) (recordJSON, error) {
	var j recordJSON
	var err error
	if err = checkDelivery(r); err != nil {
		return j, err
	}
	if j.Side, err = sideNames.name(r.Side, "side"); err != nil {
		return j, err
	}
	if j.State, err = enumName(r.State, delivery.State.String, "state"); err != nil {
		return j, err
	}
	j.StateAt = tptr(r.StateAt) // checkDelivery refused a record without one
	switch r.Sub {
	case delivery.SubNone:
	case delivery.SubNotified:
		j.Sub = "notified"
	default:
		return j, fmt.Errorf("substate %d has no name", r.Sub)
	}
	if r.Reject != delivery.RejectNone {
		if j.Reject, err = enumName(r.Reject, delivery.RejectReason.String, "rejection reason"); err != nil {
			return j, err
		}
	}
	if r.Unknown != delivery.UnknownCauseNone {
		if j.Unknown, err = enumName(r.Unknown, delivery.UnknownCause.String, "unknown cause"); err != nil {
			return j, err
		}
	}
	j.Lost, j.Control, j.Exempt = r.Lost, r.Control, r.Exempt
	j.AcceptBefore, j.ReportBy, j.BacklogDeadline = tptr(r.AcceptBefore), tptr(r.ReportBy), tptr(r.BacklogDeadline)
	for k := delivery.KindUnreachable; k <= lastFeedbackKind; k++ {
		if r.Reported.Has(k) {
			j.Reported = append(j.Reported, k.String())
		}
	}
	if r.Reported != delivery.KindSet(0) && len(j.Reported) == 0 || kindSetBeyond(r.Reported) {
		return j, fmt.Errorf("reported set %b has a bit that is no kind", uint16(r.Reported))
	}
	j.Blocked = r.Blocked
	if r.BlockedCause != delivery.CauseNone {
		if j.BlockedCause, err = enumName(r.BlockedCause, delivery.Cause.String, "cause"); err != nil {
			return j, err
		}
	}
	if !leaseIsZero(r.Lease) {
		l := r.Lease
		lj := &leaseJSON{Handouts: l.Handouts, Active: l.Active, IssuedAt: tptr(l.IssuedAt), ExpiresAt: tptr(l.ExpiresAt), Chunks: l.Chunks,
			HandedOut: l.HandedOut, Acked: l.Acked}
		if l.Handouts < 0 || l.Chunks < 0 {
			return j, errors.New("negative lease counts")
		}
		for _, id := range l.IDs {
			if id.IsZero() {
				return j, errors.New("a lease id is unset")
			}
			lj.IDs = append(lj.IDs, id.String())
		}
		j.Lease = lj
	}
	return j, nil
}

// lastFeedbackKind is the highest feedback kind: the reported set is stored as
// the names of the kinds from the first to this one. A kind added after it is
// refused at encoding (the set has a bit that is no kind), never dropped.
const lastFeedbackKind = delivery.KindLostAtTarget

func kindSetBeyond(s delivery.KindSet) bool {
	var known delivery.KindSet
	for k := delivery.KindUnreachable; k <= lastFeedbackKind; k++ {
		known = known.With(k)
	}
	return s&^known != 0
}

func fromRecord(j recordJSON) (delivery.Record, error) {
	var r delivery.Record
	var err error
	if r.Side, err = sideNames.value(j.Side, "side"); err != nil {
		return r, err
	}
	if r.State, err = enumValue(j.State, delivery.State.String, "state"); err != nil {
		return r, err
	}
	if r.StateAt, err = tmust(j.StateAt, "state_at"); err != nil {
		return r, err
	}
	switch j.Sub {
	case "":
	case "notified":
		r.Sub = delivery.SubNotified
	default:
		return r, fmt.Errorf("substate %q is not one of the names", j.Sub)
	}
	if j.Reject != "" {
		if r.Reject, err = enumValue(j.Reject, delivery.RejectReason.String, "rejection reason"); err != nil {
			return r, err
		}
	}
	if j.Unknown != "" {
		if r.Unknown, err = enumValue(j.Unknown, delivery.UnknownCause.String, "unknown cause"); err != nil {
			return r, err
		}
	}
	r.Lost, r.Control, r.Exempt = j.Lost, j.Control, j.Exempt
	r.AcceptBefore, r.ReportBy, r.BacklogDeadline = tval(j.AcceptBefore), tval(j.ReportBy), tval(j.BacklogDeadline)
	last := delivery.Kind(0)
	for _, n := range j.Reported {
		k, err := enumValue(n, delivery.Kind.String, "feedback kind")
		if err != nil {
			return r, err
		}
		if k <= last {
			return r, errors.New("the reported kinds are not in order, once each")
		}
		last = k
		r.Reported = r.Reported.With(k)
	}
	r.Blocked = j.Blocked
	if j.BlockedCause != "" {
		if r.BlockedCause, err = enumValue(j.BlockedCause, delivery.Cause.String, "cause"); err != nil {
			return r, err
		}
	}
	if j.Lease != nil {
		l := j.Lease
		r.Lease = delivery.Lease{Handouts: l.Handouts, Active: l.Active, IssuedAt: tval(l.IssuedAt), ExpiresAt: tval(l.ExpiresAt), Chunks: l.Chunks,
			HandedOut: l.HandedOut, Acked: l.Acked}
		for _, s := range l.IDs {
			id, err := delivery.ParseLeaseID(s)
			if err != nil {
				return r, err
			}
			r.Lease.IDs = append(r.Lease.IDs, id)
		}
		if leaseIsZero(r.Lease) {
			return r, errors.New("an empty lease is not written")
		}
	}
	return r, checkDelivery(r)
}

// checkDelivery asks the delivery package whether a record, written or decoded, is one its
// constructors and transitions can produce. It is the first thing done with a
// decoded record: Apply, Tick, Handout and Ack trust the record, and a lease
// whose chunk flags disagree with its chunk count would make them index out of
// range. A record it refuses is a damaged body, whatever its checksum says.
func checkDelivery(r delivery.Record) error { return r.Valid() }

// ---- dedup, authorization, reply decision

type dedupJSON struct {
	Digest      string  `json:"digest"`
	RetainUntil wtime   `json:"retain_until"`
	Reply       *string `json:"reply,omitempty"`
	DigestLost  bool    `json:"digest_lost,omitempty"`
}

func toDedup(d delivery.DedupRecord, key TargetKey) (dedupJSON, error) {
	if d.Key != key.Dedup() {
		return dedupJSON{}, errors.New("the deduplication key is not the key of the record")
	}
	ru, err := wreq(d.RetainUntil, "the deduplication retention")
	if err != nil {
		return dedupJSON{}, err
	}
	j := dedupJSON{Digest: hex.EncodeToString(d.Digest[:]), RetainUntil: ru, DigestLost: d.DigestLost}
	if d.HasReply {
		s := b64(d.Reply)
		j.Reply = &s
	} else if len(d.Reply) != 0 {
		return j, errors.New("a reply is stored without the flag that says so")
	}
	return j, nil
}

func fromDedup(j dedupJSON, key TargetKey) (delivery.DedupRecord, error) {
	d := delivery.DedupRecord{Key: key.Dedup(), RetainUntil: time.Time(j.RetainUntil), DigestLost: j.DigestLost}
	dig, err := hexN(j.Digest, 32)
	if err != nil {
		return d, fmt.Errorf("digest: %w", err)
	}
	copy(d.Digest[:], dig)
	if j.Reply != nil {
		b, err := unb64(*j.Reply)
		if err != nil {
			return d, fmt.Errorf("reply: %w", err)
		}
		d.Reply, d.HasReply = b, true
		if len(b) == 0 {
			d.Reply = []byte{}
		}
	}
	return d, nil
}

type authJSON struct {
	Kind   string `json:"kind"`
	Reject string `json:"reject,omitempty"`
}

func toAuth(a delivery.Authorization) (*authJSON, error) {
	k, err := authNames.name(a.Kind, "authorization kind")
	if err != nil {
		return nil, err
	}
	j := &authJSON{Kind: k}
	if a.Reject != delivery.RejectNone {
		if j.Reject, err = enumName(a.Reject, delivery.RejectReason.String, "rejection reason"); err != nil {
			return nil, err
		}
	}
	return j, nil
}

func fromAuth(j *authJSON) (delivery.Authorization, error) {
	k, err := authNames.value(j.Kind, "authorization kind")
	if err != nil {
		return delivery.Authorization{}, err
	}
	a := delivery.Authorization{Kind: k}
	if j.Reject != "" {
		if a.Reject, err = enumValue(j.Reject, delivery.RejectReason.String, "rejection reason"); err != nil {
			return a, err
		}
	}
	return a, nil
}
