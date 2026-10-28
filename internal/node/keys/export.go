package keys

import (
	"bytes"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"

	"github.com/aianyai/sidera/internal/record"
)

// The armor of an exported self-revocation: the record bytes in base64 (the
// standard alphabet, with padding) in lines of 64 characters between two marker
// lines, so that the statement can be copied by hand, printed or kept in a
// password manager. The digest that identifies the statement, and that the
// owner confirms the backup with, is the record digest, shown apart from the
// armor.
const (
	exportBegin     = "-----BEGIN SIDERA SELF-REVOCATION-----"
	exportEnd       = "-----END SIDERA SELF-REVOCATION-----"
	exportLineWidth = 64
)

// FormatRevocationExport returns the exported form of the self-revocation, the
// text with its trailing newline, and the record digest. It is the one formatter
// behind both ways of exporting, for a statement from Keys.SelfRevocation or from
// ReadSelfRevocation. The file an export writes is the plain Wire bytes.
//
// It panics on the zero SelfRevocation, which no statement of this package is:
// an export of nothing would read as a backup that succeeded, and only a caller
// that built a statement by hand could reach it.
func FormatRevocationExport(sr SelfRevocation) (text []byte, digest record.Digest) {
	if len(sr.wire) == 0 {
		panic("keys: FormatRevocationExport of the zero SelfRevocation")
	}
	body := base64.StdEncoding.EncodeToString(sr.wire)
	var b strings.Builder
	b.WriteString(exportBegin)
	b.WriteByte('\n')
	for len(body) > 0 {
		n := min(len(body), exportLineWidth)
		b.WriteString(body[:n])
		b.WriteByte('\n')
		body = body[n:]
	}
	b.WriteString(exportEnd)
	b.WriteByte('\n')
	return []byte(b.String()), sr.digest
}

// ParseRevocationExport parses the text produced by FormatRevocationExport,
// with or without its final newline, and verifies that the statement is signed
// by the identity key of the node it names. All other spellings, extra text and
// invalid statements are refused with ErrCorrupt and a zero SelfRevocation.
func ParseRevocationExport(text []byte) (SelfRevocation, error) {
	block, _ := pem.Decode(text)
	if block == nil {
		return SelfRevocation{}, corrupt(SelfRevocationPath, errors.New("invalid self-revocation export"))
	}
	sr, err := verifySelfRevocation(block.Bytes)
	if err != nil {
		return SelfRevocation{}, err
	}
	// Decode can skip leading text and accept other spellings. Comparing the
	// whole input with the formatter's output keeps the format's rules in one
	// place and refuses anything before or after the one canonical block.
	canonical, _ := FormatRevocationExport(sr)
	if !bytes.Equal(bytes.TrimSuffix(text, []byte{'\n'}), bytes.TrimSuffix(canonical, []byte{'\n'})) {
		return SelfRevocation{}, corrupt(SelfRevocationPath, errors.New("non-canonical self-revocation export"))
	}
	return sr, nil
}
