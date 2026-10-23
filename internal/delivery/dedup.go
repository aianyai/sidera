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
// reply instead of running again.
type DedupRecord struct {
	Key         DedupKey
	Digest      BodyDigest
	RetainUntil time.Time
	Reply       []byte
	HasReply    bool
}

// NewDedupRecord returns the record for a request that was just accepted.
func NewDedupRecord(key DedupKey, digest BodyDigest, reportBy time.Time) DedupRecord {
	return DedupRecord{Key: key, Digest: digest, RetainUntil: DedupRetainUntil(reportBy)}
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
// there is none. A record that outlived its retention still counts: a retry
// that late is refused as expired by Intake anyway, so keeping or dropping the
// record gives the same answer.
func CheckDedup(existing *DedupRecord, digest BodyDigest) DedupVerdict {
	switch {
	case existing == nil:
		return DedupNew
	case existing.Digest == digest:
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
