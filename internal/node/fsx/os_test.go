package fsx_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// chunkReader hands out its chunks, then fails. It stands in for the random
// source and makes temporary names predictable.
type chunkReader struct{ chunks [][]byte }

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}

// countingReader yields distinct 8-byte chunks forever.
type countingReader struct{ n byte }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.n
	}
	r.n++
	return len(p), nil
}

func openTemp(t *testing.T) (fsx.FS, string) {
	t.Helper()
	root := t.TempDir()
	fsys, err := fsx.OpenOS(root, &countingReader{})
	if err != nil {
		t.Fatalf("OpenOS(%s): %v", root, err)
	}
	return fsys, root
}

func TestOSFSConformsToTheFSContract(t *testing.T) {
	fsxtest.RunConformance(t, func(t *testing.T) fsx.FS {
		fsys, _ := openTemp(t)
		return fsys
	})
}

func TestOpenOSNeedsAnExistingDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := fsx.OpenOS(filepath.Join(root, "missing"), &countingReader{}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("OpenOS of a missing directory = %v, want fs.ErrNotExist", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fsx.OpenOS(file, &countingReader{}); err == nil {
		t.Error("OpenOS of a file succeeded")
	}
}

func TestOpenOSNeedsARandomSource(t *testing.T) {
	if _, err := fsx.OpenOS(t.TempDir(), nil); err == nil {
		t.Error("OpenOS accepted a nil random source")
	}
}

func TestOSTemporaryNamesComeFromTheInjectedReader(t *testing.T) {
	root := t.TempDir()
	fsys, err := fsx.OpenOS(root, &chunkReader{chunks: [][]byte{
		{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef},
		{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}, // the same name again: exclusive create collides
		{0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88},
	}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := fsys.CreateTemp(tx, ".", fsx.ModePrivate)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if first.Name() != ".tmp-0123456789abcdef" {
		t.Errorf("first name = %q, want .tmp-0123456789abcdef", first.Name())
	}
	second, err := fsys.CreateTemp(tx, ".", fsx.ModePrivate)
	if err != nil {
		t.Fatalf("CreateTemp after a collision: %v", err)
	}
	defer second.Close()
	if second.Name() != ".tmp-ffeeddccbbaa9988" {
		t.Errorf("name after a collision = %q, want .tmp-ffeeddccbbaa9988", second.Name())
	}
}

func TestOSCreateTempFailsWhenTheRandomSourceFails(t *testing.T) {
	fsys, err := fsx.OpenOS(t.TempDir(), &chunkReader{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := fsys.CreateTemp(tx, ".", fsx.ModePrivate)
	if err == nil {
		f.Close()
		t.Fatal("CreateTemp succeeded without randomness")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error = %v, want it to wrap the reader's error", err)
	}
}

func TestOSCreateTempStopsAfterRepeatedCollisions(t *testing.T) {
	same := bytes.Repeat([]byte{7}, 8)
	chunks := make([][]byte, 20)
	for i := range chunks {
		chunks[i] = same
	}
	fsys, err := fsx.OpenOS(t.TempDir(), &chunkReader{chunks: chunks})
	if err != nil {
		t.Fatal(err)
	}
	first, err := fsys.CreateTemp(tx, ".", fsx.ModePrivate)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if f, err := fsys.CreateTemp(tx, ".", fsx.ModePrivate); err == nil {
		f.Close()
		t.Error("CreateTemp succeeded although every name collided")
	}
}

// A real FS under Faulty: the temporary file really leaves the directory after
// a failed sync, and the target is untouched.
func TestReplaceOnTheRealFileSystemAfterAFailedSync(t *testing.T) {
	real, root := openTemp(t)
	faulty := fsxtest.NewFaulty(t, real)
	if err := fsx.Replace(real, tx, "rec", []byte("old"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	rule := faulty.Fail(fsxtest.Match{Tx: tx.Tx, Step: fsx.StepSync, Hit: 1}, syscall.EIO)

	err := fsx.Replace(faulty, tx, "rec", []byte("new"), fsx.ModePrivate)

	var fe *fsx.Error
	if !errors.As(err, &fe) || fe.Class != fsx.Failed {
		t.Fatalf("error = %v, want a non-fatal failure", err)
	}
	if rule.Hits() != 1 {
		t.Errorf("rule fired %d times, want 1", rule.Hits())
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != "rec" {
		t.Errorf("directory holds %v, want only rec", entries)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "rec")); string(got) != "old" {
		t.Errorf("target = %q, want it unchanged", got)
	}
}

func TestOSDurableOperationsEndToEnd(t *testing.T) {
	fsys, root := openTemp(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(fsx.MakeDir(fsys, tx, "adapters"))
	must(fsx.MakeDir(fsys, tx, "adapters/.stage"))
	must(fsx.Replace(fsys, tx, "adapters/.stage/bin", []byte("exe"), fsx.ModePrivateExec))
	must(fsx.RenameDir(fsys, tx, "adapters/.stage", "adapters/v1"))
	f, err := fsx.OpenAppend(fsys, tx, "adapters/v1/log")
	must(err)
	must(fsx.Append(f, tx, []byte("a\n")))
	must(fsx.Append(f, tx, []byte("b\n")))
	must(fsx.SyncFile(fsys, tx, f))
	must(f.Close())
	must(fsx.RenameFile(fsys, tx, "adapters/v1/log", "adapters/v1/log.1"))
	must(fsx.Replace(fsys, tx, "leftover", []byte("x"), fsx.ModePrivate))
	must(fsx.Remove(fsys, tx, "leftover"))
	must(fsx.SyncDir(fsys, tx, "."))
	must(fsx.Discard(fsys, tx, "adapters/v1"))

	got, err := os.ReadDir(filepath.Join(root, "adapters"))
	must(err)
	if len(got) != 0 {
		t.Errorf("adapters holds %v, want nothing after Discard", got)
	}
	left, err := fsx.Leftovers(fsys, ".")
	must(err)
	if len(left) != 0 {
		t.Errorf("leftovers after a clean run: %v", left)
	}
}

func TestOSReadFileReturnsTheWholeFile(t *testing.T) {
	fsys, _ := openTemp(t)
	data := bytes.Repeat([]byte("0123456789abcdef"), 5000) // spans several 4 KiB blocks
	if err := fsx.Replace(fsys, tx, "big", data, fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	got, err := fsys.ReadFile("big")
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("ReadFile returned %d bytes, %v; want the %d written", len(got), err, len(data))
	}
}

func TestOSDoesNotFollowASymlinkInStat(t *testing.T) {
	fsys, root := openTemp(t)
	if err := os.Mkdir(filepath.Join(root, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	info, err := fsys.Stat("link")
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir {
		t.Error("Stat followed the final symbolic link")
	}
	if strings.Contains(info.Name, "real") {
		t.Errorf("Name = %q, want the link's own name", info.Name)
	}
}
