package adaptersetup_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

func TestRecordRoundTripAndEdits(t *testing.T) {
	state := t.TempDir()
	if r, err := adaptersetup.ReadRecord(state); r != nil || err != nil {
		t.Fatalf("no record yet: ReadRecord = %v, %v", r, err)
	}
	rec := adaptersetup.Record{AdapterVersion: "0.15.0", SideraProgram: "/bin/sidera"}
	rec.Set(adaptersetup.RecordItem{ID: "a", Kind: "file", Target: "/t/a", Digest: adaptersetup.Digest([]byte("x")), Version: "0.15.0"})
	rec.Set(adaptersetup.RecordItem{ID: "b", Kind: "file", Target: "/t/b"})
	rec.Set(adaptersetup.RecordItem{ID: "a", Kind: "file", Target: "/t/a2"}) // replaces
	rec.Drop("b")
	if err := adaptersetup.WriteRecord(state, rec); err != nil {
		t.Fatal(err)
	}
	got, err := adaptersetup.ReadRecord(state)
	if err != nil || got == nil || got.AdapterVersion != "0.15.0" || got.SideraProgram != "/bin/sidera" || len(got.Items) != 1 || got.Items[0].Target != "/t/a2" {
		t.Fatalf("ReadRecord = %+v, %v", got, err)
	}
	if _, ok := got.Find("b"); ok {
		t.Error("Find found a dropped item")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(adaptersetup.RecordPath(state)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("record mode = %v, %v; want 0600", info.Mode(), err)
		}
	}
	if err := adaptersetup.RemoveRecord(state); err != nil {
		t.Fatal(err)
	}
	if err := adaptersetup.RemoveRecord(state); err != nil {
		t.Errorf("removing a record that is gone: %v", err)
	}
	if r, err := adaptersetup.ReadRecord(state); r != nil || err != nil {
		t.Errorf("after RemoveRecord: %v, %v", r, err)
	}
}

// A state directory that does not exist is never made by setup, and the error says
// what to do.
func TestSetupNeverMakesTheStateDirectory(t *testing.T) {
	state := filepath.Join(t.TempDir(), "adapter-state", "work")
	for name, err := range map[string]error{
		"RequireStateDir": adaptersetup.RequireStateDir(state),
		"WriteRecord":     adaptersetup.WriteRecord(state, adaptersetup.Record{}),
	} {
		if !errors.Is(err, adaptersetup.ErrNoStateDir) || !strings.Contains(err.Error(), "start the daemon") {
			t.Errorf("%s = %v", name, err)
		}
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the state directory was made: %v", err)
	}
	if r, err := adaptersetup.ReadRecord(state); r != nil || err != nil {
		t.Errorf("ReadRecord of a missing directory = %v, %v", r, err)
	}
}

func TestReadRecordRefusesWhatItDoesNotUnderstand(t *testing.T) {
	for name, content := range map[string]string{
		"not JSON":      `{`,
		"a newer shape": `{"version":2,"items":[]}`,
		"no version":    `{"items":[]}`,
	} {
		state := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(adaptersetup.RecordPath(state)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(adaptersetup.RecordPath(state), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if r, err := adaptersetup.ReadRecord(state); err == nil || r != nil {
			t.Errorf("%s: ReadRecord = %v, %v; want an error", name, r, err)
		}
	}
}

func TestWritePrivateFileReplacesWholeAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "file")
	for _, content := range []string{"first", "second, longer than the first"} {
		if err := adaptersetup.WritePrivateFile(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != content {
			t.Errorf("content = %q, want %q", got, content)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("the directory holds %d entries, want only the file", len(entries))
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700, filepath.Join(dir, "a"): 0o700} {
			if info, err := os.Stat(p); err != nil || info.Mode().Perm() != want {
				t.Errorf("%s mode = %v, %v; want %v", p, info.Mode().Perm(), err, want)
			}
		}
	}
}
