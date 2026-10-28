package fsxtest_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"path"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// The crash model, rule by rule. Every test builds a state with the public FS
// methods, then inspects clones with the public Reader methods; the expected
// outcomes are written out here from the model's rules, never read from Mem.

const manySeeds = 300

func rng(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, 0xfeed)) }

// crashes returns the power-loss clones of mem for seeds 1..manySeeds.
func crashes(mem *fsxtest.Mem) []*fsxtest.Mem {
	out := make([]*fsxtest.Mem, 0, manySeeds)
	for s := uint64(1); s <= manySeeds; s++ {
		out = append(out, mem.CrashClone(rng(s)))
	}
	return out
}

func exists(r fsx.Reader, p string) bool {
	_, err := r.Stat(p)
	return err == nil
}

func readFile(t *testing.T, r fsx.Reader, p string) ([]byte, bool) {
	t.Helper()
	b, err := r.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", p, err)
	}
	return b, true
}

// dump lists every path of the file system with its content ("/" for a
// directory), so two states can be compared.
func dump(t *testing.T, r fsx.Reader) map[string]string {
	t.Helper()
	out := map[string]string{}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := r.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			p := path.Join(dir, e.Name)
			if e.IsDir {
				out[p] = "/"
				walk(p)
				continue
			}
			b, err := r.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			out[p] = string(b)
		}
	}
	walk(".")
	return out
}

// durable writes a file through the full sequence: durable when it returns.
func durable(t *testing.T, fs fsx.FS, p, data string) {
	t.Helper()
	if err := fsx.Replace(fs, tag, p, []byte(data), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
}

func durableDir(t *testing.T, fs fsx.FS, p string) {
	t.Helper()
	if err := fsx.MakeDir(fs, tag, p); err != nil {
		t.Fatal(err)
	}
}

// unsyncedAppend creates or extends a file by appending, with no sync of the
// file or of its directory.
func unsyncedAppend(t *testing.T, fs fsx.FS, p, data string) {
	t.Helper()
	f, err := fs.OpenAppend(tag, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A synced file's content survives every power loss; what follows it is cut.
func TestSyncedContentSurvivesEveryPowerLoss(t *testing.T) {
	mem := fsxtest.NewMem()
	f, err := mem.OpenAppend(tag, "log")
	must(t, err)
	_, err = f.Write([]byte("synced;"))
	must(t, err)
	must(t, f.Sync())
	_, err = f.Write([]byte("unsynced;"))
	must(t, err)
	must(t, mem.SyncDir(tag, ".")) // the name is durable; the content is what is under test

	cut, full := 0, 0
	check := func(clone *fsxtest.Mem) {
		got, ok := readFile(t, clone, "log")
		if !ok {
			t.Fatal("the synced file is gone")
		}
		if !bytes.HasPrefix(got, []byte("synced;")) {
			t.Fatalf("content %q lost the synced prefix", got)
		}
		if len(got) > len("synced;unsynced;") {
			t.Fatalf("content %q is longer than what was written", got)
		}
		if len(got) < len("synced;unsynced;") {
			cut++
		} else {
			full++
		}
	}
	for _, clone := range crashes(mem) {
		check(clone)
	}
	check(mem.CrashCloneWorst())
	if got, _ := readFile(t, mem.CrashCloneWorst(), "log"); string(got) != "synced;" {
		t.Errorf("worst mode kept %q, want exactly the synced prefix", got)
	}
	if cut == 0 || full == 0 {
		t.Errorf("over %d seeds, cut = %d and complete = %d; both outcomes must occur", manySeeds, cut, full)
	}
}

// Syncing a file does not make its directory entry durable.
func TestFileSyncDoesNotMakeItsNameDurable(t *testing.T) {
	mem := fsxtest.NewMem()
	f, err := mem.OpenAppend(tag, "f")
	must(t, err)
	_, err = f.Write([]byte("x"))
	must(t, err)
	must(t, f.Sync())

	if exists(mem.CrashCloneWorst(), "f") {
		t.Error("worst mode kept a name whose directory was never synced")
	}
	present, absent := 0, 0
	for _, c := range crashes(mem) {
		if exists(c, "f") {
			present++
			if got, _ := readFile(t, c, "f"); string(got) != "x" {
				t.Fatalf("a surviving name holds %q, want the synced content", got)
			}
		} else {
			absent++
		}
	}
	if present == 0 || absent == 0 {
		t.Errorf("over %d seeds, present = %d and absent = %d; both outcomes must occur", manySeeds, present, absent)
	}

	must(t, mem.SyncDir(tag, "."))
	for _, c := range append(crashes(mem), mem.CrashCloneWorst()) {
		if got, _ := readFile(t, c, "f"); string(got) != "x" {
			t.Fatalf("after SyncDir a clone holds %q, want x", got)
		}
	}
}

// SyncDir makes the directory's own entries durable and nothing else: not the
// content of the files in it, not the entries of its subdirectories.
func TestSyncDirCoversOnlyItsOwnEntries(t *testing.T) {
	mem := fsxtest.NewMem()
	durableDir(t, mem, "sub")
	unsyncedAppend(t, mem, "top", "unsynced content")
	unsyncedAppend(t, mem, "sub/inner", "x")
	must(t, mem.SyncDir(tag, "."))

	worst := mem.CrashCloneWorst()
	if got, ok := readFile(t, worst, "top"); !ok || len(got) != 0 {
		t.Errorf("top = %q, %v; want the name durable and the unsynced content gone", got, ok)
	}
	if exists(worst, "sub/inner") {
		t.Error("SyncDir of the parent made an entry of the subdirectory durable")
	}
	must(t, mem.SyncDir(tag, "sub"))
	if !exists(mem.CrashCloneWorst(), "sub/inner") {
		t.Error("SyncDir of the subdirectory did not make its entry durable")
	}
}

// Unsynced file data reaches the disk in 4 KiB blocks, each independently real,
// zero or garbage, and the length is anything from the synced length to the
// written length.
func TestUnsyncedDataIsCutAndDamagedBlockByBlock(t *testing.T) {
	const synced, total = 1000, 10000 // pieces after the cut: [1000,4096) [4096,8192) [8192,10000)
	real := make([]byte, total)
	for i := range real {
		real[i] = byte('A' + i%26)
	}
	mem := fsxtest.NewMem()
	f, err := mem.OpenAppend(tag, "f")
	must(t, err)
	_, err = f.Write(real[:synced])
	must(t, err)
	must(t, f.Sync())
	_, err = f.Write(real[synced:])
	must(t, err)
	must(t, mem.SyncDir(tag, "."))

	bounds := [][2]int{{synced, 4096}, {4096, 8192}, {8192, total}}
	var lengths = map[string]int{}
	var pieceKinds = map[string]int{}
	for _, c := range crashes(mem) {
		got, _ := readFile(t, c, "f")
		if len(got) < synced || len(got) > total {
			t.Fatalf("length %d outside [%d, %d]", len(got), synced, total)
		}
		if !bytes.Equal(got[:synced], real[:synced]) {
			t.Fatal("the synced prefix was damaged")
		}
		switch {
		case len(got) == total:
			lengths["full"]++
		case len(got) == synced:
			lengths["synced-only"]++
		default:
			lengths["between"]++
		}
		for _, b := range bounds {
			if b[0] >= len(got) {
				continue
			}
			piece := got[b[0]:min(b[1], len(got))]
			switch {
			case bytes.Equal(piece, real[b[0]:b[0]+len(piece)]):
				pieceKinds["real"]++
			case bytes.Count(piece, []byte{0}) == len(piece):
				pieceKinds["zero"]++
			default:
				pieceKinds["garbage"]++
			}
		}
	}
	t.Logf("lengths %v, pieces %v", lengths, pieceKinds)
	// The synced length itself is the least likely draw; the worst mode checks it exactly.
	for _, k := range []string{"full", "between"} {
		if lengths[k] == 0 {
			t.Errorf("length outcome %q never occurred in %d seeds", k, manySeeds)
		}
	}
	for _, k := range []string{"real", "zero", "garbage"} {
		if pieceKinds[k] == 0 {
			t.Errorf("block outcome %q never occurred in %d seeds", k, manySeeds)
		}
	}
	if got, _ := readFile(t, mem.CrashCloneWorst(), "f"); !bytes.Equal(got, real[:synced]) {
		t.Errorf("worst mode kept %d bytes, want exactly the %d synced", len(got), synced)
	}
}

// Every name's fate is its own: any subset of the unsynced names can survive.
func TestUnsyncedNamesSurviveIndependently(t *testing.T) {
	const names = 8
	mem := fsxtest.NewMem()
	for i := range names {
		f, err := mem.CreateTemp(tag, ".", fsx.ModePrivate)
		must(t, err)
		_, err = f.Write([]byte("x"))
		must(t, err)
		must(t, f.Sync())
		must(t, f.Close())
		must(t, mem.Rename(tag, f.Name(), fmt.Sprintf("n%d", i)))
	}
	survivors := map[int]int{} // how many names survived -> seeds
	perName := make([]int, names)
	for _, c := range crashes(mem) {
		n := 0
		for i := range names {
			if exists(c, fmt.Sprintf("n%d", i)) {
				perName[i]++
				n++
			}
		}
		survivors[n]++
	}
	for i, count := range perName {
		if count == 0 || count == manySeeds {
			t.Errorf("name n%d survived in %d of %d seeds, want a mixture", i, count, manySeeds)
		}
	}
	mixed := 0
	for n, seeds := range survivors {
		if n > 0 && n < names {
			mixed += seeds
		}
	}
	if mixed == 0 {
		t.Error("every seed kept all names or none: names are not decided independently")
	}
	if n := len(dump(t, mem.CrashCloneWorst())); n != 0 {
		t.Errorf("worst mode kept %d unsynced names", n)
	}
}

// A single rename is atomic, so a rename over an existing target leaves either
// the old file under the target name with the source name still present, or the
// new file under the target name with the source name gone. The target never
// holds a third value, and the new file never has two names or none.
func TestRenameOverATargetIsOldWithTheSourceOrNewWithoutIt(t *testing.T) {
	mem := fsxtest.NewMem()
	durable(t, mem, "target", "old")
	f, err := mem.CreateTemp(tag, ".", fsx.ModePrivate)
	must(t, err)
	_, err = f.Write([]byte("new"))
	must(t, err)
	must(t, f.Sync())
	must(t, f.Close())
	tmp := f.Name()
	must(t, mem.SyncDir(tag, ".")) // the temporary name is durable, the rename below is not
	must(t, mem.Rename(tag, tmp, "target"))

	combos := map[string]int{}
	for _, c := range crashes(mem) {
		got, ok := readFile(t, c, "target")
		if !ok || (string(got) != "old" && string(got) != "new") {
			t.Fatalf("target = %q, %v; want the old or the new file", got, ok)
		}
		tmpPresent := exists(c, tmp)
		if tmpPresent {
			if b, _ := readFile(t, c, tmp); string(b) != "new" {
				t.Fatalf("the source name holds %q, want the renamed file", b)
			}
		}
		if (string(got) == "new") == tmpPresent {
			t.Fatalf("target = %q with source present = %v: the rename must have happened (new, source gone) or not (old, source present)", got, tmpPresent)
		}
		combos[fmt.Sprintf("%s/source-present=%v", got, tmpPresent)]++
	}
	t.Logf("combinations %v", combos)
	for _, want := range []string{"old/source-present=true", "new/source-present=false"} {
		if combos[want] == 0 {
			t.Errorf("combination %s never occurred in %d seeds", want, manySeeds)
		}
	}
	if got, _ := readFile(t, mem.CrashCloneWorst(), "target"); string(got) != "old" {
		t.Errorf("worst mode: target = %q, want the old content", got)
	}
}

// A file has one name at a time, like a directory: a rename of a file to a free
// name is moved or not, and both names are never present, nor neither.
func TestFileRenameMovesOrDoesNotButNeverDuplicatesOrLoses(t *testing.T) {
	mem := fsxtest.NewMem()
	durable(t, mem, "rec", "payload")
	// Quarantining a damaged file: the rename is made, its directory sync is not.
	must(t, mem.Rename(tag, "rec", "rec.corrupt"))

	moved, notMoved := 0, 0
	for _, c := range crashes(mem) {
		rec, corrupt := exists(c, "rec"), exists(c, "rec.corrupt")
		if rec == corrupt {
			t.Fatalf("rec present = %v, rec.corrupt present = %v: a renamed file must have exactly one name", rec, corrupt)
		}
		name := "rec"
		if corrupt {
			name = "rec.corrupt"
			moved++
		} else {
			notMoved++
		}
		if got, _ := readFile(t, c, name); string(got) != "payload" {
			t.Fatalf("%s = %q, want the renamed file's content", name, got)
		}
	}
	if moved == 0 || notMoved == 0 {
		t.Errorf("moved = %d, not moved = %d over %d seeds; both outcomes must occur", moved, notMoved, manySeeds)
	}
	if w := mem.CrashCloneWorst(); !exists(w, "rec") || exists(w, "rec.corrupt") {
		t.Error("worst mode: want the file under its old name")
	}
}

// A directory has one name at a time: a rename of a directory is moved or not,
// and both names are never present, nor neither. This holds for a chain of
// renames too.
func TestDirectoryRenameMovesOrDoesNotButNeverDuplicates(t *testing.T) {
	t.Run("single-rename", func(t *testing.T) {
		mem := fsxtest.NewMem()
		durableDir(t, mem, "stage")
		durable(t, mem, "stage/f", "x")
		must(t, mem.RenameDir(tag, "stage", "pkg"))

		moved, notMoved := 0, 0
		for _, c := range crashes(mem) {
			stage, pkg := exists(c, "stage"), exists(c, "pkg")
			if stage == pkg {
				t.Fatalf("stage present = %v, pkg present = %v: a directory must have exactly one name", stage, pkg)
			}
			dir := "stage"
			if pkg {
				dir = "pkg"
				moved++
			} else {
				notMoved++
			}
			if got, _ := readFile(t, c, dir+"/f"); string(got) != "x" {
				t.Fatalf("%s/f = %q, want the staged content", dir, got)
			}
		}
		if moved == 0 || notMoved == 0 {
			t.Errorf("moved = %d, not moved = %d over %d seeds; both outcomes must occur", moved, notMoved, manySeeds)
		}
		if w := mem.CrashCloneWorst(); !exists(w, "stage") || exists(w, "pkg") {
			t.Error("worst mode: want the directory under its old name")
		}
	})
	t.Run("chain-of-renames", func(t *testing.T) {
		mem := fsxtest.NewMem()
		durableDir(t, mem, "a")
		must(t, mem.RenameDir(tag, "a", "b"))
		must(t, mem.RenameDir(tag, "b", "c"))
		outcomes := map[string]int{}
		for _, c := range crashes(mem) {
			var names []string
			for _, n := range []string{"a", "b", "c"} {
				if exists(c, n) {
					names = append(names, n)
				}
			}
			if len(names) != 1 || (names[0] != "a" && names[0] != "c") {
				t.Fatalf("names after a chain of renames = %v, want only a (not moved) or only c (moved)", names)
			}
			outcomes[names[0]]++
		}
		if outcomes["a"] == 0 || outcomes["c"] == 0 {
			t.Errorf("outcomes %v: both must occur", outcomes)
		}
	})
}

// Once a directory is synced, its earlier renames no longer tie names together:
// the names are decided one by one again.
func TestSyncDirEndsTheGroupingOfEarlierDirectoryRenames(t *testing.T) {
	mem := fsxtest.NewMem()
	durableDir(t, mem, "a")
	must(t, mem.RenameDir(tag, "a", "b"))
	must(t, mem.SyncDir(tag, ".")) // the rename is durable and its names are free again
	must(t, mem.RemoveAll(tag, "b"))
	must(t, mem.Mkdir(tag, "a")) // an unrelated, unsynced change to both names

	outcomes := map[string]int{}
	for _, c := range crashes(mem) {
		outcomes[fmt.Sprintf("a=%v,b=%v", exists(c, "a"), exists(c, "b"))]++
	}
	t.Logf("outcomes %v", outcomes)
	for _, mixed := range []string{"a=true,b=true", "a=false,b=false", "a=true,b=false", "a=false,b=true"} {
		if outcomes[mixed] == 0 {
			t.Errorf("outcome %s never occurred in %d seeds: the names are still decided together", mixed, manySeeds)
		}
	}
}

// The new name can be durable while the renamed file's content is not: the
// target then holds a cut or damaged file.
func TestDurableNameOfAnUnsyncedFileHoldsDamagedContent(t *testing.T) {
	mem := fsxtest.NewMem()
	f, err := mem.CreateTemp(tag, ".", fsx.ModePrivate)
	must(t, err)
	data := bytes.Repeat([]byte("record;"), 30)
	_, err = f.Write(data)
	must(t, err)
	must(t, f.Close())
	must(t, mem.Rename(tag, f.Name(), "target"))
	must(t, mem.SyncDir(tag, "."))

	if got, ok := readFile(t, mem.CrashCloneWorst(), "target"); !ok || len(got) != 0 {
		t.Errorf("worst mode: target = %d bytes, %v; want present and empty", len(got), ok)
	}
	damaged, intact := 0, 0
	for _, c := range crashes(mem) {
		got, ok := readFile(t, c, "target")
		if !ok {
			t.Fatal("the durable name is missing")
		}
		if bytes.Equal(got, data) {
			intact++
		} else {
			damaged++
		}
	}
	if damaged == 0 || intact == 0 {
		t.Errorf("damaged = %d, intact = %d over %d seeds; both outcomes must occur", damaged, intact, manySeeds)
	}
}

// A directory created without syncing its parent can vanish, and everything in
// it with it.
func TestUnsyncedMkdirTakesItsContentWithIt(t *testing.T) {
	mem := fsxtest.NewMem()
	must(t, mem.Mkdir(tag, "d"))
	durable(t, mem, "d/f", "x") // durable content and a durable entry inside d, but d itself is not durable
	if exists(mem.CrashCloneWorst(), "d") {
		t.Error("worst mode kept a directory whose parent was never synced")
	}
	present, absent := 0, 0
	for _, c := range crashes(mem) {
		if exists(c, "d") {
			present++
			if got, _ := readFile(t, c, "d/f"); string(got) != "x" {
				t.Fatalf("d/f = %q, want x: what was synced inside survives with the directory", got)
			}
		} else {
			absent++
			if exists(c, "d/f") {
				t.Fatal("d/f is reachable although d is gone")
			}
		}
	}
	if present == 0 || absent == 0 {
		t.Errorf("present = %d, absent = %d over %d seeds; both outcomes must occur", present, absent, manySeeds)
	}
}

// RemoveAll is many unbindings, each with its own fate: a power loss can leave
// any subset of the tree.
func TestRemoveAllCanBeHalfDoneAfterPowerLoss(t *testing.T) {
	const files = 6
	mem := fsxtest.NewMem()
	durableDir(t, mem, "d")
	for i := range files {
		durable(t, mem, fmt.Sprintf("d/f%d", i), "x")
	}
	must(t, mem.RemoveAll(tag, "d"))

	if exists(mem, "d") {
		t.Fatal("RemoveAll left the directory in the live state")
	}
	w := mem.CrashCloneWorst()
	for i := range files {
		if !exists(w, fmt.Sprintf("d/f%d", i)) {
			t.Errorf("worst mode: d/f%d is gone although the removal was never synced", i)
		}
	}
	partial, gone, whole := 0, 0, 0
	for _, c := range crashes(mem) {
		if !exists(c, "d") {
			gone++
			continue
		}
		n := 0
		for i := range files {
			if exists(c, fmt.Sprintf("d/f%d", i)) {
				n++
			}
		}
		if n == files {
			whole++
		} else {
			partial++
		}
	}
	if partial == 0 || gone == 0 || whole == 0 {
		t.Errorf("partial = %d, gone = %d, whole = %d over %d seeds; all three must occur", partial, gone, whole, manySeeds)
	}
}

// A process crash loses nothing that a call completed, and loses the open
// files and the locks.
func TestCloneKeepsEveryCompletedCallAndDropsHandlesAndLocks(t *testing.T) {
	mem := fsxtest.NewMem()
	durable(t, mem, "synced", "s")
	unsyncedAppend(t, mem, "unsynced", "u")
	must(t, mem.Mkdir(tag, "dir"))
	open, err := mem.OpenAppend(tag, "open")
	must(t, err)
	_, err = open.Write([]byte("written before the crash"))
	must(t, err)
	_, err = mem.Lock("daemon.lock")
	must(t, err)

	clone := mem.Clone()

	want := map[string]string{"synced": "s", "unsynced": "u", "dir": "/", "open": "written before the crash", "daemon.lock": ""}
	if got := dump(t, clone); !reflect.DeepEqual(got, want) {
		t.Errorf("clone = %v, want %v", got, want)
	}
	// The handle belongs to the crashed process: writing to it does not reach the clone.
	_, err = open.Write([]byte(" and after"))
	must(t, err)
	if got, _ := readFile(t, clone, "open"); string(got) != "written before the crash" {
		t.Errorf("a write through an old handle reached the clone: %q", got)
	}
	// The lock is gone.
	unlock, err := clone.Lock("daemon.lock")
	if err != nil {
		t.Errorf("Lock on the clone = %v, want the lock to be free", err)
	} else {
		unlock()
	}
	// What was unsynced is still unsynced in the clone.
	if got, ok := readFile(t, clone.CrashCloneWorst(), "unsynced"); ok {
		t.Errorf("the clone's worst power loss kept the unsynced file: %q", got)
	}
}

func TestClonesAreIndependentOfTheOriginalAndOfEachOther(t *testing.T) {
	for name, clone := range map[string]func(*fsxtest.Mem) *fsxtest.Mem{
		"Clone":           func(m *fsxtest.Mem) *fsxtest.Mem { return m.Clone() },
		"CrashClone":      func(m *fsxtest.Mem) *fsxtest.Mem { return m.CrashClone(rng(1)) },
		"CrashCloneWorst": func(m *fsxtest.Mem) *fsxtest.Mem { return m.CrashCloneWorst() },
	} {
		t.Run(name, func(t *testing.T) {
			mem := fsxtest.NewMem()
			durable(t, mem, "f", "before")
			durableDir(t, mem, "d")
			before := dump(t, mem)

			c := clone(mem)
			second := clone(mem)
			durable(t, c, "f", "changed in the clone")
			durable(t, c, "d/new", "x")
			must(t, c.Remove(tag, "f"))

			if got := dump(t, mem); !reflect.DeepEqual(got, before) {
				t.Errorf("the original changed: %v, want %v", got, before)
			}
			if got := dump(t, second); !reflect.DeepEqual(got, before) {
				t.Errorf("a sibling clone changed: %v, want %v", got, before)
			}
			durable(t, mem, "f", "changed in the original")
			if got, _ := readFile(t, second, "f"); string(got) != "before" {
				t.Errorf("a clone saw a later change of the original: %q", got)
			}
		})
	}
}

func TestCrashCloneIsAFunctionOfTheSeed(t *testing.T) {
	mem := fsxtest.NewMem()
	for i := range 10 {
		unsyncedAppend(t, mem, fmt.Sprintf("f%d", i), string(bytes.Repeat([]byte{byte('a' + i)}, 6000)))
	}
	durableDir(t, mem, "d")
	must(t, mem.RenameDir(tag, "d", "e"))
	a, b, other := dump(t, mem.CrashClone(rng(7))), dump(t, mem.CrashClone(rng(7))), dump(t, mem.CrashClone(rng(8)))
	if !reflect.DeepEqual(a, b) {
		t.Error("the same seed gave two different clones")
	}
	if reflect.DeepEqual(a, other) {
		t.Error("two different seeds gave the same clone")
	}
}

// What a power loss leaves is what a restart sees: all of it is durable, so
// losing power again changes nothing.
func TestAPowerLossCloneIsFullyDurable(t *testing.T) {
	mem := fsxtest.NewMem()
	for i := range 6 {
		unsyncedAppend(t, mem, fmt.Sprintf("f%d", i), "some unsynced content")
	}
	must(t, mem.Mkdir(tag, "d"))
	clone := mem.CrashClone(rng(3))
	want := dump(t, clone)
	for s := uint64(1); s <= 20; s++ {
		if got := dump(t, clone.CrashClone(rng(s))); !reflect.DeepEqual(got, want) {
			t.Fatalf("a second power loss changed the state: %v, want %v", got, want)
		}
	}
	if got := dump(t, clone.CrashCloneWorst()); !reflect.DeepEqual(got, want) {
		t.Errorf("a second worst power loss changed the state: %v, want %v", got, want)
	}
}

func TestCrashCloneNeedsARandomSource(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("CrashClone(nil) did not panic")
		}
	}()
	fsxtest.NewMem().CrashClone(nil)
}

func TestTemporaryNamesDoNotCollideAfterAClone(t *testing.T) {
	mem := fsxtest.NewMem()
	f, err := mem.CreateTemp(tag, ".", fsx.ModePrivate)
	must(t, err)
	must(t, f.Close())
	for _, c := range []*fsxtest.Mem{mem.Clone(), mem.CrashClone(rng(1)), mem.CrashCloneWorst()} {
		g, err := c.CreateTemp(tag, ".", fsx.ModePrivate)
		must(t, err)
		if g.Name() == f.Name() {
			t.Errorf("a clone reused the temporary name %s", g.Name())
		}
	}
}

func TestMemReportsWhatTheTestSetsAsStandIns(t *testing.T) {
	mem := fsxtest.NewMem()
	durable(t, mem, "key", "secret")
	if info, _ := mem.Stat("key"); !info.OwnerOnly {
		t.Error("a new file is not owner-only")
	}
	mem.SetOwnerOnly("key", false)
	if info, _ := mem.Stat("key"); info.OwnerOnly {
		t.Error("SetOwnerOnly(false) had no effect")
	}
	mem.SetAvailable(1234)
	if n, err := mem.Available("."); err != nil || n != 1234 {
		t.Errorf("Available = %d, %v; want 1234", n, err)
	}
	if got, _ := mem.Clone().Available("."); got != 1234 {
		t.Errorf("a clone reports %d available, want 1234", got)
	}
	if info, _ := mem.Clone().Stat("key"); info.OwnerOnly {
		t.Error("a clone forgot the permissions")
	}
}

// The result of every append and directory-rename scenario, as a table: what a
// process crash (Clone) leaves and what a power loss (CrashClone) can leave.
func TestAppendAndRenameDirResultTable(t *testing.T) {
	t.Run("append-before-sync-file", func(t *testing.T) {
		mem := fsxtest.NewMem()
		f, err := mem.OpenAppend(tag, "log")
		must(t, err)
		defer f.Close()
		_, err = f.Write([]byte("first;"))
		must(t, err)
		must(t, f.Sync())
		must(t, mem.SyncDir(tag, "."))
		_, err = f.Write([]byte("second;"))
		must(t, err)

		if got, _ := readFile(t, mem.Clone(), "log"); string(got) != "first;second;" {
			t.Errorf("Clone: %q, want every appended byte", got)
		}
		cutSeen := false
		for _, c := range crashes(mem) {
			got, ok := readFile(t, c, "log")
			if !ok || !bytes.HasPrefix(got, []byte("first;")) || len(got) > len("first;second;") {
				t.Fatalf("CrashClone: %q, %v; want the synced content followed by at most the unsynced bytes", got, ok)
			}
			cutSeen = cutSeen || len(got) < len("first;second;")
		}
		if !cutSeen {
			t.Error("CrashClone never cut the unsynced append")
		}
	})
	t.Run("append-to-a-new-file-before-sync-file", func(t *testing.T) {
		mem := fsxtest.NewMem()
		unsyncedAppend(t, mem, "new", "data;")
		if got, _ := readFile(t, mem.Clone(), "new"); string(got) != "data;" {
			t.Errorf("Clone: %q, want the appended bytes", got)
		}
		absent := 0
		for _, c := range crashes(mem) {
			if !exists(c, "new") {
				absent++
			}
		}
		if absent == 0 {
			t.Error("CrashClone never lost a file created in an unsynced directory")
		}
	})
	t.Run("after-the-sync-of-sync-file-before-its-sync-dir", func(t *testing.T) {
		mem := fsxtest.NewMem()
		f, err := mem.OpenAppend(tag, "new")
		must(t, err)
		defer f.Close()
		_, err = f.Write([]byte("data;"))
		must(t, err)
		must(t, f.Sync())

		if got, _ := readFile(t, mem.Clone(), "new"); string(got) != "data;" {
			t.Errorf("Clone: %q", got)
		}
		absent := 0
		for _, c := range crashes(mem) {
			if got, ok := readFile(t, c, "new"); !ok {
				absent++
			} else if string(got) != "data;" {
				t.Fatalf("CrashClone: %q, want the synced content whenever the name survives", got)
			}
		}
		if absent == 0 {
			t.Error("CrashClone never lost the new file's name")
		}
	})
	t.Run("after-sync-file", func(t *testing.T) {
		mem := fsxtest.NewMem()
		f, err := fsx.OpenAppend(mem, tag, "new")
		must(t, err)
		defer f.Close()
		must(t, fsx.Append(f, tag, []byte("data;")))
		must(t, fsx.SyncFile(mem, tag, f))
		for _, c := range append(crashes(mem), mem.Clone(), mem.CrashCloneWorst()) {
			if got, _ := readFile(t, c, "new"); string(got) != "data;" {
				t.Fatalf("a clone holds %q, want data;", got)
			}
		}
	})
	t.Run("rename-dir-before-its-sync-dir", func(t *testing.T) {
		mem := fsxtest.NewMem()
		durableDir(t, mem, "stage")
		durable(t, mem, "stage/f", "x")
		must(t, mem.RenameDir(tag, "stage", "pkg"))

		c := mem.Clone()
		if !exists(c, "pkg") || exists(c, "stage") {
			t.Error("Clone: want the new name present and the old one absent")
		}
		// Power loss: covered in full by the directory rename test; the table row
		// only needs both outcomes and whole content.
		seen := map[string]int{}
		for _, c := range crashes(mem) {
			dir := "stage"
			if exists(c, "pkg") {
				dir = "pkg"
			}
			if got, _ := readFile(t, c, dir+"/f"); string(got) != "x" {
				t.Fatalf("%s/f = %q, want the staged content", dir, got)
			}
			seen[dir]++
		}
		if seen["stage"] == 0 || seen["pkg"] == 0 {
			t.Errorf("outcomes %v: both names must occur", seen)
		}
	})
	t.Run("after-the-sync-dir-of-rename-dir", func(t *testing.T) {
		mem := fsxtest.NewMem()
		durableDir(t, mem, "stage")
		durable(t, mem, "stage/f", "x")
		must(t, fsx.RenameDir(mem, tag, "stage", "pkg"))
		for _, c := range append(crashes(mem), mem.Clone(), mem.CrashCloneWorst()) {
			if exists(c, "stage") {
				t.Fatal("the old name is back")
			}
			if got, _ := readFile(t, c, "pkg/f"); string(got) != "x" {
				t.Fatalf("pkg/f = %q, want x", got)
			}
		}
	})
	t.Run("discard-in-the-middle-of-remove-all", func(t *testing.T) {
		mem := fsxtest.NewMem()
		durableDir(t, mem, "pkg")
		for i := range 4 {
			durable(t, mem, fmt.Sprintf("pkg/f%d", i), "x")
		}
		var atRemoveAll *fsxtest.Mem
		observed := fsx.Observe(mem, new(fsx.Counter), func(observation fsx.Observation) error {
			p := observation.Point
			if p.Step == fsx.StepRemoveAll {
				atRemoveAll = mem.Clone() // the crash point is before the whole step
			}
			return nil
		})
		must(t, fsx.Discard(observed, tag, "pkg"))

		if got := dump(t, atRemoveAll); len(got) != 5 || got[".trash-1"] != "/" || got[".trash-1/f3"] != "x" {
			t.Errorf("process crash before remove-all: %v, want the whole tree under the garbage name", got)
		}
		// Power loss after the whole Discard: the original name is gone for good,
		// and the garbage-named tree may be there with any subset of its files.
		sizes := map[int]int{}
		for _, c := range crashes(mem) {
			if exists(c, "pkg") {
				t.Fatal("the discarded directory is back under its own name")
			}
			n := 0
			for i := range 4 {
				if exists(c, fmt.Sprintf(".trash-1/f%d", i)) {
					n++
				}
			}
			sizes[n]++
		}
		partial := 0
		for n, seeds := range sizes {
			if n > 0 && n < 4 {
				partial += seeds
			}
		}
		if partial == 0 {
			t.Errorf("subset sizes %v: a partial deletion never occurred", sizes)
		}
	})
}
