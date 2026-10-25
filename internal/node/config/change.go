package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// checkVersion is where every versioned change begins. The version a request
// names is compared with the current one by admin.CheckVersion, and any
// difference is a conflict. It is checked here, inside the change, because the
// check and the write have to be one step: between a check made by someone else
// and the write another change could slip in.
func checkVersion(req Request, current admin.Version) error {
	if admin.CheckVersion(req.Expected, current).Apply {
		return nil
	}
	return &ChangeError{Reason: ChangeVersionConflict, Current: current}
}

// invalidChange turns the error of a value that is refused into the refusal of a
// change that carried it.
func invalidChange(err error) error {
	var fe *FieldError
	if errors.As(err, &fe) {
		return &ChangeError{Reason: ChangeInvalid, Field: fe.Field, Err: fe.Err}
	}
	return &ChangeError{Reason: ChangeInvalid, Err: err}
}

// nextVersion is the version a change produces. The largest version has no
// successor: it would wrap to 0, which no versioned file may hold.
func nextVersion(current admin.Version) (admin.Version, error) {
	if current == admin.Version(math.MaxUint64) {
		return 0, &ChangeError{Reason: ChangeInvalid, Field: "version", Err: errors.New("the version cannot be raised any further")}
	}
	return current + 1, nil
}

// operationOf builds the operation a change records, from the request and the
// result the management layer built for the state the change produces. A request
// without an operation id is refused: the file would record an operation that
// its own decoder refuses.
func operationOf[T any](req Request, build ResultFunc[T], after T) (*Operation, error) {
	if build == nil {
		return nil, errors.New("config: a versioned change needs a ResultFunc")
	}
	if req.Key.ID.IsZero() {
		return nil, &ChangeError{Reason: ChangeInvalid, Field: "id", Err: errors.New("a versioned change needs an operation id")}
	}
	raw, err := build(after)
	if err != nil {
		return nil, fmt.Errorf("config: building the result of the change: %w", err)
	}
	result, err := compactObject(raw)
	if err != nil {
		return nil, fmt.Errorf("config: the result of a change %w", err)
	}
	return &Operation{ID: req.Key.ID, Person: req.Key.Person, Fingerprint: req.Fingerprint, Result: result}, nil
}

// replaceFile is the transaction of a change of one file.
func (c *Config) replaceFile(decl *txn.Decl, role, p string, data []byte) error {
	tx := c.w.Begin(decl)
	defer tx.Close()
	if err := tx.Replace(role, p, data, fsx.ModePrivate); err != nil {
		return err
	}
	return tx.Commit()
}

// SetNodeSettings changes the settings of the node named by patch, a JSON object
// of settings, atomically: with one value refused nothing changes. The patch is
// merged into the current settings and the result is validated whole, so a
// setting is judged against the others it depends on. The change is recorded in
// node.json with the operation and the result, in the same replacement.
func (c *Config) SetNodeSettings(req Request, patch []byte, result ResultFunc[Node]) (Node, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	cur := c.read().node
	if err := checkVersion(req, cur.Version); err != nil {
		return Node{}, err
	}
	p, err := DecodeNodePatch(patch)
	if err != nil {
		return Node{}, invalidChange(err)
	}
	if p.isEmpty() {
		return Node{}, &ChangeError{Reason: ChangeNothingToChange}
	}
	version, err := nextVersion(cur.Version)
	if err != nil {
		return Node{}, err
	}
	next := Node{Version: version, Settings: p.apply(cur.Settings)}
	if err := next.Settings.Validate(); err != nil {
		return Node{}, invalidChange(err)
	}
	if next.LastOperation, err = operationOf(req, result, next); err != nil {
		return Node{}, err
	}
	data, err := next.Encode()
	if err != nil {
		return Node{}, err
	}
	if err := c.replaceFile(nodeSet, RoleNode, NodePath, data); err != nil {
		return Node{}, err
	}
	c.mu.Lock()
	c.st.node = next
	c.mu.Unlock()
	return c.Node(), nil
}

// AddPreset adds a preset named name from its document. The document must make
// the statement about local sources; a name that is taken is refused.
func (c *Config) AddPreset(req Request, name string, preset []byte, result ResultFunc[Presets]) (Presets, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	cur := c.read().presets
	if err := checkVersion(req, cur.Version); err != nil {
		return Presets{}, err
	}
	if err := adapterproto.ValidatePresetName(name); err != nil {
		return Presets{}, invalidChange(invalid("name", err))
	}
	p, err := DecodePreset(preset)
	if err != nil {
		return Presets{}, invalidChange(err)
	}
	if _, taken := cur.Items[name]; taken {
		return Presets{}, &ChangeError{Reason: ChangePresetExists, Field: name}
	}
	next := clonePresets(cur)
	version, err := nextVersion(next.Version)
	if err != nil {
		return Presets{}, err
	}
	next.Version = version
	next.Items[name] = p
	return c.commitPresets(req, next, result)
}

// RemovePreset removes the preset named name.
func (c *Config) RemovePreset(req Request, name string, result ResultFunc[Presets]) (Presets, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	cur := c.read().presets
	if err := checkVersion(req, cur.Version); err != nil {
		return Presets{}, err
	}
	if _, ok := cur.Items[name]; !ok {
		return Presets{}, &ChangeError{Reason: ChangePresetUnknown, Field: name}
	}
	next := clonePresets(cur)
	version, err := nextVersion(next.Version)
	if err != nil {
		return Presets{}, err
	}
	next.Version = version
	delete(next.Items, name)
	return c.commitPresets(req, next, result)
}

func (c *Config) commitPresets(req Request, next Presets, result ResultFunc[Presets]) (Presets, error) {
	op, err := operationOf(req, result, next)
	if err != nil {
		return Presets{}, err
	}
	next.LastOperation = op
	data, err := next.Encode()
	if err != nil {
		return Presets{}, err
	}
	if err := c.replaceFile(presetsChange, RolePresets, PresetsPath, data); err != nil {
		return Presets{}, err
	}
	c.mu.Lock()
	c.st.presets = next
	c.mu.Unlock()
	return c.Presets(), nil
}

// commitInstances writes a new instances.json. The settings of session
// endpoints that have closed go with it; see dropClosedSessionSettings.
func (c *Config) commitInstances(decl *txn.Decl, req Request, next Instances, open OpenEndpoints, result ResultFunc[Instances]) (Instances, error) {
	next = dropClosedSessionSettings(next, c.read().endpointIDs, open)
	op, err := operationOf(req, result, next)
	if err != nil {
		return Instances{}, err
	}
	next.LastOperation = op
	data, err := next.Encode()
	if err != nil {
		return Instances{}, err
	}
	if err := c.replaceFile(decl, RoleInstances, InstancesPath, data); err != nil {
		return Instances{}, err
	}
	c.mu.Lock()
	c.st.instances = next
	c.mu.Unlock()
	return c.Instances(), nil
}

// EnableAdapter enables the instance named instance of an installed adapter. A
// new instance is created, with instanceConfig as its configuration when that
// is given (a JSON object); an existing one is turned on, and keeps what the
// owner set. An existing instance of another adapter, and an existing instance
// together with a configuration, which would be silently ignored or replace the
// owner's, are refused.
func (c *Config) EnableAdapter(req Request, instance, adapter string, instanceConfig []byte, open OpenEndpoints, result ResultFunc[Instances]) (Instances, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	st := c.read()
	if err := checkVersion(req, st.instances.Version); err != nil {
		return Instances{}, err
	}
	if err := checkName("instance", instance); err != nil {
		return Instances{}, invalidChange(err)
	}
	if err := checkName("adapter", adapter); err != nil {
		return Instances{}, invalidChange(err)
	}
	var cfg map[string]json.RawMessage
	if instanceConfig != nil {
		var raw map[string]json.RawMessage
		if err := decodeStrict(instanceConfig, &raw); err != nil {
			return Instances{}, invalidChange(under("config", err))
		}
		var err error
		if cfg, err = checkValues(raw); err != nil {
			return Instances{}, invalidChange(under("config", err))
		}
	}
	existing, exists := st.instances.Items[instance]
	switch {
	case exists && existing.Adapter != adapter:
		return Instances{}, &ChangeError{Reason: ChangeInstanceAdapterMismatch, Field: instance,
			Err: fmt.Errorf("the instance belongs to %q", existing.Adapter)}
	case exists && instanceConfig != nil:
		return Instances{}, &ChangeError{Reason: ChangeInstanceExists, Field: instance}
	}
	if _, installed := st.adapters[adapter]; !installed {
		return Instances{}, &ChangeError{Reason: ChangeAdapterNotInstalled, Field: adapter}
	}
	next := cloneInstances(st.instances)
	version, err := nextVersion(next.Version)
	if err != nil {
		return Instances{}, err
	}
	next.Version = version
	if exists {
		in := next.Items[instance]
		in.Enabled = true
		next.Items[instance] = in
	} else {
		next.Items[instance] = Instance{Adapter: adapter, Enabled: true, Config: cfg}
	}
	return c.commitInstances(adapterEnable, req, next, open, result)
}

// DisableAdapter disables the instance named instance. The instance stays, with
// its settings and its stable endpoint ids, so that enabling it again finds the
// same endpoints.
func (c *Config) DisableAdapter(req Request, instance string, open OpenEndpoints, result ResultFunc[Instances]) (Instances, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	st := c.read()
	if err := checkVersion(req, st.instances.Version); err != nil {
		return Instances{}, err
	}
	if _, ok := st.instances.Items[instance]; !ok {
		return Instances{}, &ChangeError{Reason: ChangeInstanceUnknown, Field: instance}
	}
	next := cloneInstances(st.instances)
	version, err := nextVersion(next.Version)
	if err != nil {
		return Instances{}, err
	}
	next.Version = version
	in := next.Items[instance]
	in.Enabled = false
	next.Items[instance] = in
	return c.commitInstances(adapterDisable, req, next, open, result)
}

// SetEndpointSettings changes the settings of one endpoint of an instance: the
// settings the patch names change and the others stay. The endpoint is a stable
// endpoint of the instance or one that is open now; a setting for any other
// would be dropped with the next write, so it is refused. Without knowledge of
// the open endpoints that is not judged.
func (c *Config) SetEndpointSettings(req Request, instance string, endpoint ident.EndpointID, patch []byte, open OpenEndpoints, result ResultFunc[Instances]) (Instances, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	st := c.read()
	if err := checkVersion(req, st.instances.Version); err != nil {
		return Instances{}, err
	}
	in, ok := st.instances.Items[instance]
	if !ok {
		return Instances{}, &ChangeError{Reason: ChangeInstanceUnknown, Field: instance}
	}
	p, err := DecodeEndpointPatch(patch)
	if err != nil {
		return Instances{}, invalidChange(err)
	}
	if p.isEmpty() {
		return Instances{}, &ChangeError{Reason: ChangeNothingToChange}
	}
	if err := p.validate(); err != nil {
		return Instances{}, invalidChange(err)
	}
	if open != nil && !st.endpointIDs[instance].stable(endpoint) && !open(endpoint) {
		return Instances{}, &ChangeError{Reason: ChangeEndpointUnknown, Field: endpoint.String()}
	}
	next := cloneInstances(st.instances)
	version, err := nextVersion(next.Version)
	if err != nil {
		return Instances{}, err
	}
	next.Version = version
	cur := next.Items[instance]
	if cur.Endpoints == nil {
		cur.Endpoints = map[ident.EndpointID]EndpointOverride{}
	}
	cur.Endpoints[endpoint] = p.apply(in.Endpoints[endpoint])
	next.Items[instance] = cur
	return c.commitInstances(endpointSet, req, next, open, result)
}

// RuntimeID returns the runtime endpoint id of an instance. An instance that has
// none gets one minted from rnd; the id is then written by PutEndpointIDs in
// the transaction that registers the endpoint.
func (c *Config) RuntimeID(instance string, rnd io.Reader) (IDChange, error) {
	return c.stableID(instance, "", rnd)
}

// DirectID returns the id of the direct endpoint an adapter registers under key.
// A key that is new gets an id minted from rnd. The same key always gives the
// same id, which is the promise the adapter protocol makes.
func (c *Config) DirectID(instance, key string, rnd io.Reader) (IDChange, error) {
	if err := checkKey("key", key); err != nil {
		return IDChange{}, invalidChange(err)
	}
	return c.stableID(instance, key, rnd)
}

func (c *Config) stableID(instance, key string, rnd io.Reader) (IDChange, error) {
	st := c.read()
	if _, ok := st.instances.Items[instance]; !ok {
		return IDChange{}, &ChangeError{Reason: ChangeInstanceUnknown, Field: instance}
	}
	ids := cloneEndpointIDs(st.endpointIDs[instance])
	if key == "" && !ids.Runtime.IsZero() {
		return IDChange{Instance: instance, ID: ids.Runtime, ids: ids}, nil
	}
	if id, ok := ids.Direct[key]; key != "" && ok {
		return IDChange{Instance: instance, ID: id, ids: ids}, nil
	}
	id, err := ident.NewEndpointID(rnd)
	if err != nil {
		return IDChange{}, err
	}
	for name, other := range st.endpointIDs {
		if what, dup := other.every()[id]; dup {
			return IDChange{}, fmt.Errorf("config: a minted endpoint id collides with %s of instance %s", what, name)
		}
	}
	if key == "" {
		ids.Runtime = id
	} else {
		if ids.Direct == nil {
			ids.Direct = map[string]ident.EndpointID{}
		}
		ids.Direct[key] = id
	}
	data, err := ids.Encode()
	if err != nil {
		return IDChange{}, err
	}
	return IDChange{Instance: instance, ID: id, New: true, ids: ids, data: data}, nil
}

// PutEndpointIDs writes the ids of an instance with the id of ch in them, under
// RoleEndpointIDs, in a transaction of the composing package. The ids were
// encoded when RuntimeID or DirectID minted the id, before the transaction, so
// the write can only fail the way a write of the transaction does. For an id that
// already existed it writes nothing. The returned function makes the
// configuration in memory take the ids over; it is called after the transaction
// committed.
func (c *Config) PutEndpointIDs(tx *txn.Tx, ch IDChange) (AfterCommit, error) {
	if !ch.New {
		return func() {}, nil
	}
	if err := tx.Replace(RoleEndpointIDs, EndpointIDsPath(ch.Instance), ch.data, fsx.ModePrivate); err != nil {
		return nil, err
	}
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		all := make(map[string]EndpointIDs, len(c.st.endpointIDs)+1)
		for k, v := range c.st.endpointIDs {
			all[k] = v
		}
		all[ch.Instance] = cloneEndpointIDs(ch.ids)
		c.st.endpointIDs = all
	}, nil
}

// PreparedBackup is the owner's confirmation of the offline backup, checked and
// encoded for a transaction of the composing package. PrepareBackup runs before
// that transaction begins, so a confirmation that cannot be written is an error
// that touches nothing, and PutBackup can only fail the way a write of the
// transaction does.
type PreparedBackup struct {
	b    Backup
	data []byte
}

// PrepareBackup checks and encodes a confirmation. It does no I/O.
func PrepareBackup(b Backup) (PreparedBackup, error) {
	data, err := b.Encode()
	if err != nil {
		return PreparedBackup{}, err
	}
	return PreparedBackup{b: b, data: data}, nil
}

// PutBackup writes a prepared confirmation under RoleBackup, in a transaction of
// the composing package. The returned function is called after the transaction
// committed. It panics for a PreparedBackup that PrepareBackup did not return.
func (c *Config) PutBackup(tx *txn.Tx, p PreparedBackup) (AfterCommit, error) {
	if p.data == nil {
		panic("config: PutBackup of a value PrepareBackup did not return")
	}
	if err := tx.Replace(RoleBackup, BackupPath, p.data, fsx.ModePrivate); err != nil {
		return nil, err
	}
	b := p.b
	b.ConfirmedAt = b.ConfirmedAt.UTC()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.st.backup = &b
	}, nil
}

// PreparedRevocation is a verified revocation record, with its file, for a
// transaction of the composing package. PrepareRevocation runs before that
// transaction begins, so a record that does not verify is an error that touches
// nothing, and PutRevocation can only fail the way a write of the transaction
// does.
type PreparedRevocation struct {
	rev  revocation.Revocation
	rec  record.Record
	data []byte
}

// PrepareRevocation verifies a revocation record and encodes its file, the
// record's own wire bytes. It does no I/O.
func PrepareRevocation(rec record.Record) (PreparedRevocation, error) {
	rev, err := revocation.Verify(rec)
	if err != nil {
		return PreparedRevocation{}, fmt.Errorf("config: the record is not a valid revocation: %w", err)
	}
	data, err := rec.Encode()
	if err != nil {
		return PreparedRevocation{}, err
	}
	return PreparedRevocation{rev: rev, rec: rec, data: data}, nil
}

// PutRevocation writes a prepared revocation under the name its digest gives,
// under RoleRevocation, in a transaction of the composing package. The returned
// function is called after the transaction committed. It panics for a
// PreparedRevocation that PrepareRevocation did not return.
func (c *Config) PutRevocation(tx *txn.Tx, p PreparedRevocation) (AfterCommit, error) {
	if p.data == nil {
		panic("config: PutRevocation of a value PrepareRevocation did not return")
	}
	if err := tx.Replace(RoleRevocation, RevocationPath(p.rec.Digest()), p.data, fsx.ModePrivate); err != nil {
		return nil, err
	}
	rev, rec := p.rev, p.rec
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, k := range c.st.revocations {
			if k.rev.Digest() == rev.Digest() {
				return
			}
		}
		kept := append(slices.Clone(c.st.revocations), keptRevocation{rev: rev, rec: rec})
		slices.SortFunc(kept, func(a, b keptRevocation) int {
			return strings.Compare(a.rev.Digest().String(), b.rev.Digest().String())
		})
		c.st.revocations = kept
	}, nil
}
