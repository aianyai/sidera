package store_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// render writes a declaration as the design writes it: role:Operation, then the
// flags M (many), O (optional), P (prepare) in that order.
func render(d *txn.Decl) string {
	ops := map[txn.Op]string{
		txn.OpReplace: "Replace", txn.OpReplaceDefer: "ReplaceDefer", txn.OpAppend: "Append",
		txn.OpSyncFile: "SyncFile", txn.OpRenameFile: "RenameFile", txn.OpRemove: "Remove",
		txn.OpSyncDir: "SyncDir", txn.OpMakeDir: "MakeDir", txn.OpRenameDir: "RenameDir",
		txn.OpRemoveAll: "RemoveAll", txn.OpDiscard: "Discard",
	}
	var parts []string
	for _, w := range d.Writes {
		s := w.Role + ":" + ops[w.Op]
		var flags string
		if w.Many {
			flags += "M"
		}
		if w.Optional {
			flags += "O"
		}
		if w.Prepare {
			flags += "P"
		}
		if flags != "" {
			s += "[" + flags + "]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

const (
	drainTail = "in:ReplaceDefer[MO] in-dir:SyncDir[O] ctl:ReplaceDefer[MO] ctl-dir:SyncDir[O] body:Remove[MO] body-dir:SyncDir[O]"
)

// designTable is the table of the transactions as the design lists them, one
// line each: name, kind (N normal, R recovery), the writes in order. Where the
// declaration differs from the design's line, the comment says why.
var designTable = []struct {
	name   string
	kind   txn.Kind
	writes string
}{
	{"intake", txn.Normal, "body:Replace[OP] out:Replace"},
	{"deliver", txn.Normal, "body:Replace[OP] in:ReplaceDefer[M] in-dir:SyncDir"},
	{"deliver-reply", txn.Normal, "body:Replace[OP] out:Replace[O] in:ReplaceDefer[M] in-dir:SyncDir"},
	{"source-update", txn.Normal, "out:Replace body:Remove[MO] body-dir:SyncDir[O]"},
	{"control-intake", txn.Normal, "body:Replace[OP] endpoint:Replace[OP] ctl:Replace"},
	{"control-result", txn.Normal, "ctl:Replace endpoint:Replace[O] body:Remove[MO] body-dir:SyncDir[O]"},
	{"control-reply", txn.Normal, "out:Replace"},
	{"prompt-begin", txn.Normal, "attempts:Replace"},
	{"prompt-end", txn.Normal, "attempts:Replace in:ReplaceDefer[MO] in-dir:SyncDir[O]"},
	{"blocked", txn.Normal, "attempts:Replace in:ReplaceDefer[MO] in-dir:SyncDir[O]"},
	{"lease", txn.Normal, "in:ReplaceDefer[M] in-dir:SyncDir"},
	{"ack", txn.Normal, "in:ReplaceDefer[M] in-dir:SyncDir body:Remove[MO] body-dir:SyncDir[O]"},
	{"tick", txn.Normal, "out:ReplaceDefer[MO] out-dir:SyncDir[O] in:ReplaceDefer[MO] in-dir:SyncDir[O] ctl:ReplaceDefer[MO] ctl-dir:SyncDir[O] body:Remove[MO] body-dir:SyncDir[O]"},
	{"endpoint-update", txn.Normal, "endpoint:Replace"},
	{"endpoint-close", txn.Normal, "endpoint:Replace result:Replace[O]"},
	{"endpoint-drain", txn.Normal, drainTail},
	{"instance-drain", txn.Normal, "endpoint:ReplaceDefer[M] endpoint-dir:SyncDir " + drainTail},
	{"feedback-form", txn.Normal, "body:Replace[P] notice:Replace"},
	{"feedback-clear", txn.Normal, "out:ReplaceDefer[M] out-dir:SyncDir"},
	{"checkpoint-put", txn.Normal, "checkpoint:Replace"},
	{"alert-raise", txn.Normal, "alert:Replace"},
	{"archive-events", txn.Normal, "archive:Append[M]"},
	{"archive-sweep", txn.Normal, "archive:Append[MO] archive:SyncFile[O] seq-floor:Replace[MO] corrupt:Remove[MO] corrupt-dir:SyncDir[MO] ctl:Remove[MO] ctl-dir:SyncDir[O] record:Remove[MO] body:Remove[MO] dir:SyncDir[MO]"},
	{"archive-expire", txn.Normal, "file:Remove[M] dir:SyncDir"},
	{"alert-ack", txn.Normal, "quarantined:Remove[O] quarantined-dir:SyncDir[O] alert:Replace result:Replace"},
	{"daemon-stop", txn.Normal, "result:Replace"},
	{"quarantine", txn.Recovery, "record:RenameFile seq-floor:Replace[O] replacement:Replace[O] session:Replace[O] alert:Replace"},
	{"quarantine-resume", txn.Recovery, "seq-floor:Replace[O] replacement:Replace[O] session:Replace[O] alert:Replace"},
	// The design lists the body rename and the alert without flags; a body with
	// more references than one chunk holds is quarantined in chunks, the rename
	// in the first and the alert in the last.
	{"quarantine-body", txn.Recovery, "body:RenameFile[O] out:ReplaceDefer[MO] out-dir:SyncDir[O] in:ReplaceDefer[MO] in-dir:SyncDir[O] ctl:ReplaceDefer[MO] ctl-dir:SyncDir[O] alert:Replace[O]"},
	{"orphan-gc", txn.Recovery, "body:Remove[MO] dir:SyncDir[M]"},
	{"orphan-gc-endpoint", txn.Recovery, "endpoint:Remove[MO] dir:SyncDir[M]"},
	{"ctl-effect-replay", txn.Recovery, "endpoint:ReplaceDefer[MO] endpoint-dir:SyncDir[O]"},
	{"endpoint-drain-replay", txn.Recovery, drainTail},
	{"instance-drain-replay", txn.Recovery, "endpoint:ReplaceDefer[M] endpoint-dir:SyncDir " + drainTail},
	{"prompt-replay", txn.Recovery, "in:ReplaceDefer[M] in-dir:SyncDir"},
	{"blocked-replay", txn.Recovery, "in:ReplaceDefer[M] in-dir:SyncDir"},
	{"feedback-replay", txn.Recovery, "out:ReplaceDefer[M] out-dir:SyncDir"},
}

func TestDeclarationsMatchTheDesignTable(t *testing.T) {
	got := map[string]*txn.Decl{}
	for _, d := range store.Transactions() {
		if _, dup := got[d.Name]; dup {
			t.Errorf("%s is declared twice", d.Name)
		}
		got[d.Name] = d
	}
	for _, want := range designTable {
		d, ok := got[want.name]
		if !ok {
			t.Errorf("transaction %s is not declared", want.name)
			continue
		}
		if d.Kind != want.kind {
			t.Errorf("%s: kind %d, want %d", want.name, d.Kind, want.kind)
		}
		if r := render(d); r != want.writes {
			t.Errorf("%s:\n got  %s\n want %s", want.name, r, want.writes)
		}
		delete(got, want.name)
	}
	for name := range got {
		t.Errorf("transaction %s is declared but not in the design table", name)
	}
}

// The transactions of other packages that write files of state/ use the roles
// this package exports, and the program's table with them is valid.
func TestTransactionTableIsValid(t *testing.T) {
	tab := program(t)
	keys := tab.Keys()
	if len(keys) == 0 {
		t.Fatal("no keys")
	}
	for _, d := range store.Transactions() {
		for _, w := range d.Writes {
			switch w.Op {
			case txn.OpReplace, txn.OpReplaceDefer, txn.OpAppend, txn.OpRemove, txn.OpRenameFile, txn.OpSyncDir, txn.OpSyncFile:
			default:
				t.Errorf("%s writes %q with an operation (%d) that state/ never needs", d.Name, w.Role, w.Op)
			}
		}
	}
	if store.RoleEndpoint != "endpoint" || store.RoleResult != "result" {
		t.Errorf("the exported roles are %q and %q: they are part of crash point identifiers and fixed", store.RoleEndpoint, store.RoleResult)
	}
	// Every name is a legal crash point name and none collides with a name of
	// txn itself.
	seen := map[string]bool{}
	for _, d := range txn.Internal() {
		seen[d.Name] = true
	}
	for _, d := range store.Transactions() {
		if seen[d.Name] {
			t.Errorf("%s collides with a transaction of the transaction layer", d.Name)
		}
		seen[d.Name] = true
	}
	_ = fmt.Sprint
}

// The cross-owner write functions write under the roles of the declaration they
// are called in, and refuse nothing that the declaration allows.
func TestCrossOwnerWritesUseTheCallersRoles(t *testing.T) {
	r := newRig(t)
	tx := r.w.Begin(&declXAdmit)
	if err := tx.Replace("ids", "config/ids", []byte("ids"), 0); err != nil {
		t.Fatal(err)
	}
	e := directEndpoint(7, "chat")
	if err := store.PutEndpoint(tx, store.RoleEndpoint, e); err != nil {
		t.Fatalf("PutEndpoint: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	sc := r.scan()
	if len(sc.Endpoints) != 1 || !equalRecords(sc.Endpoints[0], e) {
		t.Errorf("scan found %#v", sc.Endpoints)
	}

	tx = r.w.Begin(&declXClear)
	if err := tx.Replace("clock", "keys/clock", []byte("c"), 0); err != nil {
		t.Fatal(err)
	}
	res := adminResult(1)
	if err := store.PutAdminResult(tx, store.RoleResult, res); err != nil {
		t.Fatalf("PutAdminResult: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	sc = r.scan()
	if len(sc.Admin) != 1 || !equalRecords(sc.Admin[0], res) {
		t.Errorf("scan found %#v", sc.Admin)
	}
}
