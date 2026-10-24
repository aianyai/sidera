package delivery

import (
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// MessageID is the sender-generated unique identifier of a message. It stays
// the same across retries, paths and relays; one multi-recipient message has
// one. It is compared byte for byte.
type MessageID string

// BodyDigest is a digest of the request's content as computed by the caller. It
// is only compared for equality.
type BodyDigest [32]byte

// DedupKey identifies a request for deduplication. The recipient is part of
// the key because a multi-recipient message is delivered once per recipient.
type DedupKey struct {
	SenderNode ident.NodeID
	Message    MessageID
	Recipient  ident.EndpointID
}

// DedupRecord is the target's memory of one accepted request. A control signal
// stores its reply in the same record so a repeated request gets the stored
// reply instead of running again. The stored reply outlives the record's
// RetainUntil: it must be kept until reply.RetainFactsUntil, so storage does not
// purge it with the deduplication record.
type DedupRecord struct {
	Key         DedupKey
	Digest      BodyDigest
	RetainUntil time.Time
	Reply       []byte
	HasReply    bool
	// DigestLost marks the record that replaces one lost, together with its
	// delivery record, to storage corruption: the digest is unknown, so every
	// request under the key is a replay of the lost one. Treating the request as
	// new would accept and deliver the message a second time.
	DigestLost bool
}

// NewDedupRecord returns the record for a request that was just accepted.
func NewDedupRecord(key DedupKey, digest BodyDigest, reportBy time.Time) DedupRecord {
	return DedupRecord{Key: key, Digest: digest, RetainUntil: DedupRetainUntil(reportBy)}
}

// NewLostDedupRecord returns the deduplication record that replaces one lost to
// storage corruption, discovered at discoveredAt. It is kept as long as the
// record it replaces could have been: until DedupRetainUntil(LostReportBy).
// discoveredAt is the instant the delivery record's replacement was made from,
// so the two records of one loss end together: the caller passes the very value
// it first gave NewLostRecord, which has to be one that call accepted.
func NewLostDedupRecord(key DedupKey, discoveredAt time.Time) DedupRecord {
	return DedupRecord{Key: key, RetainUntil: DedupRetainUntil(LostReportBy(discoveredAt)), DigestLost: true}
}

// DedupVerdict says how to treat a request.
type DedupVerdict uint8

const (
	// DedupNew: no record exists; handle the request.
	DedupNew DedupVerdict = iota + 1
	// DedupReplay: the same request again; answer with the original record.
	DedupReplay
	// DedupConflict: the same identifier with a different body; answer with a
	// conflict and change nothing.
	DedupConflict
)

// CheckDedup compares a request with the record stored under its key, nil when
// there is none. A record whose digest was lost (DigestLost) cannot tell a
// conflict from a replay and always answers as a replay: the one answer that
// cannot deliver the message twice. A record that outlived its retention still counts: a retry
// that late is refused as expired by Intake anyway, so keeping or dropping the
// record gives the same answer.
func CheckDedup(existing *DedupRecord, digest BodyDigest) DedupVerdict {
	switch {
	case existing == nil:
		return DedupNew
	case existing.DigestLost, existing.Digest == digest:
		return DedupReplay
	}
	return DedupConflict
}

// ErrReplyStored reports an attempt to store a second reply.
var ErrReplyStored = errors.New("a reply is already stored")

// WithReply returns the record with the control reply stored. The reply is
// stored once, in the same transaction as the execution result; a second reply
// would mean the signal ran twice.
func (d DedupRecord) WithReply(reply []byte) (DedupRecord, error) {
	if d.HasReply {
		return d, ErrReplyStored
	}
	d.Reply = append([]byte(nil), reply...)
	d.HasReply = true
	return d, nil
}
