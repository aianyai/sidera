package keys

import (
	"encoding/base64"
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
func FormatRevocationExport(sr SelfRevocation) (text []byte, digest record.Digest) {
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
