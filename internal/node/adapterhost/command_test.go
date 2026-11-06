package adapterhost_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// TestCommandJoinsTheProgramToThePackage: a program of the manifest is a path with
// slashes inside the package, and it names a file of this operating system under
// the package directory.
func TestCommandJoinsTheProgramToThePackage(t *testing.T) {
	dir := t.TempDir()
	program, args := adapterhost.Command(dir, manifest.Entry{Program: "bin/setup", Args: []string{"--json", "x"}})
	if want := filepath.Join(dir, "bin", "setup"); program != want {
		t.Errorf("program = %q, want %q", program, want)
	}
	if want := []string{"--json", "x"}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %q, want %q", args, want)
	}
}
