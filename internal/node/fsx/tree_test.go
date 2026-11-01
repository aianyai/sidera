package fsx_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// CheckPrivateTree judges every directory and only the files the caller names:
// the others rely on their directory. The first exposure is returned with its
// path, and a clean tree passes.
func TestCheckPrivateTreeJudgesDirectoriesAndNamedFiles(t *testing.T) {
	secret := func(rel string) bool { return strings.HasPrefix(rel, "tokens/") }
	for _, tc := range []struct {
		name string
		wide string // the path whose permissions are wider than the owner's; "" for none
		want string // the path the refusal names; "" for no refusal
	}{
		{"a clean tree", "", ""},
		{"the root", ".", "."},
		{"a directory", "records", "records"},
		{"a directory below a directory", "tokens/inner", "tokens/inner"},
		{"a file that holds a secret", "tokens/a", "tokens/a"},
		{"a file below a directory that holds a secret", "tokens/inner/b", "tokens/inner/b"},
		{"a file that holds nothing secret", "records/r", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := fsxtest.NewMem()
			for _, dir := range []string{"records", "tokens", "tokens/inner"} {
				mustMkdir(t, mem, dir)
			}
			for _, file := range []string{"records/r", "tokens/a", "tokens/inner/b"} {
				mustWrite(t, mem, file, "x")
			}
			if tc.wide != "" {
				mem.SetOwnerOnly(tc.wide, false)
			}
			err := fsx.CheckPrivateTree(mem, ".", secret)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("CheckPrivateTree = %v, want nil", err)
				}
				return
			}
			var refusal *fsx.NotPrivateError
			if !errors.As(err, &refusal) || refusal.Path != tc.want || refusal.Detail == "" {
				t.Fatalf("CheckPrivateTree = %#v, want a *NotPrivateError for %s with a detail", err, tc.want)
			}
		})
	}
}
