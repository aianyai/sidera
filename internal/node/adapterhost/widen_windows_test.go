//go:build windows

package adapterhost

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// widen lets a program other than the owner reach a path, as a sandbox did on a
// state directory: it grants a group no account holds Modify on it.
func widen(t testing.TB, path string) { fsxtest.GrantForeign(t, path, fsxtest.Modify) }
