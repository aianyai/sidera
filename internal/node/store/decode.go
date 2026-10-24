package store

import "errors"

var errNotImplemented = errors.New("store: not implemented")

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
func EncodeRecord(rec Record) ([]byte, error) { panic(errNotImplemented) }

// DecodeRecord validates the file `name` of directory dir with the contents
// data and returns its record. The steps, in order, and the class of the
// damage each gives:
//
//  1. the first line is at most MaxHeaderLen bytes, ends in a newline and has
//     six fields of single spaces, with the right magic and format version
//     (HeaderUnreadable);
//  2. the kind is one the directory holds (HeaderUnreadable);
//  3. the key is unpadded base64url and the file name is the hash of kind and
//     key (HeaderUnreadable);
//  4. the key parses as a key of the kind (HeaderUnreadable);
//  5. the bytes after the first line are exactly the length (BodyDamaged);
//  6. their CRC32C is the checksum (BodyDamaged);
//  7. the body decodes strictly, is the canonical encoding of what it decodes
//     to, and passes the checks of its kind (BodyDamaged).
func DecodeRecord(dir, name string, data []byte) (Record, *Damage) { panic(errNotImplemented) }
