package ledger

import (
	"crypto/sha256"
	"encoding/hex"
)

// Digest returns the SHA-256 of b in lower-case hexadecimal: what the ledger
// records instead of a message body or a prompt text. Every writer and every test
// computes it here, so a test can name an exact text without the ledger holding it.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
