package keys_test

import (
	crand "crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/txn"
)

// realRig is a process over the real file system in a temporary data
// directory, created the way init creates it.
type realRig struct {
	root string
	fsys fsx.FS
	w    *txn.Writer
}

func newRealRig(t *testing.T) *realRig {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".sidera")
	if err := fsx.MakeRoot(root); err != nil {
		t.Fatal(err)
	}
	fsys, err := fsx.OpenOS(root, crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	w := txn.New(fsys, newTable(t), nil)
	if err := w.Recover(); err != nil {
		t.Fatal(err)
	}
	return &realRig{root: root, fsys: fsys, w: w}
}

// initialize runs init over the real file system with a deterministic random
// source.
func (r *realRig) initialize(t *testing.T) {
	t.Helper()
	plan, err := keys.PlanInit(r.w.Reader(), newSeeded(7), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	tx := r.w.Begin(initDecl)
	defer tx.Close()
	for _, dir := range []string{"keys", "config"} {
		if err := tx.MakeDir("dir", dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := plan.WriteKeys(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Replace("config", "config/stand-in", []byte("config"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteMarker(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
