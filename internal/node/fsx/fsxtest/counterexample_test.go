package fsxtest_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"path"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// The counter-example check: the crash model is only worth trusting if it
// catches the two classic durability bugs. Each broken writer below is written
// out in this file, independently of fsx.Replace, and must be caught by
// CrashClone within the fixed seeds. The correct writer is fsx.Replace itself,
// so the check exercises the sequence that ships.

const (
	seeds        = 32 // the fixed seeds 1..seeds
	filesPerSeed = 4
)

type writer func(fs fsx.FS, tag fsx.Tag, p string, data []byte) error

// withoutSync renames the temporary file into place and syncs the directory,
// but never syncs the file: the entry is durable, the content is not.
func withoutSync(fs fsx.FS, tag fsx.Tag, p string, data []byte) error {
	f, err := fs.CreateTemp(tag, path.Dir(p), fsx.ModePrivate)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fs.Rename(tag, f.Name(), p); err != nil {
		return err
	}
	return fs.SyncDir(tag, path.Dir(p))
}

// withoutSyncDir syncs the file and renames it into place, but never syncs the
// directory: the content is durable, the new name is not.
func withoutSyncDir(fs fsx.FS, tag fsx.Tag, p string, data []byte) error {
	f, err := fs.CreateTemp(tag, path.Dir(p), fsx.ModePrivate)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fs.Rename(tag, f.Name(), p)
}

func correct(fs fsx.FS, tag fsx.Tag, p string, data []byte) error {
	return fsx.Replace(fs, tag, p, data, fsx.ModePrivate)
}

var tag = fsx.Tag{Tx: "check", Role: "file"}

func filePath(i int) string { return fmt.Sprintf("file%d", i) }

// content is distinct per file and per generation, and spans less than one
// 4 KiB block, like every small record.
func content(i int, generation string) []byte {
	return bytes.Repeat([]byte(fmt.Sprintf("%s-%d;", generation, i)), 20+7*i)
}

// scenario writes filesPerSeed unrelated files with w, each acknowledged when
// its write returns. With overwrite, every file first has an old, fully durable
// content that the write replaces.
func scenario(t *testing.T, w writer, overwrite bool) *fsxtest.Mem {
	t.Helper()
	mem := fsxtest.NewMem()
	for i := range filesPerSeed {
		if overwrite {
			if err := correct(mem, tag, filePath(i), content(i, "old")); err != nil {
				t.Fatal(err)
			}
		}
		if err := w(mem, tag, filePath(i), content(i, "new")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	return mem
}

// confirmed counts the files of the clone that hold exactly the acknowledged
// content.
func confirmed(t *testing.T, clone *fsxtest.Mem) int {
	t.Helper()
	n := 0
	for i := range filesPerSeed {
		got, err := clone.ReadFile(filePath(i))
		if err == nil && bytes.Equal(got, content(i, "new")) {
			n++
		}
	}
	return n
}

func seedRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, 0x5ce)) }

// sweep runs the fixed seeds and returns how many seeds lost an acknowledged
// file and how many kept at least one file whose durability the writer never
// secured.
func sweep(t *testing.T, w writer, overwrite bool) (lostSeeds, keptSeeds int) {
	t.Helper()
	for seed := uint64(1); seed <= seeds; seed++ {
		mem := scenario(t, w, overwrite)
		n := confirmed(t, mem.CrashClone(seedRand(seed)))
		if n < filesPerSeed {
			lostSeeds++
		}
		if n > 0 {
			keptSeeds++
		}
	}
	t.Logf("seeds=%d lost=%d kept=%d (overwrite=%v)", seeds, lostSeeds, keptSeeds, overwrite)
	return lostSeeds, keptSeeds
}

func TestCrashCloneCatchesReplaceWithoutSync(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("overwrite=%v", overwrite), func(t *testing.T) {
			lost, kept := sweep(t, withoutSync, overwrite)
			if lost == 0 {
				t.Error("no seed lost the unsynced content: the model cannot catch a missing Sync")
			}
			if kept == 0 {
				t.Error("no seed kept unsynced content: the clone drops everything, so catching is vacuous")
			}
		})
	}
}

func TestCrashCloneCatchesReplaceWithoutSyncDir(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("overwrite=%v", overwrite), func(t *testing.T) {
			lost, kept := sweep(t, withoutSyncDir, overwrite)
			if lost == 0 {
				t.Error("no seed lost the unsynced rename: the model cannot catch a missing SyncDir")
			}
			if kept == 0 {
				t.Error("no seed kept the unsynced rename: the clone drops everything, so catching is vacuous")
			}
		})
	}
}

func TestCrashCloneKeepsEverySyncedReplace(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("overwrite=%v", overwrite), func(t *testing.T) {
			for seed := uint64(1); seed <= seeds; seed++ {
				mem := scenario(t, correct, overwrite)
				if n := confirmed(t, mem.CrashClone(seedRand(seed))); n != filesPerSeed {
					t.Fatalf("seed %d: %d of %d synced files survived the power loss", seed, n, filesPerSeed)
				}
				if n := confirmed(t, mem.Clone()); n != filesPerSeed {
					t.Fatalf("seed %d: %d of %d synced files survived the process crash", seed, n, filesPerSeed)
				}
			}
			if n := confirmed(t, scenario(t, correct, overwrite).CrashCloneWorst()); n != filesPerSeed {
				t.Fatalf("worst mode: %d of %d synced files survived", n, filesPerSeed)
			}
		})
	}
}

// The worst mode drops everything unsynced and draws no random numbers, so
// each broken writer is caught by a single clone.
func TestCrashCloneWorstCatchesEachBrokenWriterInOneTry(t *testing.T) {
	for _, tc := range []struct {
		name string
		w    writer
	}{{"without-sync", withoutSync}, {"without-syncdir", withoutSyncDir}} {
		for _, overwrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/overwrite=%v", tc.name, overwrite), func(t *testing.T) {
				if n := confirmed(t, scenario(t, tc.w, overwrite).CrashCloneWorst()); n == filesPerSeed {
					t.Error("worst mode kept every file of a broken writer")
				}
			})
		}
	}
}
