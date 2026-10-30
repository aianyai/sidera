package fsx_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// BenchmarkFilesystemWrites measures one public operation per iteration on a
// real file system. Preparation, result checks, handle closes and cleanup are
// outside the timer; operations without a sync keep that boundary here too.
func BenchmarkFilesystemWrites(b *testing.B) {
	b.Run("Replace", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedFile("file", nil)
			b.StartTimer()
			err := fsx.Replace(f.fs, f.tag, "file", f.data, fsx.ModePrivate)
			b.StopTimer()
			f.require(err)
			f.checkFile("file", f.data)
			f.clean("file")
		}
	})
	b.Run("ReplaceDeferred", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedFile("file", nil)
			b.StartTimer()
			err := fsx.ReplaceDeferred(f.fs, f.tag, "file", f.data, fsx.ModePrivate)
			b.StopTimer()
			f.require(err)
			f.checkFile("file", f.data)
			f.clean("file")
		}
	})
	b.Run("OpenAppend", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedFile("file", f.data)
			b.StartTimer()
			handle, err := fsx.OpenAppend(f.fs, f.tag, "file")
			b.StopTimer()
			f.require(err)
			f.require(handle.Close())
			f.checkFile("file", f.data)
			f.clean("file")
		}
	})
	b.Run("Append", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedFile("file", nil)
			handle, err := fsx.OpenAppend(f.fs, f.tag, "file")
			f.require(err)
			b.StartTimer()
			err = fsx.Append(handle, f.tag, f.data)
			b.StopTimer()
			closeErr := handle.Close()
			f.require(err)
			f.require(closeErr)
			f.checkFile("file", f.data)
			f.clean("file")
		}
	})
	b.Run("SyncFile", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			handle, err := fsx.OpenAppend(f.fs, f.tag, "file")
			f.require(err)
			f.require(fsx.Append(handle, f.tag, f.data))
			b.StartTimer()
			err = fsx.SyncFile(f.fs, f.tag, handle)
			b.StopTimer()
			closeErr := handle.Close()
			f.require(err)
			f.require(closeErr)
			f.checkFile("file", f.data)
			f.clean("file")
		}
	})
	b.Run("RenameFile", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedFile("from", f.data)
			b.StartTimer()
			err := fsx.RenameFile(f.fs, f.tag, "from", "to")
			b.StopTimer()
			f.require(err)
			f.checkAbsent("from")
			f.checkFile("to", f.data)
			f.clean("to")
		}
	})
	b.Run("Remove", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedFile("file", f.data)
			b.StartTimer()
			err := fsx.Remove(f.fs, f.tag, "file")
			b.StopTimer()
			f.require(err)
			f.checkAbsent("file")
		}
	})
	b.Run("SyncDir", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedFile("file", f.data)
			f.require(fsx.Remove(f.fs, f.tag, "file"))
			b.StartTimer()
			err := fsx.SyncDir(f.fs, f.tag, ".")
			b.StopTimer()
			f.require(err)
			f.checkAbsent("file")
		}
	})
	b.Run("MakeDir", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			b.StartTimer()
			err := fsx.MakeDir(f.fs, f.tag, "dir")
			b.StopTimer()
			f.require(err)
			info, err := f.fs.Stat("dir")
			f.require(err)
			if !info.IsDir {
				b.Fatal("created path is not a directory")
			}
			f.clean("dir")
		}
	})
	b.Run("RenameDir", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedTree("from")
			b.StartTimer()
			err := fsx.RenameDir(f.fs, f.tag, "from", "to")
			b.StopTimer()
			f.require(err)
			f.checkAbsent("from")
			f.checkFile("to/file", f.data)
			f.clean("to")
		}
	})
	b.Run("RemoveAll", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedTree("dir")
			// RemoveAll consumes a tree already renamed to a garbage name.
			f.require(fsx.RenameDir(f.fs, f.tag, "dir", ".trash-1"))
			b.StartTimer()
			err := fsx.RemoveAll(f.fs, f.tag, ".trash-1")
			b.StopTimer()
			f.require(err)
			f.checkAbsent(".trash-1")
		}
	})
	b.Run("Discard", func(b *testing.B) {
		f := newFilesystemWriteFixture(b)
		for range b.N {
			f.seedTree("dir")
			b.StartTimer()
			err := fsx.Discard(f.fs, f.tag, "dir")
			b.StopTimer()
			f.require(err)
			f.checkAbsent("dir")
			leftovers, err := fsx.Leftovers(f.fs, ".")
			f.require(err)
			if len(leftovers) != 0 {
				b.Fatalf("discard left debris: %v", leftovers)
			}
		}
	})
}

type filesystemWriteFixture struct {
	b    *testing.B
	fs   fsx.FS
	root string
	tag  fsx.Tag
	data []byte
}

func newFilesystemWriteFixture(b *testing.B) filesystemWriteFixture {
	b.StopTimer()
	f := filesystemWriteFixture{
		b: b, root: b.TempDir(),
		tag:  fsx.Tag{Tx: "benchmark", Role: "write"},
		data: bytes.Repeat([]byte("x"), 4*1024),
	}
	var err error
	f.fs, err = fsx.OpenOS(f.root, rand.Reader)
	f.require(err)
	unlock, err := f.fs.Lock("lock")
	f.require(err)
	b.Cleanup(func() { f.require(unlock()) })
	b.ResetTimer()
	return f
}

func (f filesystemWriteFixture) require(err error) {
	f.b.Helper()
	if err != nil {
		f.b.Fatal(err)
	}
}

func (f filesystemWriteFixture) seedFile(name string, data []byte) {
	f.b.Helper()
	f.require(fsx.Replace(f.fs, f.tag, name, data, fsx.ModePrivate))
}

func (f filesystemWriteFixture) seedTree(name string) {
	f.b.Helper()
	f.require(fsx.MakeDir(f.fs, f.tag, name))
	f.seedFile(name+"/file", f.data)
}

func (f filesystemWriteFixture) checkFile(name string, want []byte) {
	f.b.Helper()
	got, err := f.fs.ReadFile(name)
	f.require(err)
	if !bytes.Equal(got, want) {
		f.b.Fatalf("%s: got %d bytes with unexpected content, want %d bytes", name, len(got), len(want))
	}
}

func (f filesystemWriteFixture) checkAbsent(name string) {
	f.b.Helper()
	exists, err := fsx.Exists(f.fs, name)
	f.require(err)
	if exists {
		f.b.Fatalf("%s still exists", name)
	}
}

func (f filesystemWriteFixture) clean(name string) {
	f.b.Helper()
	f.require(os.RemoveAll(filepath.Join(f.root, name)))
}
