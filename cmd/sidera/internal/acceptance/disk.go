package acceptance

import (
	"crypto/rand"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

// diskFilesystem opens the fixture disk for read-only binding audits. The full
// interface is needed only to construct the existing store scanner; callers must
// not begin transactions, recover or write through it.
func diskFilesystem(e scenario.Env) (fsx.FS, error) {
	if err := envFrom(e).check(); err != nil {
		return nil, err
	}
	return fsx.OpenOS(envFrom(e).fixture.home, rand.Reader)
}

// Ordinary file inspections retain only the read side.
func diskReader(e scenario.Env) (fsx.Reader, error) {
	return diskFilesystem(e)
}
