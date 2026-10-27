package fsx_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

func TestOSLinuxReadFileLimitChecksContentBeyondTheReportedSize(t *testing.T) {
	// procfs reports a regular file with size zero, while its content is longer
	// than the limit. This exercises overflow after fstat without a timing race.
	const target = "/proc/version"
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Size() != 0 {
		t.Fatalf("Stat(%s) = mode %v, size %d; want a regular file of reported size zero", target, info.Mode(), info.Size())
	}
	content, err := os.ReadFile(target)
	if err != nil || len(content) <= 1 {
		t.Fatalf("ReadFile(%s) = %q, %v; want more than one byte", target, content, err)
	}
	fsys, root := openTemp(t)
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	data, err := fsys.ReadFileLimit("link", 1)
	if !errors.Is(err, fsx.ErrTooLarge) || data != nil {
		t.Fatalf("ReadFileLimit(link, 1) = %q, %v; want no data and ErrTooLarge", data, err)
	}
}
