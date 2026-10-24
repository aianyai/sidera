package txn_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// The Reader a Writer hands out reads and can do nothing else: no type
// assertion reaches a write method.
func TestReaderOfAWriterOffersNoWriteMethod(t *testing.T) {
	r := newRig(t, nil)
	put(t, r.mem, map[string]string{"a/x": "content"})
	reader := r.w.Reader()

	got, err := reader.ReadFile("a/x")
	if err != nil || string(got) != "content" {
		t.Errorf("ReadFile = %q, %v", got, err)
	}
	entries, err := reader.ReadDir("a")
	if err != nil || len(entries) != 1 || entries[0].Name != "x" {
		t.Errorf("ReadDir = %v, %v", entries, err)
	}
	if info, err := reader.Stat("a/x"); err != nil || info.Size != 7 {
		t.Errorf("Stat = %+v, %v", info, err)
	}

	var as any = reader
	if _, ok := as.(fsx.FS); ok {
		t.Error("the Reader is an fsx.FS")
	}
	if _, ok := as.(interface {
		Rename(fsx.Tag, string, string) error
	}); ok {
		t.Error("the Reader has Rename")
	}
	if _, ok := as.(interface {
		Remove(fsx.Tag, string) error
	}); ok {
		t.Error("the Reader has Remove")
	}
	if _, ok := as.(interface {
		CreateTemp(fsx.Tag, string, fsx.Mode) (fsx.File, error)
	}); ok {
		t.Error("the Reader has CreateTemp")
	}
	if _, ok := as.(interface {
		Lock(string) (func() error, error)
	}); ok {
		t.Error("the Reader has Lock")
	}
}

// Reads are not crash points: reading through the Reader reaches no point.
func TestReadsReachNoPoint(t *testing.T) {
	r := newRig(t, nil)
	put(t, r.mem, map[string]string{"a/x": "content"})
	reader := r.w.Reader()
	reader.ReadFile("a/x")
	reader.ReadDir("a")
	reader.Stat("a/x")
	reader.Available("a")
	if n := r.rec.count(); n != 0 {
		t.Errorf("reads reached %d points", n)
	}
}
