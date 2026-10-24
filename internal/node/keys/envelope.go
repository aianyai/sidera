package keys

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"regexp"
	"strconv"
	"strings"
)

// The files of this package that are not wire bytes of a record are written in
// the record envelope of the data directory: one first line, then the body.
//
//	sidera-record 1 <kind> <crc32c> <length> <key>\n<body>
//
// <crc32c> is the CRC32C (Castagnoli) of the body in eight lowercase hex
// digits, <length> the number of bytes of the body in decimal without leading
// zeros, and <key> the base64url (no padding) of the kind name: these files are
// singletons, each with a fixed name, so the key is the kind itself. The body is
// JSON in one canonical form. A file is read only if it is exactly what the
// writer writes.
const (
	envelopeMagic   = "sidera-record"
	envelopeVersion = "1"
	maxFirstLine    = 8 << 10   // including the newline
	maxBody         = 256 << 10 // the limit of the record envelope
)

var (
	castagnoli = crc32.MakeTable(crc32.Castagnoli)
	crcField   = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

// encodeEnvelope returns the file for a body of the given kind.
func encodeEnvelope(kind string, body []byte) []byte {
	head := fmt.Sprintf("%s %s %s %08x %d %s\n", envelopeMagic, envelopeVersion, kind,
		crc32.Checksum(body, castagnoli), len(body), base64.RawURLEncoding.EncodeToString([]byte(kind)))
	return append([]byte(head), body...)
}

var errEnvelope = errors.New("not a record of the expected kind")

// decodeEnvelope returns the body of a file whose first line says it is a
// record of the given kind. It checks the line field by field, in the order of
// the record format: the magic and the version, the kind, the key, the length
// and then the checksum. The error says which check failed and never repeats
// the content.
func decodeEnvelope(kind string, data []byte) ([]byte, error) {
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 || nl+1 > maxFirstLine {
		return nil, fmt.Errorf("%w: no first line", errEnvelope)
	}
	fields := strings.Split(string(data[:nl]), " ")
	if len(fields) != 6 {
		return nil, fmt.Errorf("%w: the first line has %d fields, want 6", errEnvelope, len(fields))
	}
	switch {
	case fields[0] != envelopeMagic:
		return nil, fmt.Errorf("%w: wrong magic", errEnvelope)
	case fields[1] != envelopeVersion:
		return nil, fmt.Errorf("%w: unsupported format version", errEnvelope)
	case fields[2] != kind:
		return nil, fmt.Errorf("%w: the kind is not %s", errEnvelope, kind)
	case fields[5] != base64.RawURLEncoding.EncodeToString([]byte(kind)):
		return nil, fmt.Errorf("%w: the key is not the kind", errEnvelope)
	}
	length, err := strconv.ParseUint(fields[4], 10, 64)
	if err != nil || strconv.FormatUint(length, 10) != fields[4] {
		return nil, fmt.Errorf("%w: the length is not a canonical decimal", errEnvelope)
	}
	body := data[nl+1:]
	if uint64(len(body)) != length || len(body) > maxBody {
		return nil, fmt.Errorf("%w: the body is not the declared length", errEnvelope)
	}
	if !crcField.MatchString(fields[3]) || fields[3] != fmt.Sprintf("%08x", crc32.Checksum(body, castagnoli)) {
		return nil, fmt.Errorf("%w: the checksum does not match", errEnvelope)
	}
	return body, nil
}
