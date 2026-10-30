package diskfacts

import (
	"bytes"
	"fmt"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
)

var signingFiles = [...]string{
	keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SelfRevocationPath, keys.CertificatePath, keys.SerialPath,
}

// SigningSnapshot holds private copies of the five signing files. Obtain it
// with ReadSigning before an operation whose signing state must not change.
type SigningSnapshot struct {
	files [len(signingFiles)][]byte
}

// Format keeps key and self-revocation bytes out of diagnostics.
func (s SigningSnapshot) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte("diskfacts.SigningSnapshot"))
}

// ReadSigning reads every signing file without loading keys or starting a node.
// Any missing or unreadable file is an error. The bytes are never exposed.
func ReadSigning(r fsx.Reader) (SigningSnapshot, error) {
	var out SigningSnapshot
	for i, path := range signingFiles {
		data, err := r.ReadFile(path)
		if err != nil {
			return SigningSnapshot{}, err
		}
		out.files[i] = bytes.Clone(data)
	}
	return out, nil
}

// Unchanged compares every signing file with the snapshot. Missing or unreadable
// files remain errors rather than being reported as an ordinary byte change.
func (s SigningSnapshot) Unchanged(r fsx.Reader) (bool, error) {
	after, err := ReadSigning(r)
	if err != nil {
		return false, err
	}
	for i, data := range s.files {
		if !bytes.Equal(data, after.files[i]) {
			return false, nil
		}
	}
	return true, nil
}
