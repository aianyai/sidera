// Package recfile is the framing of a record file: a first line that names the
// kind, the checksum, the length and the key of the record, then the body.
//
//	sidera-record 1 <kind> <crc32c> <length> <key>\n<body>
//
// It knows nothing of what a kind or a body means. The packages that own
// records (store for state/, keys for keys/) put their own records in it and
// decide what the key is, which directory holds which kind and what the body
// says. A record file is named after its kind and key (FileName), or, for a
// kind of which there is one record, by a name fixed by its owner (a
// singleton, EncodeSingleton).
package recfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"regexp"
	"strconv"
	"strings"
)

const (
	// MaxHeaderLen is the longest first line, newline included.
	MaxHeaderLen = 8 << 10
	// MaxBodyLen is the longest body.
	MaxBodyLen = 256 << 10
)

// DamageClass says what part of a file is unreadable. The distinction decides
// what recovery can do: a record whose first line is readable names its key, so
// a replacement can be built for it; one whose first line is not can only be
// reported by its file name.
type DamageClass uint8

const (
	// HeaderUnreadable: the first line, or what it says about the file, cannot
	// be trusted: the key or the kind may be wrong.
	HeaderUnreadable DamageClass = iota + 1
	// BodyDamaged: the first line is readable and its key matches the file name,
	// but the body is not what was written.
	BodyDamaged
)

// DamageReason is the machine-readable reason a file failed validation.
type DamageReason uint8

const (
	ReasonHeaderNoNewline  DamageReason = iota + 1 // no newline within MaxHeaderLen bytes
	ReasonHeaderFields                             // not exactly six fields of single spaces
	ReasonHeaderMagic                              // the magic or the format version is wrong
	ReasonHeaderKind                               // the kind is not a valid kind name
	ReasonHeaderChecksum                           // the checksum is not 8 lowercase hex digits
	ReasonHeaderLength                             // the length is not a decimal without leading zeros
	ReasonHeaderKey                                // the key is not unpadded base64url
	ReasonKindNotAllowed                           // the directory does not hold this kind
	ReasonNameMismatch                             // the file name is not the hash of kind and key
	ReasonKeyInvalid                               // the key does not parse as a key of the kind
	ReasonLengthMismatch                           // the bytes after the first line are not <length>
	ReasonBodyTooLarge                             // <length> is above MaxBodyLen
	ReasonChecksumMismatch                         // the checksum of the body does not match
	ReasonBodyInvalid                              // the body does not decode strictly or fails its checks
)

// Damage is why a file failed validation.
type Damage struct {
	Class  DamageClass
	Reason DamageReason
	Detail string
	// Kind and Key are the kind and the key bytes of the first line, set when
	// Class is BodyDamaged: the first line was readable and checked against the
	// file name, so they can be trusted.
	Kind string
	Key  []byte
}

var reasonNames = map[DamageReason]string{
	ReasonHeaderNoNewline: "first line has no newline within its limit", ReasonHeaderFields: "first line is not six fields",
	ReasonHeaderMagic: "wrong magic or format version", ReasonHeaderKind: "kind is not a kind name",
	ReasonHeaderChecksum: "checksum is not eight lowercase hex digits", ReasonHeaderLength: "length is not a decimal number",
	ReasonHeaderKey: "key is not unpadded base64url", ReasonKindNotAllowed: "directory does not hold this kind",
	ReasonNameMismatch: "file name is not the hash of kind and key", ReasonKeyInvalid: "key is not a key of the kind",
	ReasonLengthMismatch: "body is not as long as the first line says", ReasonBodyTooLarge: "length is over the limit",
	ReasonChecksumMismatch: "checksum of the body does not match", ReasonBodyInvalid: "body is invalid",
}

func (r DamageReason) String() string {
	if n, ok := reasonNames[r]; ok {
		return n
	}
	return fmt.Sprintf("reason(%d)", uint8(r))
}

func (d *Damage) Error() string {
	if d.Detail == "" {
		return d.Reason.String()
	}
	return d.Reason.String() + ": " + d.Detail
}

var (
	crcTable = crc32.MakeTable(crc32.Castagnoli)
	kindRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	lengthRE = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	crcRE    = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

const magic = "sidera-record"

// Header is the first line of a record file, parsed.
type Header struct {
	Kind   string
	CRC    uint32
	Length int
	Key    []byte
}

// Frame is a decoded file: the kind, the key and the body, all checked against
// each other except the name of the file and the meaning of the key and body.
type Frame struct {
	Kind string
	Key  []byte
	Body []byte
}

// EncodeFrame returns the bytes of a record file. It refuses what no reader
// would accept: a kind that is not a kind name, an empty key, a first line
// over MaxHeaderLen, a body over MaxBodyLen.
func EncodeFrame(kind string, key, body []byte) ([]byte, error) {
	if !kindRE.MatchString(kind) {
		return nil, fmt.Errorf("recfile: %q is not a kind name", kind)
	}
	if len(key) == 0 {
		return nil, errors.New("recfile: a record needs a key")
	}
	if len(body) > MaxBodyLen {
		return nil, fmt.Errorf("recfile: a body of %d bytes is over the limit of %d", len(body), MaxBodyLen)
	}
	line := fmt.Sprintf("%s 1 %s %08x %d %s\n", magic, kind, crc32.Checksum(body, crcTable), len(body), base64.RawURLEncoding.EncodeToString(key))
	if len(line) > MaxHeaderLen {
		return nil, fmt.Errorf("recfile: a first line of %d bytes is over the limit of %d", len(line), MaxHeaderLen)
	}
	out := make([]byte, 0, len(line)+len(body))
	out = append(out, line...)
	return append(out, body...), nil
}

// HeaderDamage is the damage of a first line that cannot be trusted.
func HeaderDamage(r DamageReason, format string, args ...any) *Damage {
	return headerDamage(r, format, args...)
}

func headerDamage(r DamageReason, format string, args ...any) *Damage {
	return &Damage{Class: HeaderUnreadable, Reason: r, Detail: fmt.Sprintf(format, args...)}
}

// ParseHeader checks the syntax of the first line of data and returns it with
// the bytes after it. The detail of a damage names the rule and the number of
// the field, never the contents of the first line: a damaged newline can glue
// the body of a file, a private key for one under keys/, into it.
func ParseHeader(data []byte) (Header, []byte, *Damage) {
	window := data
	if len(window) > MaxHeaderLen {
		window = window[:MaxHeaderLen]
	}
	nl := bytes.IndexByte(window, '\n')
	if nl < 0 {
		return Header{}, nil, headerDamage(ReasonHeaderNoNewline, "no newline in the first %d bytes", len(window))
	}
	fields := strings.Split(string(window[:nl]), " ")
	if len(fields) != 6 {
		return Header{}, nil, headerDamage(ReasonHeaderFields, "%d fields", len(fields))
	}
	for i, f := range fields {
		if f == "" {
			return Header{}, nil, headerDamage(ReasonHeaderFields, "field %d is empty", i+1)
		}
	}
	if fields[0] != magic || fields[1] != "1" {
		return Header{}, nil, headerDamage(ReasonHeaderMagic, "field 1 or 2 is not the magic and the format version")
	}
	if !kindRE.MatchString(fields[2]) {
		return Header{}, nil, headerDamage(ReasonHeaderKind, "field 3 is not a kind name")
	}
	if !crcRE.MatchString(fields[3]) {
		return Header{}, nil, headerDamage(ReasonHeaderChecksum, "field 4 is not a checksum")
	}
	crc, _ := strconv.ParseUint(fields[3], 16, 32)
	if !lengthRE.MatchString(fields[4]) {
		return Header{}, nil, headerDamage(ReasonHeaderLength, "field 5 is not a length")
	}
	length, err := strconv.Atoi(fields[4])
	if err != nil {
		return Header{}, nil, headerDamage(ReasonHeaderLength, "field 5 is not a length")
	}
	key, err := base64.RawURLEncoding.Strict().DecodeString(fields[5])
	if err != nil {
		return Header{}, nil, headerDamage(ReasonHeaderKey, "field 6 is not unpadded base64url")
	}
	// The decoder skips carriage returns and newlines, so what it accepted may
	// not be the text of the key it returned: only the canonical text is a key.
	if len(key) == 0 || base64.RawURLEncoding.EncodeToString(key) != fields[5] {
		return Header{}, nil, headerDamage(ReasonHeaderKey, "field 6 is not in its canonical form")
	}
	return Header{Kind: fields[2], CRC: uint32(crc), Length: length, Key: key}, data[nl+1:], nil
}

// CheckBody is steps 5 and 6: the bytes after the first line are as many as it
// says, and their checksum is the one it says. The damage carries the kind and
// the key of the first line.
func CheckBody(h Header, body []byte) *Damage {
	damage := func(r DamageReason, format string, args ...any) *Damage {
		return &Damage{Class: BodyDamaged, Reason: r, Detail: fmt.Sprintf(format, args...), Kind: h.Kind, Key: h.Key}
	}
	switch {
	case h.Length > MaxBodyLen:
		return damage(ReasonBodyTooLarge, "%d bytes", h.Length)
	case len(body) != h.Length:
		return damage(ReasonLengthMismatch, "%d bytes follow, the first line says %d", len(body), h.Length)
	case crc32.Checksum(body, crcTable) != h.CRC:
		return damage(ReasonChecksumMismatch, "")
	}
	return nil
}

// DecodeFrame checks the first line, the length and the checksum, in that
// order. It does not know what the key or the body mean. A damage of the body
// carries the kind and the key of the first line; the caller checks them
// against the file name before it trusts them.
func DecodeFrame(data []byte) (Frame, *Damage) {
	h, body, dmg := ParseHeader(data)
	if dmg != nil {
		return Frame{}, dmg
	}
	if dmg := CheckBody(h, body); dmg != nil {
		return Frame{}, dmg
	}
	return Frame{Kind: h.Kind, Key: h.Key, Body: body}, nil
}

// FileName is the name of the file of the record with this kind and key:
// the lowercase hex SHA-256 of the kind, a space and the key bytes, then
// ".rec". It is the way the kind and the key of a first line are verified: the
// checksum covers only the body.
func FileName(kind string, key []byte) string {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{' '})
	h.Write(key)
	return hex.EncodeToString(h.Sum(nil)) + ".rec"
}

// ValidKind reports whether s is a kind name.
func ValidKind(s string) bool { return kindRE.MatchString(s) }

// EncodeSingleton returns the bytes of a record of which there is one: its key
// is its kind. The owner decides the file name.
func EncodeSingleton(kind string, body []byte) ([]byte, error) {
	return EncodeFrame(kind, []byte(kind), body)
}

// DecodeSingleton checks the file of a singleton kind and returns its body: the
// first line is well formed, the kind is the one expected and the key is the
// kind's name, and the length and the checksum are right. The file name cannot
// be hashed from kind and key, so the caller reads the one path its kind has.
// Any damage means the file cannot be used; a singleton is never replaced by
// the package that finds it damaged.
func DecodeSingleton(kind string, data []byte) ([]byte, *Damage) {
	h, body, dmg := ParseHeader(data)
	if dmg != nil {
		return nil, dmg
	}
	if h.Kind != kind {
		return nil, HeaderDamage(ReasonKindNotAllowed, "the file holds a record of another kind than the one expected")
	}
	if string(h.Key) != kind {
		return nil, HeaderDamage(ReasonNameMismatch, "the key is not the name of the kind")
	}
	if dmg := CheckBody(h, body); dmg != nil {
		return nil, dmg
	}
	return body, nil
}

// Checksum is the CRC32C (Castagnoli) of b, the checksum of a body. Other
// formats of the node that check their pieces the same way use it too.
func Checksum(b []byte) uint32 { return crc32.Checksum(b, crcTable) }

// ChecksumText is the form a checksum has in a first line and in a line of the
// archive: eight lowercase hex digits.
func ChecksumText(sum uint32) string { return fmt.Sprintf("%08x", sum) }

// ValidChecksumText reports whether s is a checksum in that form.
func ValidChecksumText(s string) bool { return crcRE.MatchString(s) }
