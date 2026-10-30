// Package diskfacts reads the named on-disk facts shared by lifecycle test
// bindings. It neither runs the daemon nor judges a lifecycle outcome.
package diskfacts

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
)

// These files must survive init retries verbatim. The certificate and serial
// may be reissued after an interrupted init and are deliberately excluded.
var initPreservedFiles = [...]string{
	keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SelfRevocationPath, keys.ClockPath,
	"config/node.json", "config/policy.json", "config/presets.json", "config/instances.json",
}

// InitPreservation remembers the first bytes actually observed for each file
// init must preserve. Its zero value is ready to use.
type InitPreservation struct {
	protected map[string][]byte
}

// Format keeps retained key and self-revocation bytes out of diagnostics.
func (p InitPreservation) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte("diskfacts.InitPreservation"))
}

// Remember adds files not already remembered. A missing file supplies no
// baseline; a later call remembers it once it exists.
func (p *InitPreservation) Remember(r fsx.Reader) error {
	if p.protected == nil {
		p.protected = make(map[string][]byte)
	}
	for _, path := range initPreservedFiles {
		if _, ok := p.protected[path]; ok {
			continue
		}
		data, err := r.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		p.protected[path] = bytes.Clone(data)
	}
	return nil
}

// Check requires every remembered file to remain present and byte-identical.
func (p *InitPreservation) Check(r fsx.Reader) error {
	for _, path := range initPreservedFiles {
		want, ok := p.protected[path]
		if !ok {
			continue
		}
		got, err := r.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			return fmt.Errorf("diskfacts: init changed existing %s", path)
		}
	}
	return nil
}
