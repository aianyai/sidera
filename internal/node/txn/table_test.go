package txn_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// The composition check of the whole program, run here on the declarations
// that exist without any owner package: the internal ones and the test
// declarations of this package. An owner package's table is checked the same
// way by the composition root once it exists.
func TestTransactionTableIsValid(t *testing.T) {
	decls := append(txn.Internal(), multi, singleReplace, eventsOnly, sweepLike)
	tab, err := txn.NewTable(decls...)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}

	names := map[string]bool{}
	for _, d := range decls {
		if names[d.Name] {
			t.Errorf("transaction name %q is declared twice", d.Name)
		}
		names[d.Name] = true
		if !fsx.ValidName(d.Name) {
			t.Errorf("transaction name %q is not a valid name", d.Name)
		}
		for _, w := range d.Writes {
			if !fsx.ValidName(w.Role) {
				t.Errorf("%s: role %q is not a valid name", d.Name, w.Role)
			}
		}
	}

	seen := map[string]bool{}
	for _, k := range tab.Keys() {
		if err := k.Key.Validate(); err != nil {
			t.Errorf("key %v: %v", k.Key, err)
		}
		if seen[k.Key.String()] {
			t.Errorf("key %v appears twice", k.Key)
		}
		seen[k.Key.String()] = true
		if k.Kind != txn.Normal && k.Kind != txn.Recovery {
			t.Errorf("key %v has kind %d", k.Key, k.Kind)
		}
	}
}

// The cleanup transaction repairs a crash and is of the recovery kind. The
// settle transaction runs at every start, so it is of the normal kind: a clean
// start must not count as a recovery.
func TestInternalDeclarationsHaveTheirKinds(t *testing.T) {
	internal := txn.Internal()
	want := map[string]txn.Kind{"txn-cleanup": txn.Recovery, "txn-settle": txn.Normal}
	got := map[string]txn.Kind{}
	for _, d := range internal {
		got[d.Name] = d.Kind
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("internal declarations and kinds = %v, want %v", got, want)
	}
	if _, err := txn.NewTable(internal...); err != nil {
		t.Errorf("the internal declarations are not a valid table: %v", err)
	}
}

// The keys of a table are expanded from the declarations: every step of every
// operation under its role, and one commit per transaction. The expected list
// is written out by hand for multi, in the order the table promises: by
// transaction, commit last, then role, then step.
func TestTableKeysAreTheStepsOfEveryDeclaredWrite(t *testing.T) {
	tab, err := txn.NewTable(multi)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"multi/body.create", "multi/body.write", "multi/body.sync", "multi/body.rename", "multi/body.sync-dir",
		"multi/in.create", "multi/in.write", "multi/in.sync", "multi/in.rename",
		"multi/in-dir.sync-dir",
		"multi/journal.open", "multi/journal.write", "multi/journal.sync", "multi/journal.sync-dir",
		"multi/out.create", "multi/out.write", "multi/out.sync", "multi/out.rename", "multi/out.sync-dir",
		"multi/commit",
	}
	var got []string
	for _, k := range tab.Keys() {
		if k.Kind != txn.Normal {
			t.Errorf("key %v has kind %d, want Normal", k.Key, k.Kind)
		}
		got = append(got, k.Key.String())
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("keys:\n got %v\nwant %v", got, want)
	}
}

// The steps each operation expands to, one declaration per operation, written
// out by hand from the sequence each composite operation of the file system
// layer runs.
func TestTableKeysOfEveryOperation(t *testing.T) {
	cases := []struct {
		name   string
		writes []txn.Write
		want   []string
	}{
		{"replace", []txn.Write{{Role: "r", Op: txn.OpReplace}},
			[]string{"r.create", "r.write", "r.sync", "r.rename", "r.sync-dir"}},
		{"replace-defer", []txn.Write{{Role: "r", Op: txn.OpReplaceDefer}, {Role: "d", Op: txn.OpSyncDir}},
			[]string{"d.sync-dir", "r.create", "r.write", "r.sync", "r.rename"}},
		{"append-and-sync-file", []txn.Write{{Role: "r", Op: txn.OpAppend}, {Role: "r", Op: txn.OpSyncFile}},
			[]string{"r.open", "r.write", "r.sync", "r.sync-dir"}},
		{"append-alone", []txn.Write{{Role: "r", Op: txn.OpAppend}},
			[]string{"r.open", "r.write"}},
		{"rename-file", []txn.Write{{Role: "r", Op: txn.OpRenameFile}},
			[]string{"r.rename", "r.sync-dir"}},
		{"remove", []txn.Write{{Role: "r", Op: txn.OpRemove}}, []string{"r.remove"}},
		{"sync-dir", []txn.Write{{Role: "r", Op: txn.OpSyncDir}}, []string{"r.sync-dir"}},
		{"make-dir", []txn.Write{{Role: "r", Op: txn.OpMakeDir}}, []string{"r.sync-dir", "r.mkdir"}},
		{"rename-dir", []txn.Write{{Role: "r", Op: txn.OpRenameDir}}, []string{"r.sync-dir", "r.rename-dir"}},
		{"remove-all", []txn.Write{{Role: "r", Op: txn.OpRemoveAll}}, []string{"r.remove-all"}},
		{"discard", []txn.Write{{Role: "r", Op: txn.OpDiscard}}, []string{"r.sync-dir", "r.remove-all", "r.rename-dir"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tab, err := txn.NewTable(&txn.Decl{Name: "x", Kind: txn.Normal, Writes: c.writes})
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, k := range tab.Keys() {
				if k.Step == fsx.StepCommit {
					continue
				}
				got[k.Tag.Role+"."+k.Step.String()] = true
			}
			if len(got) != len(c.want) {
				t.Errorf("keys = %v, want %v", got, c.want)
			}
			for _, w := range c.want {
				if !got[w] {
					t.Errorf("missing key %s in %v", w, got)
				}
			}
		})
	}
}

// What NewTable refuses, one row per rule.
func TestNewTableRejects(t *testing.T) {
	ok := func(name string) *txn.Decl {
		return &txn.Decl{Name: name, Kind: txn.Normal, Writes: []txn.Write{{Role: "r", Op: txn.OpReplace}}}
	}
	withWrites := func(ws ...txn.Write) *txn.Decl {
		return &txn.Decl{Name: "x", Kind: txn.Normal, Writes: ws}
	}
	same := ok("same")
	long := strings.Repeat("a", 33)
	cases := []struct {
		name  string
		decls []*txn.Decl
		want  string // a word the error must contain
	}{
		{"nil-declaration", []*txn.Decl{nil}, "nil"},
		{"duplicate-name", []*txn.Decl{ok("x"), ok("x")}, "twice"},
		{"the-same-declaration-twice", []*txn.Decl{same, same}, "twice"},
		{"empty-name", []*txn.Decl{ok("")}, "name"},
		{"upper-case-name", []*txn.Decl{ok("Intake")}, "name"},
		{"name-starting-with-a-digit", []*txn.Decl{ok("1x")}, "name"},
		{"name-with-underscore", []*txn.Decl{ok("a_b")}, "name"},
		{"name-too-long", []*txn.Decl{ok(long)}, "name"},
		{"reserved-name-log", []*txn.Decl{ok("log")}, "name"},
		{"reserved-name-cleanup", []*txn.Decl{ok("cleanup")}, "name"},
		{"zero-kind", []*txn.Decl{{Name: "x", Writes: ok("x").Writes}}, "kind"},
		{"unknown-kind", []*txn.Decl{{Name: "x", Kind: 9, Writes: ok("x").Writes}}, "kind"},
		{"no-writes", []*txn.Decl{{Name: "x", Kind: txn.Normal}}, "no writes"},
		{"empty-role", []*txn.Decl{withWrites(txn.Write{Role: "", Op: txn.OpReplace})}, "role"},
		{"invalid-role", []*txn.Decl{withWrites(txn.Write{Role: "Body", Op: txn.OpReplace})}, "role"},
		{"reserved-role-log", []*txn.Decl{withWrites(txn.Write{Role: "log", Op: txn.OpReplace})}, "role"},
		{"reserved-role-cleanup", []*txn.Decl{withWrites(txn.Write{Role: "cleanup", Op: txn.OpReplace})}, "role"},
		{"zero-op", []*txn.Decl{withWrites(txn.Write{Role: "r"})}, "operation"},
		{"unknown-op", []*txn.Decl{withWrites(txn.Write{Role: "r", Op: 99})}, "operation"},
		{"duplicate-role", []*txn.Decl{withWrites(
			txn.Write{Role: "r", Op: txn.OpReplace}, txn.Write{Role: "r", Op: txn.OpRemove})}, "role"},

		{"replace-defer-last", []*txn.Decl{withWrites(txn.Write{Role: "r", Op: txn.OpReplaceDefer})}, "SyncDir"},
		{"replace-defer-followed-by-replace", []*txn.Decl{withWrites(
			txn.Write{Role: "r", Op: txn.OpReplaceDefer}, txn.Write{Role: "s", Op: txn.OpReplace})}, "SyncDir"},
		{"replace-defer-followed-by-another-replace-defer", []*txn.Decl{withWrites(
			txn.Write{Role: "r", Op: txn.OpReplaceDefer}, txn.Write{Role: "s", Op: txn.OpReplaceDefer},
			txn.Write{Role: "d", Op: txn.OpSyncDir})}, "SyncDir"},
		{"sync-dir-before-the-replace-defer-it-belongs-to", []*txn.Decl{withWrites(
			txn.Write{Role: "d", Op: txn.OpSyncDir}, txn.Write{Role: "r", Op: txn.OpReplaceDefer})}, "SyncDir"},

		{"sync-file-without-append", []*txn.Decl{withWrites(txn.Write{Role: "r", Op: txn.OpSyncFile})}, "SyncFile"},
		{"sync-file-of-a-replace-role", []*txn.Decl{withWrites(
			txn.Write{Role: "r", Op: txn.OpReplace}, txn.Write{Role: "r", Op: txn.OpSyncFile})}, "SyncFile"},
		{"sync-file-with-another-role-than-its-append", []*txn.Decl{withWrites(
			txn.Write{Role: "journal", Op: txn.OpAppend}, txn.Write{Role: "sync", Op: txn.OpSyncFile})}, "SyncFile"},
		{"sync-file-before-its-append", []*txn.Decl{withWrites(
			txn.Write{Role: "journal", Op: txn.OpSyncFile}, txn.Write{Role: "journal", Op: txn.OpAppend})}, "SyncFile"},
		{"two-sync-files-of-one-append-role", []*txn.Decl{withWrites(
			txn.Write{Role: "journal", Op: txn.OpAppend}, txn.Write{Role: "journal", Op: txn.OpSyncFile},
			txn.Write{Role: "journal", Op: txn.OpSyncFile})}, "SyncFile"},
		{"two-appends-with-one-role", []*txn.Decl{withWrites(
			txn.Write{Role: "journal", Op: txn.OpAppend}, txn.Write{Role: "journal", Op: txn.OpAppend})}, "role"},

		{"prepare-write-after-a-write-that-is-not-prepare", []*txn.Decl{withWrites(
			txn.Write{Role: "r", Op: txn.OpReplace}, txn.Write{Role: "s", Op: txn.OpReplace, Prepare: true})}, "Prepare"},
		{"prepare-write-after-an-optional-write-that-is-not-prepare", []*txn.Decl{withWrites(
			txn.Write{Role: "r", Op: txn.OpReplace, Optional: true}, txn.Write{Role: "s", Op: txn.OpMakeDir, Prepare: true})}, "Prepare"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tab, err := txn.NewTable(c.decls...)
			if err == nil {
				t.Fatalf("NewTable accepted it, keys: %v", tab.Keys())
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// What NewTable accepts at the edge of each rule.
func TestNewTableAccepts(t *testing.T) {
	cases := []struct {
		name   string
		writes []txn.Write
	}{
		{"a-name-of-32-characters", []txn.Write{{Role: strings.Repeat("a", 32), Op: txn.OpReplace}}},
		{"append-then-sync-file-with-the-same-role", []txn.Write{
			{Role: "journal", Op: txn.OpAppend, Many: true, Optional: true}, {Role: "journal", Op: txn.OpSyncFile, Optional: true}}},
		{"replace-defer-then-sync-dir", []txn.Write{
			{Role: "r", Op: txn.OpReplaceDefer, Many: true, Optional: true}, {Role: "d", Op: txn.OpSyncDir, Optional: true}}},
		{"two-deferred-groups", []txn.Write{
			{Role: "a", Op: txn.OpReplaceDefer, Many: true}, {Role: "a-dir", Op: txn.OpSyncDir},
			{Role: "b", Op: txn.OpReplaceDefer, Many: true}, {Role: "b-dir", Op: txn.OpSyncDir}}},
		{"every-flag", []txn.Write{{Role: "r", Op: txn.OpReplace, Many: true, Optional: true, Prepare: true}}},
		{"prepare-writes-before-the-others", []txn.Write{
			{Role: "a", Op: txn.OpReplace, Prepare: true}, {Role: "b", Op: txn.OpMakeDir, Prepare: true},
			{Role: "c", Op: txn.OpReplace}, {Role: "d", Op: txn.OpRemove}}},
		{"two-appends-each-synced", []txn.Write{
			{Role: "a", Op: txn.OpAppend}, {Role: "a", Op: txn.OpSyncFile},
			{Role: "b", Op: txn.OpAppend}, {Role: "b", Op: txn.OpSyncFile}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := txn.NewTable(&txn.Decl{Name: "x", Kind: txn.Normal, Writes: c.writes}); err != nil {
				t.Errorf("NewTable refused a valid declaration: %v", err)
			}
		})
	}
}

// A table does not change when the declaration it was built from is changed
// afterwards: the declaration a Writer enforces is the one that was validated.
func TestTableKeepsTheDeclarationItWasBuiltFrom(t *testing.T) {
	d := &txn.Decl{Name: "x", Kind: txn.Normal, Writes: []txn.Write{{Role: "r", Op: txn.OpReplace}}}
	tab, err := txn.NewTable(append(txn.Internal(), d)...)
	if err != nil {
		t.Fatal(err)
	}
	before := len(tab.Keys())
	d.Writes[0].Op = txn.OpMakeDir
	d.Writes = append(d.Writes, txn.Write{Role: "s", Op: txn.OpRemove})
	if after := len(tab.Keys()); after != before {
		t.Errorf("keys changed from %d to %d after the declaration was edited", before, after)
	}

	mem := fsxtest.NewMem()
	mkdirs(t, mem, "a")
	w := txn.New(mem, tab, nil)
	if err := w.Recover(); err != nil {
		t.Fatal(err)
	}
	tx := w.Begin(d)
	defer tx.Close()
	mustNotPanic(t, func() {
		if err := tx.Replace("r", "a/f", []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
	})
	mustPanic(t, "not declared", func() { tx.Remove("s", "a/f") })
}

// Other declarations used by the tests of this package.
var (
	singleReplace = &txn.Decl{Name: "single", Kind: txn.Normal,
		Writes: []txn.Write{{Role: "rec", Op: txn.OpReplace}}}

	// eventsOnly appends without ever syncing, like a diagnostic batch.
	eventsOnly = &txn.Decl{Name: "events", Kind: txn.Normal,
		Writes: []txn.Write{{Role: "events", Op: txn.OpAppend, Many: true}}}

	// sweepLike makes a digest durable before it deletes what the digest
	// describes.
	sweepLike = &txn.Decl{Name: "sweep", Kind: txn.Normal,
		Writes: []txn.Write{
			{Role: "digest", Op: txn.OpAppend, Many: true, Optional: true},
			{Role: "digest", Op: txn.OpSyncFile, Optional: true},
			{Role: "floor", Op: txn.OpReplace, Optional: true},
			{Role: "old", Op: txn.OpRemove, Many: true},
			{Role: "old-dir", Op: txn.OpSyncDir},
		}}
)
