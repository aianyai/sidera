package store

import (
	"fmt"
	"github.com/aianyai/sidera/internal/node/recfile"
	"slices"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
)

// Record is a decoded record of state/: one of OutRecord, InRecord, CtlRecord,
// EndpointRecord, AttemptsRecord, CheckpointRecord, AdminRecord, AlertRecord
// and SeqRecord, as a value.
type Record interface {
	// Name is the file name of the record.
	Name() string
}

// EncodeRecord returns the bytes of the file of a record. Nothing it returns
// can fail decoding, and a record that cannot be encoded (a field outside its
// domain, a key or body over the limits) is an error here, not later.
func EncodeRecord(rec Record) ([]byte, error) {
	var (
		kind Kind
		key  []byte
		body []byte
		err  error
	)
	switch r := rec.(type) {
	case OutRecord:
		kind = r.Key.Kind()
		if key, err = r.Key.Bytes(); err == nil {
			body, err = encodeOut(r)
		}
	case InRecord:
		kind = KindIn
		if key, err = r.Key.Bytes(); err == nil {
			body, err = encodeIn(r)
		}
	case CtlRecord:
		kind = KindCtl
		if key, err = r.Key.Bytes(); err == nil {
			body, err = encodeCtl(r)
		}
	case EndpointRecord:
		kind, key = KindEndpoint, EndpointKeyBytes(r.ID)
		body, err = encodeEndpoint(r)
	case AttemptsRecord:
		kind, key = KindAttempts, EndpointKeyBytes(r.Endpoint)
		body, err = encodeAttempts(r)
	case CheckpointRecord:
		kind = KindCheckpoint
		if key, err = CheckpointKeyBytes(r.Key); err == nil {
			body, err = encodeCheckpoint(r)
		}
	case AdminRecord:
		kind, key = KindAdmin, AdminKeyBytes(r.Key)
		body, err = encodeAdmin(r)
	case AlertRecord:
		kind, key = KindAlert, AlertKeyBytes(r.ID)
		body, err = encodeAlert(r)
	case SeqRecord:
		kind, key = KindSeq, EndpointKeyBytes(r.Sender)
		body, err = encodeSeq(r)
	default:
		return nil, fmt.Errorf("store: %T is not a record of state/", rec)
	}
	if err != nil {
		return nil, fmt.Errorf("store: %s record: %w", kind, err)
	}
	return recfile.EncodeFrame(string(kind), key, body)
}

// DecodeRecord validates the file `name` of directory dir with the contents
// data and returns its record. The steps, in order, and the class of the
// damage each gives:
//
//  1. the first line is at most MaxHeaderLen bytes, ends in a newline and has
//     six fields of single spaces, with the right magic and format version
//     (HeaderUnreadable);
//  2. the kind is one the directory holds (HeaderUnreadable);
//  3. the key is unpadded base64url and the file name is the hash of kind and
//     key, with the sending endpoint before it for a file of out/
//     (HeaderUnreadable);
//  4. the key parses as a key of the kind (HeaderUnreadable);
//  5. the bytes after the first line are exactly the length (BodyDamaged);
//  6. their CRC32C is the checksum (BodyDamaged);
//  7. the body decodes strictly, is the canonical encoding of what it decodes
//     to, and passes the checks of its kind (BodyDamaged).
func DecodeRecord(dir, name string, data []byte) (Record, *Damage) {
	h, body, key, dmg := readHeader(dir, name, data)
	if dmg != nil {
		return nil, dmg
	}
	if dmg := recfile.CheckBody(h, body); dmg != nil {
		return nil, dmg
	}
	rec, err := decodeBody(Kind(h.Kind), key, body)
	if err != nil {
		return nil, &Damage{Class: BodyDamaged, Reason: ReasonBodyInvalid, Detail: err.Error(), Kind: h.Kind, Key: h.Key}
	}
	return rec, nil
}

// readHeader is steps 1 to 4 of DecodeRecord: what the first line of a file of
// dir that is, or was, named name may be trusted to say. The kind is one the
// directory holds, the file name is the hash of kind and key, and the key
// parses as a key of the kind; only then are the kind and the key those of the
// file, and the key is returned parsed. It is the one place the rule lives.
func readHeader(dir, name string, data []byte) (h recfile.Header, body []byte, key any, dmg *Damage) {
	h, body, dmg = recfile.ParseHeader(data)
	if dmg != nil {
		return h, nil, nil, dmg
	}
	if !slices.Contains(kindsOf(dir), Kind(h.Kind)) {
		return h, nil, nil, headerDamage(ReasonKindNotAllowed, "%s does not hold %q", dir, h.Kind)
	}
	if want := RecordFileName(Kind(h.Kind), h.Key); name != want {
		return h, nil, nil, headerDamage(ReasonNameMismatch, "the name is %q, kind and key hash to %q", name, want)
	}
	key, err := ParseKey(Kind(h.Kind), h.Key)
	if err != nil {
		return h, nil, nil, headerDamage(ReasonKeyInvalid, "%v", err)
	}
	return h, body, key, nil
}

func decodeBody(kind Kind, key any, body []byte) (Record, error) {
	switch kind {
	case KindOut, KindOutCtl:
		return decodeOut(key.(OutKey), body)
	case KindIn:
		return decodeIn(key.(TargetKey), body)
	case KindCtl:
		return decodeCtl(key.(TargetKey), body)
	case KindEndpoint:
		return decodeEndpoint(key.(ident.EndpointID), body)
	case KindAttempts:
		return decodeAttempts(key.(ident.EndpointID), body)
	case KindCheckpoint:
		return decodeCheckpoint(key.(CheckpointKey), body)
	case KindAdmin:
		return decodeAdmin(key.(admin.RequestKey), body)
	case KindAlert:
		return decodeAlert(key.(AlertID), body)
	case KindSeq:
		return decodeSeq(key.(ident.EndpointID), body)
	}
	return nil, fmt.Errorf("no body of kind %q", kind)
}

func headerDamage(r DamageReason, format string, args ...any) *Damage {
	return recfile.HeaderDamage(r, format, args...)
}
