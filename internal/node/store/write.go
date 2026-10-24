package store

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/txn"
)

// The methods of this file are the transactions of the same name. Each opens a
// transaction, writes in the order of its declaration, commits and returns; an
// error is the txn error of the failed write, with its class, never wrapped
// into another. The caller acknowledges and updates its memory only after nil.
//
// A record that replaces another is passed whole. A target record carries the
// revision it was read with, and is written with the next one.

// Intake records a message accepted from a local sender: the body, if any, and
// the source record.
func (s *Store) Intake(body []byte, out OutRecord) error { panic(errNotImplemented) }

// Deliver records the messages accepted for the endpoints of this node. The
// body is nil when the message was rejected, or when it is already there
// because the source on this node wrote it.
func (s *Store) Deliver(body []byte, ins []InRecord) error { panic(errNotImplemented) }

// DeliverReply records a reply that carries evidence: the updated source
// record of the message it answers, if the evidence changed it, before the
// target records of the reply.
func (s *Store) DeliverReply(body []byte, out *OutRecord, ins []InRecord) error {
	panic(errNotImplemented)
}

// SourceUpdate replaces one source record and removes the bodies whose last
// reference it dropped.
func (s *Store) SourceUpdate(out OutRecord, release []BodyRef) error { panic(errNotImplemented) }

// ControlIntake records a control signal accepted on the target: the initial
// message's body and the session endpoint it created, as preparation, then the
// control record.
func (s *Store) ControlIntake(body []byte, endpoint *EndpointRecord, ctl CtlRecord) error {
	panic(errNotImplemented)
}

// ControlResult records the result of a control signal, then the session
// endpoint it moves, then removes the bodies it released.
func (s *Store) ControlResult(ctl CtlRecord, endpoint *EndpointRecord, release []BodyRef) error {
	panic(errNotImplemented)
}

// ControlReply replaces a source record that handled a control reply.
func (s *Store) ControlReply(out OutRecord) error { panic(errNotImplemented) }

// PromptBegin records that a prompt is about to be handed to an adapter.
func (s *Store) PromptBegin(a AttemptsRecord) error { panic(errNotImplemented) }

// PromptEnd records the result of a prompt, then the notified target records
// its Pending lists.
func (s *Store) PromptEnd(a AttemptsRecord, ins []InRecord) error { panic(errNotImplemented) }

// Blocked records a blocked report, then the target records its Pending lists.
func (s *Store) Blocked(a AttemptsRecord, ins []InRecord) error { panic(errNotImplemented) }

// Lease records the leases of one page of target records.
func (s *Store) Lease(ins []InRecord) error { panic(errNotImplemented) }

// Ack records acknowledgements and removes the bodies they released.
func (s *Store) Ack(ins []InRecord, release []BodyRef) error { panic(errNotImplemented) }

// TickBatch is the records one Tick moved.
type TickBatch struct {
	Out     []OutRecord
	In      []InRecord
	Ctl     []CtlRecord
	Release []BodyRef
}

// Tick records records moved by the passing of time.
func (s *Store) Tick(b TickBatch) error { panic(errNotImplemented) }

// EndpointUpdate replaces an endpoint record.
func (s *Store) EndpointUpdate(ep EndpointRecord) error { panic(errNotImplemented) }

// EndpointClose records the closing of an endpoint, the head record of a
// closure, and the result of the operation that asked for it, if any.
func (s *Store) EndpointClose(ep EndpointRecord, result *AdminRecord) error {
	panic(errNotImplemented)
}

// DrainBatch is a chunk of the records closed because their endpoint is.
type DrainBatch struct {
	In      []InRecord
	Ctl     []CtlRecord
	Release []BodyRef
}

// EndpointDrain records a chunk of the deliveries closed by an endpoint's
// closing.
func (s *Store) EndpointDrain(b DrainBatch) error { panic(errNotImplemented) }

// InstanceDrain records the closing of the endpoints of a disabled instance
// and a chunk of the deliveries closed with them.
func (s *Store) InstanceDrain(closed []EndpointRecord, b DrainBatch) error {
	panic(errNotImplemented)
}

// FeedbackForm records a notice to a sender's endpoint and its body.
func (s *Store) FeedbackForm(body []byte, notice InRecord) error { panic(errNotImplemented) }

// FeedbackClear replaces the source records whose feedback items were covered
// by a notice.
func (s *Store) FeedbackClear(outs []OutRecord) error { panic(errNotImplemented) }

// CheckpointPut records an adapter's checkpoint.
func (s *Store) CheckpointPut(c CheckpointRecord) error { panic(errNotImplemented) }

// AlertRaise records an owner alert.
func (s *Store) AlertRaise(a AlertRecord) error { panic(errNotImplemented) }

// DaemonStop records the result of the stop operation.
func (s *Store) DaemonStop(result AdminRecord) error { panic(errNotImplemented) }

// AlertAckInput is an acknowledgement of an alert by the owner.
type AlertAckInput struct {
	Alert  AlertRecord // the alert as it was read, Acked set
	Result AdminRecord
	// Quarantined is the file the alert names when it is one of a directory
	// other than the delivery ones: it is removed before the alert is marked.
	// Nil when there is none or when the clean-up belongs to the sweep.
	Quarantined *CorruptFile
}

// AlertAck records an acknowledgement.
func (s *Store) AlertAck(a AlertAckInput) error { panic(errNotImplemented) }

// PutEndpoint writes an endpoint record in a transaction another package
// opened, under the role the declaration gives it (RoleEndpoint by
// convention). The package that opened the transaction does the commit.
func PutEndpoint(tx *txn.Tx, role string, ep EndpointRecord) error { panic(errNotImplemented) }

// PutAdminResult writes the result of an administrative operation in a
// transaction another package opened, under the role the declaration gives it
// (RoleResult by convention).
func PutAdminResult(tx *txn.Tx, role string, r AdminRecord) error { panic(errNotImplemented) }

// ArchiveEvent is one diagnostic event of the archive.
type ArchiveEvent struct {
	At    time.Time
	File  string // the file name hash of the record, 64 hex digits
	Event string
	N     *int
}

// ArchiveEvents appends a batch of events to the archive file of the day of at.
// Nothing is synced: events are diagnostics, and a later sync of the same
// file makes them durable.
func (s *Store) ArchiveEvents(at time.Time, events []ArchiveEvent) error {
	panic(errNotImplemented)
}

// ArchiveExpire removes the archive files named, which are older than the
// retention.
func (s *Store) ArchiveExpire(names []string) error { panic(errNotImplemented) }

// FileRef names a file of state/.
type FileRef struct {
	Dir  string
	Name string
}

// FloorWrite raises the sequence floors of one sending endpoint.
type FloorWrite struct {
	Sender ident.EndpointID
	Floors map[ident.Address]uint64
}

// SweepChunk is one chunk of the archive sweep.
type SweepChunk struct {
	// Now is the time of the sweep; its UTC date names the archive file.
	Now       time.Time
	Summaries []ArchiveSummary
	// Corrupt, Ctl and Records are removed in that order, each followed by the
	// sync of its directories; Bodies last. A record that depends on a control
	// record (the derived delivery of a session creation) goes in Records and
	// its control record in Ctl.
	Corrupt []FileRef
	Ctl     []FileRef
	Records []FileRef
	Bodies  []BodyRef
	// Floors are written before anything is removed and after the summaries are
	// durable. KeepTarget, when set, prunes the floors of targets that can no
	// longer receive messages.
	Floors     []FloorWrite
	KeepTarget func(ident.Address) bool
}

// ArchiveSweep appends the summaries to the archive, makes them durable,
// raises the sequence floors and removes the records. The order is the
// guarantee: no record is removed before its summary and the floors of its
// sequence numbers are durable.
func (s *Store) ArchiveSweep(c SweepChunk) error { panic(errNotImplemented) }
