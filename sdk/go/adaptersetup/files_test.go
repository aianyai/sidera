package adaptersetup_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// A file that is made gets the file mode the caller names, and so do the directories made
// above it: the owner of the place being written (a Hermes profile, whose files are
// 0644) decides them, not this package. A file that is already there keeps its own mode,
// so that a person's 0600 dotfile linked into the place is not widened.
func TestWriteFileUsesTheModesItIsGiven(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "file")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adaptersetup.WriteFile(path, []byte("new"), 0o644, 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(dir, "c", "d", "file")
	if err := adaptersetup.WriteFile(deep, []byte("x"), 0o644, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
	noTemporaries(t, filepath.Dir(path))
	if runtime.GOOS == "windows" {
		return // a mode has no effect there
	}
	for p, want := range map[string]os.FileMode{
		path: 0o600, deep: 0o644, filepath.Dir(deep): 0o755, filepath.Join(dir, "c"): 0o755,
	} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, %v; want %v", p, info.Mode().Perm(), err, want)
		}
	}
}

// A link at the path is followed, as EditJSONFile does: the person's file is written at
// its target and the link stays, instead of being replaced by a regular file that cuts
// it from where the person keeps it.
func TestWriteFileFollowsALink(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "store")
	if err := os.Mkdir(store, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(store, "SKILL.md")
	link := filepath.Join(dir, "SKILL.md")
	symlink(t, filepath.Join("store", "SKILL.md"), link) // dangling, relative

	if err := adaptersetup.WriteFile(link, []byte("one"), 0o644, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := adaptersetup.WriteFile(link, []byte("two"), 0o644, 0o755); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", info, err)
	}
	if got, _ := os.ReadFile(target); string(got) != "two" {
		t.Errorf("the target holds %q, want %q", got, "two")
	}
	noTemporaries(t, store)
	noTemporaries(t, dir)
}

// A directory is never made behind a link: a link to a place whose directory is not
// there is an error and nothing is created.
func TestWriteFileDoesNotMakeADirectoryBehindALink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "file")
	symlink(t, filepath.Join("absent", "file"), link)
	if err := adaptersetup.WriteFile(link, []byte("x"), 0o644, 0o755); err == nil {
		t.Error("WriteFile made a file behind a link whose directory is not there")
	}
	if _, err := os.Stat(filepath.Join(dir, "absent")); err == nil {
		t.Error("a directory was made behind the link")
	}
}

// A write that fails leaves the old content, and no file of its own behind.
func TestWriteFileLeavesNothingBehindWhenItFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "occupied")
	if err := os.MkdirAll(filepath.Join(path, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := adaptersetup.WriteFile(path, []byte("x"), 0o644, 0o755); err == nil {
		t.Fatal("a file was written over a directory")
	}
	noTemporaries(t, dir)
	if _, err := os.Stat(filepath.Join(path, "inside")); err != nil {
		t.Errorf("the directory was changed: %v", err)
	}
}
