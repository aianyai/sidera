package store

import (
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
)

// The key of a record is its identity, in the first line of its file. The key
// bytes are the canonical text of the fields of the key, joined by single
// spaces; no field contains a space. Each field's text comes from the package
// that owns the type (ident, admin), so no text is made up here.

// OutKey is the key of a source record: the sending endpoint, the message and
// its recipients in the order of the envelope. The derived endpoint of a
// session creation is not part of it: it is only known after the target has
// answered, and the file name is fixed when the message is accepted.
type OutKey struct {
	Sender     ident.EndpointID
	Message    delivery.MessageID
	Recipients []ident.Address
	// Control marks a control signal. It is the kind of the file, not a field of
	// the key text, so a message and a control signal with the same text are
	// different records.
	Control bool
}

// TargetKey is the key of a target record, of kind in or ctl: the node and the
// endpoint that sent the message, the message and the receiving endpoint.
type TargetKey struct {
	SenderNode ident.NodeID
	Sender     ident.EndpointID
	Message    delivery.MessageID
	Recipient  ident.EndpointID
}

// CheckpointKey is the key of an adapter checkpoint.
type CheckpointKey struct {
	Scope string // "attempt" or "session"
	ID    string
}

// AlertID identifies an owner alert. It is random, 16 bytes, and is also the
// part of a quarantined file's name that ties it to its alert.
type AlertID [16]byte

// Kind is the kind of the file of an out key.
func (k OutKey) Kind() Kind { panic(errNotImplemented) }

// Bytes is the key text.
func (k OutKey) Bytes() ([]byte, error) { panic(errNotImplemented) }

// FileName is the name of the record file.
func (k OutKey) FileName() string { panic(errNotImplemented) }

// Bytes is the key text.
func (k TargetKey) Bytes() ([]byte, error) { panic(errNotImplemented) }

// FileName is the name of the record file of the given kind (KindIn or
// KindCtl).
func (k TargetKey) FileName(kind Kind) string { panic(errNotImplemented) }

// String is the key text, for a log or a reference inside another record.
func (k TargetKey) String() string { panic(errNotImplemented) }

// ParseTargetKey is the inverse of String.
func ParseTargetKey(s string) (TargetKey, error) { panic(errNotImplemented) }

// Dedup is the deduplication key of the request the record is about.
func (k TargetKey) Dedup() delivery.DedupKey { panic(errNotImplemented) }

// String is the lowercase hex form.
func (id AlertID) String() string { panic(errNotImplemented) }

// ParseAlertID parses the form String produces: 32 lowercase hex digits, not
// all zero.
func ParseAlertID(s string) (AlertID, error) { panic(errNotImplemented) }

// EndpointKeyBytes is the key text of an endpoint, attempts or seq record.
func EndpointKeyBytes(id ident.EndpointID) []byte { panic(errNotImplemented) }

// CheckpointKeyBytes is the key text of a checkpoint.
func CheckpointKeyBytes(k CheckpointKey) ([]byte, error) { panic(errNotImplemented) }

// AdminKeyBytes is the key text of an admin result: "local" or the person, and
// the operation.
func AdminKeyBytes(k admin.RequestKey) []byte { panic(errNotImplemented) }

// AlertKeyBytes is the key text of an alert.
func AlertKeyBytes(id AlertID) []byte { panic(errNotImplemented) }

// ParseKey parses key bytes as a key of the kind: an OutKey, TargetKey,
// ident.EndpointID, CheckpointKey, admin.RequestKey or AlertID. It is step 4 of
// the validation of a record file.
func ParseKey(kind Kind, key []byte) (any, error) { panic(errNotImplemented) }
