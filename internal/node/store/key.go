package store

import (
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/aianyai/sidera/internal/node/recfile"
	"strings"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
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
func (k OutKey) Kind() Kind {
	if k.Control {
		return KindOutCtl
	}
	return KindOut
}

// Bytes is the key text.
func (k OutKey) Bytes() ([]byte, error) {
	if k.Sender.IsZero() {
		return nil, errors.New("out key without a sending endpoint")
	}
	if err := envelope.ValidMessageID(k.Message); err != nil {
		return nil, err
	}
	if n := len(k.Recipients); n < 1 || n > envelope.MaxRecipients {
		return nil, fmt.Errorf("out key with %d recipients, want 1 to %d", n, envelope.MaxRecipients)
	}
	parts := []string{k.Sender.String(), string(k.Message)}
	for _, to := range k.Recipients {
		if to.IsZero() {
			return nil, errors.New("out key with a recipient that is not an address")
		}
		parts = append(parts, to.String())
	}
	return []byte(strings.Join(parts, " ")), nil
}

// FileName is the name of the record file.
func (k OutKey) FileName() string {
	parts := []string{k.Sender.String(), string(k.Message)}
	for _, to := range k.Recipients {
		parts = append(parts, to.String())
	}
	return recfile.FileName(string(k.Kind()), []byte(strings.Join(parts, " ")))
}

// Bytes is the key text.
func (k TargetKey) Bytes() ([]byte, error) {
	if k.SenderNode.IsZero() || k.Sender.IsZero() || k.Recipient.IsZero() {
		return nil, errors.New("target key with an identifier that is not set")
	}
	if err := envelope.ValidMessageID(k.Message); err != nil {
		return nil, err
	}
	return []byte(k.String()), nil
}

// FileName is the name of the record file of the given kind (KindIn or
// KindCtl).
func (k TargetKey) FileName(kind Kind) string {
	return recfile.FileName(string(kind), []byte(k.String()))
}

// String is the key text, for a log or a reference inside another record.
func (k TargetKey) String() string {
	return strings.Join([]string{k.SenderNode.String(), k.Sender.String(), string(k.Message), k.Recipient.String()}, " ")
}

// ParseTargetKey is the inverse of String.
func ParseTargetKey(s string) (TargetKey, error) {
	v, err := ParseKey(KindIn, []byte(s))
	if err != nil {
		return TargetKey{}, err
	}
	return v.(TargetKey), nil
}

// Dedup is the deduplication key of the request the record is about.
func (k TargetKey) Dedup() delivery.DedupKey {
	return delivery.DedupKey{SenderNode: k.SenderNode, Message: k.Message, Recipient: k.Recipient}
}

// String is the lowercase hex form.
func (id AlertID) String() string { return hex.EncodeToString(id[:]) }

// ParseAlertID parses the form String produces: 32 lowercase hex digits, not
// all zero.
func ParseAlertID(s string) (AlertID, error) {
	var id AlertID
	if err := parseHex(id[:], s); err != nil {
		return AlertID{}, fmt.Errorf("alert id: %w", err)
	}
	if id == (AlertID{}) {
		return AlertID{}, errors.New("alert id: all zero")
	}
	return id, nil
}

// parseHex fills dst from exactly 2*len(dst) lowercase hex digits.
func parseHex(dst []byte, s string) error {
	if len(s) != 2*len(dst) {
		return fmt.Errorf("%d characters, want %d", len(s), 2*len(dst))
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return fmt.Errorf("character %q is not a lowercase hex digit", c)
		}
	}
	_, err := hex.Decode(dst, []byte(s))
	return err
}

// EndpointKeyBytes is the key text of an endpoint, attempts or seq record.
func EndpointKeyBytes(id ident.EndpointID) []byte { return []byte(id.String()) }

// Scopes of a checkpoint.
const (
	CheckpointScopeAttempt = "attempt"
	CheckpointScopeSession = "session"
)

func (k CheckpointKey) check() error {
	switch k.Scope {
	case CheckpointScopeAttempt:
		if err := adapterproto.AttemptID(k.ID).Validate(); err != nil {
			return fmt.Errorf("checkpoint id: %w", err)
		}
	case CheckpointScopeSession:
		if _, err := ident.ParseEndpointID(k.ID); err != nil {
			return fmt.Errorf("checkpoint id: %w", err)
		}
	default:
		return fmt.Errorf("checkpoint scope %q", k.Scope)
	}
	return nil
}

// CheckpointKeyBytes is the key text of a checkpoint.
func CheckpointKeyBytes(k CheckpointKey) ([]byte, error) {
	if err := k.check(); err != nil {
		return nil, err
	}
	return []byte(k.Scope + " " + k.ID), nil
}

// AdminKeyBytes is the key text of an admin result: "local" or the person, and
// the operation.
func AdminKeyBytes(k admin.RequestKey) []byte {
	who := "local"
	if !k.Person.IsZero() {
		who = k.Person.String()
	}
	return []byte(who + " " + hex.EncodeToString(k.ID[:]))
}

// AlertKeyBytes is the key text of an alert.
func AlertKeyBytes(id AlertID) []byte { return []byte(id.String()) }

// ParseKey parses key bytes as a key of the kind: an OutKey, TargetKey,
// ident.EndpointID, CheckpointKey, admin.RequestKey or AlertID. It is step 4 of
// the validation of a record file.
func ParseKey(kind Kind, key []byte) (any, error) {
	fields := strings.Split(string(key), " ")
	for _, f := range fields {
		if f == "" {
			return nil, errors.New("key with an empty field")
		}
	}
	want := func(n int) error {
		if len(fields) != n {
			return fmt.Errorf("key of %d fields, want %d", len(fields), n)
		}
		return nil
	}
	switch kind {
	case KindOut, KindOutCtl:
		if len(fields) < 3 || len(fields) > 2+envelope.MaxRecipients {
			return nil, fmt.Errorf("out key of %d fields", len(fields))
		}
		sender, err := ident.ParseEndpointID(fields[0])
		if err != nil {
			return nil, err
		}
		k := OutKey{Sender: sender, Message: delivery.MessageID(fields[1]), Control: kind == KindOutCtl}
		if err := envelope.ValidMessageID(k.Message); err != nil {
			return nil, err
		}
		for _, f := range fields[2:] {
			to, err := ident.ParseAddress(f)
			if err != nil {
				return nil, err
			}
			k.Recipients = append(k.Recipients, to)
		}
		return k, nil
	case KindIn, KindCtl:
		if err := want(4); err != nil {
			return nil, err
		}
		node, err := ident.ParseNodeID(fields[0])
		if err != nil {
			return nil, err
		}
		sender, err := ident.ParseEndpointID(fields[1])
		if err != nil {
			return nil, err
		}
		recipient, err := ident.ParseEndpointID(fields[3])
		if err != nil {
			return nil, err
		}
		k := TargetKey{SenderNode: node, Sender: sender, Message: delivery.MessageID(fields[2]), Recipient: recipient}
		if err := envelope.ValidMessageID(k.Message); err != nil {
			return nil, err
		}
		return k, nil
	case KindEndpoint, KindAttempts, KindSeq:
		if err := want(1); err != nil {
			return nil, err
		}
		return ident.ParseEndpointID(fields[0])
	case KindCheckpoint:
		if err := want(2); err != nil {
			return nil, err
		}
		k := CheckpointKey{Scope: fields[0], ID: fields[1]}
		if err := k.check(); err != nil {
			return nil, err
		}
		return k, nil
	case KindAdmin:
		if err := want(2); err != nil {
			return nil, err
		}
		var k admin.RequestKey
		if fields[0] != "local" {
			p, err := ident.ParsePersonID(fields[0])
			if err != nil {
				return nil, err
			}
			k.Person = p
		}
		if err := parseHex(k.ID[:], fields[1]); err != nil {
			return nil, fmt.Errorf("operation id: %w", err)
		}
		if k.ID.IsZero() {
			return nil, errors.New("operation id: all zero")
		}
		return k, nil
	case KindAlert:
		if err := want(1); err != nil {
			return nil, err
		}
		return ParseAlertID(fields[0])
	}
	return nil, fmt.Errorf("%q is not a kind of state/", kind)
}
