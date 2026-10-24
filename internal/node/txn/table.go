package txn

import (
	"fmt"
	"sort"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// compiled is a declaration as the table validated it. The table keeps its own
// copy of the writes, so editing the Decl afterwards changes nothing.
type compiled struct {
	decl   *Decl // identity: the pointer Begin is given
	name   string
	kind   Kind
	writes []Write
}

// Table is the validated set of every declaration of the program, and so the
// set of every crash point it can reach.
type Table struct {
	byDecl map[*Decl]*compiled
	byName map[string]*compiled
	keys   []Key
	index  map[fsx.Key]int // position in keys
}

// NewTable validates the declarations and expands their keys.
//
// A declaration needs a valid and unique name, a known kind and at least one
// write. A role is a valid name, unique in the declaration except for the role
// of an Append, which one later SyncFile repeats. Every operation is known.
// Every ReplaceDefer is followed immediately by a SyncDir: the files of a
// deferred group are not durable until the directory is synced, and nothing may
// be made durable ahead of them. A Prepare write comes before every write that
// is not Prepare: once the commit point is passed the flag changes nothing, and
// a declaration that sets it there would mislead its reader.
func NewTable(decls ...*Decl) (*Table, error) {
	t := &Table{
		byDecl: make(map[*Decl]*compiled, len(decls)),
		byName: make(map[string]*compiled, len(decls)),
		index:  map[fsx.Key]int{},
	}
	for _, d := range decls {
		c, err := compile(d)
		if err != nil {
			return nil, err
		}
		if _, dup := t.byName[c.name]; dup {
			return nil, fmt.Errorf("txn: transaction %q is declared twice", c.name)
		}
		t.byDecl[d] = c
		t.byName[c.name] = c
	}
	for _, c := range t.byName {
		t.expand(c)
	}
	sort.Slice(t.keys, func(i, j int) bool { return keyLess(t.keys[i].Key, t.keys[j].Key) })
	for i, k := range t.keys {
		t.index[k.Key] = i
	}
	return t, nil
}

func compile(d *Decl) (*compiled, error) {
	if d == nil {
		return nil, fmt.Errorf("txn: nil declaration")
	}
	if !fsx.ValidName(d.Name) {
		return nil, fmt.Errorf("txn: invalid transaction name %q", d.Name)
	}
	if d.Kind != Normal && d.Kind != Recovery {
		return nil, fmt.Errorf("txn: transaction %q: invalid kind %d", d.Name, d.Kind)
	}
	if len(d.Writes) == 0 {
		return nil, fmt.Errorf("txn: transaction %q has no writes", d.Name)
	}
	c := &compiled{decl: d, name: d.Name, kind: d.Kind, writes: append([]Write(nil), d.Writes...)}
	roles := map[string]bool{}
	appended := map[string]bool{} // roles of an Append, so far
	synced := map[string]bool{}   // roles of an Append that a SyncFile took
	committing := false           // a write that is not Prepare was declared
	for i, w := range c.writes {
		if !fsx.ValidName(w.Role) {
			return nil, fmt.Errorf("txn: transaction %q: invalid role name %q", d.Name, w.Role)
		}
		if stepsOf(w.Op) == nil {
			return nil, fmt.Errorf("txn: transaction %q, role %q: unknown operation %d", d.Name, w.Role, w.Op)
		}
		switch w.Op {
		case OpSyncFile:
			// The steps of an appended file carry the role it was opened under,
			// so its sync is declared under that role.
			if !appended[w.Role] {
				return nil, fmt.Errorf("txn: transaction %q: SyncFile %q does not follow an Append with the same role", d.Name, w.Role)
			}
			if synced[w.Role] {
				return nil, fmt.Errorf("txn: transaction %q: more than one SyncFile of the Append role %q", d.Name, w.Role)
			}
			synced[w.Role] = true
		default:
			if roles[w.Role] {
				return nil, fmt.Errorf("txn: transaction %q: role %q is used twice", d.Name, w.Role)
			}
			roles[w.Role] = true
			if w.Op == OpAppend {
				appended[w.Role] = true
			}
		}
		if w.Prepare && committing {
			return nil, fmt.Errorf("txn: transaction %q: Prepare write %q follows a write that is not Prepare: after the commit point a Prepare flag has no effect", d.Name, w.Role)
		}
		committing = committing || !w.Prepare
		if w.Op == OpReplaceDefer && (i+1 == len(c.writes) || c.writes[i+1].Op != OpSyncDir) {
			return nil, fmt.Errorf("txn: transaction %q: ReplaceDefer %q must be followed immediately by a SyncDir", d.Name, w.Role)
		}
	}
	return c, nil
}

// expand adds the keys of a declaration: every step of every write under its
// role, and the commit.
func (t *Table) expand(c *compiled) {
	seen := map[fsx.Key]bool{}
	add := func(k fsx.Key) {
		if !seen[k] {
			seen[k] = true
			t.keys = append(t.keys, Key{Key: k, Kind: c.kind})
		}
	}
	for _, w := range c.writes {
		for _, step := range stepsOf(w.Op) {
			add(fsx.Key{Tag: fsx.Tag{Tx: c.name, Role: w.Role}, Step: step})
		}
	}
	add(fsx.Key{Tag: fsx.Tag{Tx: c.name}, Step: fsx.StepCommit})
}

// keyLess orders keys by transaction, then the commit after the writes, then
// role, then step.
func keyLess(a, b fsx.Key) bool {
	if a.Tag.Tx != b.Tag.Tx {
		return a.Tag.Tx < b.Tag.Tx
	}
	if ac, bc := a.Step == fsx.StepCommit, b.Step == fsx.StepCommit; ac != bc {
		return bc
	}
	if a.Tag.Role != b.Tag.Role {
		return a.Tag.Role < b.Tag.Role
	}
	return a.Step < b.Step
}

// Keys is every crash point of the program without its ordinal: for each
// declaration, every step of every write under its role, and its commit. The
// order is by transaction, role and step, with the commit last, so that two
// calls and two processes agree.
func (t *Table) Keys() []Key { return append([]Key(nil), t.keys...) }
