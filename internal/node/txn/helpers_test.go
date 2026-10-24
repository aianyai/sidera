package txn_test

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// recorder is an Observer that keeps every point it is told about and can run
// a callback on each one, before the step runs.
type recorder struct {
	mu     sync.Mutex
	points []fsx.Point
	kinds  []txn.Kind
	on     func(p fsx.Point, k txn.Kind)
}

func (r *recorder) Reached(p fsx.Point, k txn.Kind) {
	r.mu.Lock()
	r.points = append(r.points, p)
	r.kinds = append(r.kinds, k)
	on := r.on
	r.mu.Unlock()
	if on != nil {
		on(p, k)
	}
}

// names lists the recorded points as text.
func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.points))
	for i, p := range r.points {
		out[i] = p.String()
	}
	return out
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.points)
}

// table builds a table of the internal declarations and the given ones.
func table(t testing.TB, decls ...*txn.Decl) *txn.Table {
	t.Helper()
	tab, err := txn.NewTable(append(txn.Internal(), decls...)...)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	return tab
}

var setupTag = fsx.Tag{Tx: "setup", Role: "f"}

// mkdirs creates directories on a bare Mem, durably, outside any transaction.
func mkdirs(t testing.TB, mem *fsxtest.Mem, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := fsx.MakeDir(mem, setupTag, d); err != nil {
			t.Fatalf("setup MakeDir(%q): %v", d, err)
		}
	}
}

// put writes files on a bare Mem, durably, outside any transaction.
func put(t testing.TB, mem *fsxtest.Mem, files map[string]string) {
	t.Helper()
	for p, content := range files {
		if err := fsx.Replace(mem, setupTag, p, []byte(content), fsx.ModePrivate); err != nil {
			t.Fatalf("setup Replace(%q): %v", p, err)
		}
	}
}

// snapshot reads every file and directory of r, skipping leftovers: a file is
// listed by path with its content, a directory as "path/".
func snapshot(t testing.TB, r fsx.Reader) map[string]string {
	t.Helper()
	got := map[string]string{}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := r.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%q): %v", dir, err)
		}
		for _, e := range entries {
			if fsx.IsLeftover(e.Name) {
				continue
			}
			p := path.Join(dir, e.Name)
			if dir == "." {
				p = e.Name
			}
			if e.IsDir {
				got[p+"/"] = ""
				walk(p)
				continue
			}
			b, err := r.ReadFile(p)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", p, err)
			}
			got[p] = string(b)
		}
	}
	walk(".")
	return got
}

// leftovers lists the debris of r as paths.
func leftovers(t testing.TB, r fsx.Reader) []string {
	t.Helper()
	found, err := fsx.Leftovers(r, ".")
	if err != nil {
		t.Fatalf("Leftovers: %v", err)
	}
	var out []string
	for _, l := range found {
		out = append(out, l.Path)
	}
	sort.Strings(out)
	return out
}

// mustPanic fails the test unless f panics with a message containing want.
func mustPanic(t testing.TB, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Errorf("no panic, want one mentioning %q", want)
			return
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Errorf("panic %v, want one mentioning %q", r, want)
		}
	}()
	f()
}

func mustNotPanic(t testing.TB, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("unexpected panic: %v", r)
		}
	}()
	f()
}

// txnError returns err as a *txn.Error or fails the test.
func txnError(t testing.TB, err error) *txn.Error {
	t.Helper()
	var te *txn.Error
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not a *txn.Error", err)
	}
	return te
}

func absent(r fsx.Reader, p string) bool {
	_, err := r.Stat(p)
	return errors.Is(err, fs.ErrNotExist)
}

var errBoom = errors.New("boom")
