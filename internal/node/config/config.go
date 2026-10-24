package config

import (
	"errors"
	"io/fs"
	"sync"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// keptRevocation is a revocation the node keeps, with the record it came from.
type keptRevocation struct {
	rev revocation.Revocation
	rec record.Record
}

// state is everything loaded from config/.
type state struct {
	node        Node
	policy      PolicyFile
	presets     Presets
	instances   Instances
	endpointIDs map[string]EndpointIDs
	backup      *Backup
	revocations []keptRevocation
	adapters    map[string]Adapter
}

// Config is the configuration of the node as loaded, and the one writer of
// config/. Its values are read through the methods, which return copies; its
// writes go through the transactions of this package, and the in-memory
// configuration takes a change over only after the transaction that wrote it
// has committed.
type Config struct {
	w *txn.Writer

	// wmu serialises the writes: the single writer loop does that already, and
	// this makes a second writer wait instead of reading a version that is about
	// to change.
	wmu sync.Mutex
	// mu guards st, which a read takes a copy of.
	mu sync.RWMutex
	st state
}

// Load reads and checks all of config/ and then repairs the adapter directory.
//
// A file that is missing where it must exist, cannot be read, is not a valid
// document of its kind or breaks a rule makes Load return a *FileError that
// names it, and nothing is written. The files are read in a fixed order, so
// with several wrong ones the owner is shown the first: the directories, then
// node.json, policy.json, presets.json, instances.json, the endpoint-id files
// by name, revocation-backup.json, the revocation records by name, and the
// adapters by id. Only when everything is valid does the repair run, which
// removes what an interrupted installation left among the adapters.
func Load(w *txn.Writer) (*Config, error) {
	st, err := readState(w.Reader())
	if err != nil {
		return nil, err
	}
	c := &Config{w: w, st: st}
	if err := c.repairAdapters(); err != nil {
		return nil, err
	}
	return c, nil
}

func missing(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// readFile reads a file of config/ and says in a *FileError what is wrong when
// it cannot.
func readFile(r fsx.Reader, p string, kind FileKind) ([]byte, error) {
	data, err := r.ReadFile(p)
	switch {
	case err == nil:
		return data, nil
	case missing(err):
		return nil, &FileError{Path: p, Kind: kind, Reason: FileMissing, Err: err}
	default:
		return nil, &FileError{Path: p, Kind: kind, Reason: FileUnreadable, Err: err}
	}
}

// listDir lists the entries of a directory of config/ without the leftovers of
// interrupted operations, which the cleanup at start removes.
func listDir(r fsx.Reader, dir string) ([]fsx.Entry, error) {
	entries, err := r.ReadDir(dir)
	if err != nil {
		reason := FileUnreadable
		if missing(err) {
			reason = FileMissing
		}
		return nil, &FileError{Path: dir, Kind: KindDir, Reason: reason, Err: err}
	}
	out := entries[:0:0]
	for _, e := range entries {
		if !fsx.IsLeftover(e.Name) {
			out = append(out, e)
		}
	}
	return out, nil
}

func readState(r fsx.Reader) (state, error) {
	st := state{endpointIDs: map[string]EndpointIDs{}, adapters: map[string]Adapter{}}
	for _, dir := range LayoutDirs() {
		info, err := r.Stat(dir)
		switch {
		case missing(err):
			return st, &FileError{Path: dir, Kind: KindDir, Reason: FileMissing, Err: err}
		case err != nil:
			return st, &FileError{Path: dir, Kind: KindDir, Reason: FileUnreadable, Err: err}
		case !info.IsDir:
			return st, &FileError{Path: dir, Kind: KindDir, Reason: FileInvalid, Err: errors.New("is not a directory")}
		}
	}

	data, err := readFile(r, NodePath, KindNode)
	if err != nil {
		return st, err
	}
	if st.node, err = DecodeNode(data); err != nil {
		return st, err
	}
	if data, err = readFile(r, PolicyPath, KindPolicy); err != nil {
		return st, err
	}
	if st.policy, err = DecodePolicy(data); err != nil {
		return st, err
	}
	if data, err = readFile(r, PresetsPath, KindPresets); err != nil {
		return st, err
	}
	if st.presets, err = DecodePresets(data); err != nil {
		return st, err
	}
	if data, err = readFile(r, InstancesPath, KindInstances); err != nil {
		return st, err
	}
	if st.instances, err = DecodeInstances(data); err != nil {
		return st, err
	}

	if err := readEndpointIDs(r, &st); err != nil {
		return st, err
	}

	switch data, err := r.ReadFile(BackupPath); {
	case err == nil:
		b, err := DecodeBackup(data)
		if err != nil {
			return st, err
		}
		st.backup = &b
	case !missing(err):
		return st, &FileError{Path: BackupPath, Kind: KindBackup, Reason: FileUnreadable, Err: err}
	}

	if err := readRevocations(r, &st); err != nil {
		return st, err
	}
	if err := readAdapters(r, &st); err != nil {
		return st, err
	}
	return st, nil
}

func readEndpointIDs(r fsx.Reader, st *state) error {
	entries, err := listDir(r, EndpointIDsDir)
	if err != nil {
		return err
	}
	owners := map[ident.EndpointID]string{}
	for _, e := range entries {
		p := EndpointIDsDir + "/" + e.Name
		instance, ok := trimSuffix(e.Name, ".json")
		if e.IsDir || !ok || checkName("", instance) != nil {
			return &FileError{Path: p, Kind: KindEndpointIDs, Reason: FileMisnamed,
				Err: errors.New("a file here is <instance>.json, with the instance named as an adapter id is")}
		}
		data, err := readFile(r, p, KindEndpointIDs)
		if err != nil {
			return err
		}
		ids, err := DecodeEndpointIDs(instance, data)
		if err != nil {
			return err
		}
		all := ids.every()
		for _, id := range sortedEndpoints(all) {
			what := all[id]
			if other, dup := owners[id]; dup {
				return &FileError{Path: p, Kind: KindEndpointIDs, Reason: FileInvalid, Field: what,
					Err: errors.New("the id " + id.String() + " is already the id of an endpoint in " + other)}
			}
			owners[id] = p
		}
		st.endpointIDs[instance] = ids
	}
	return nil
}

func trimSuffix(s, suffix string) (string, bool) {
	if len(s) <= len(suffix) || s[len(s)-len(suffix):] != suffix {
		return "", false
	}
	return s[:len(s)-len(suffix)], true
}

func readRevocations(r fsx.Reader, st *state) error {
	entries, err := listDir(r, RevocationsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := RevocationsDir + "/" + e.Name
		if e.IsDir {
			return &FileError{Path: p, Kind: KindRevocation, Reason: FileMisnamed, Err: errors.New("a directory among the revocation records")}
		}
		data, err := readFile(r, p, KindRevocation)
		if err != nil {
			return err
		}
		rev, rec, err := DecodeRevocation(e.Name, data)
		if err != nil {
			return err
		}
		st.revocations = append(st.revocations, keptRevocation{rev: rev, rec: rec})
	}
	return nil
}

// --- reads ---

func (c *Config) read() state {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.st
}

// Node returns node.json.
func (c *Config) Node() Node {
	n := c.read().node
	n.LastOperation = cloneOperation(n.LastOperation)
	return n
}

// Policy returns policy.json.
func (c *Config) Policy() PolicyFile {
	p := c.read().policy
	p.LastOperation = cloneOperation(p.LastOperation)
	return p
}

// Presets returns presets.json.
func (c *Config) Presets() Presets { return clonePresets(c.read().presets) }

// Instances returns instances.json.
func (c *Config) Instances() Instances { return cloneInstances(c.read().instances) }

// EndpointIDs returns the stable endpoint ids of an instance: none for an
// instance that has none.
func (c *Config) EndpointIDs(instance string) EndpointIDs {
	return cloneEndpointIDs(c.read().endpointIDs[instance])
}

// EndpointSettings returns the settings that apply to an endpoint of an
// instance: what the owner gave it, and for what they did not, the node
// default.
func (c *Config) EndpointSettings(instance string, id ident.EndpointID) EndpointSettings {
	st := c.read()
	return effectiveSettings(st.node.Settings.EndpointDefaults, st.instances.Items[instance].Endpoints[id])
}

// Backup returns the owner's confirmation of the offline backup, if there is
// one. Whether it holds for the node's current self-revocation is Backup.Confirms.
func (c *Config) Backup() (Backup, bool) {
	b := c.read().backup
	if b == nil {
		return Backup{}, false
	}
	return *b, true
}

// Revocations returns the verified revocations the node keeps, in the order of
// their file names.
func (c *Config) Revocations() []revocation.Revocation {
	kept := c.read().revocations
	out := make([]revocation.Revocation, len(kept))
	for i, k := range kept {
		out[i] = k.rev
	}
	return out
}

// RevocationSet returns the revocations as the set that the packages which judge
// read. The set is new on each call.
func (c *Config) RevocationSet() *revocation.Set {
	set := revocation.NewSet()
	for _, k := range c.read().revocations {
		if err := set.Add(k.rev); err != nil {
			panic("config: a verified revocation was refused by the set: " + err.Error())
		}
	}
	return set
}

// Adapters returns the installed adapters, by id.
func (c *Config) Adapters() []Adapter {
	st := c.read()
	out := make([]Adapter, 0, len(st.adapters))
	for _, id := range sortedKeys(st.adapters) {
		out = append(out, st.adapters[id])
	}
	return out
}

// LastOperations returns the operation recorded in each versioned file that has
// one: the results the management layer can still answer a repeated request
// from.
func (c *Config) LastOperations() []Operation {
	st := c.read()
	var out []Operation
	for _, op := range []*Operation{st.node.LastOperation, st.policy.LastOperation, st.presets.LastOperation, st.instances.LastOperation} {
		if op != nil {
			out = append(out, *cloneOperation(op))
		}
	}
	return out
}
