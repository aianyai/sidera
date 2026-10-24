package config

import (
	"errors"
	"io"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

var errUnimplemented = errors.New("config: not implemented")

// The skeleton: every behaviour below is written in the commit that follows
// the tests.

// DefaultNodeSettings are the settings `init` writes.
func DefaultNodeSettings() NodeSettings { return NodeSettings{} }

// Validate checks every setting.
func (s NodeSettings) Validate() error { return errUnimplemented }

// Validate checks the settings of an endpoint.
func (s EndpointSettings) Validate() error { return errUnimplemented }

// EndpointPatch names the endpoint settings a request changes.
type EndpointPatch struct {
	MaxQueue     *time.Duration
	BacklogLimit *int
}

// NodePatch names the node settings a request changes.
type NodePatch struct {
	ReplyPermission      *bool
	NodeQueueLimit       *int
	DiskReserveBytes     *int64
	NodePromptTurns      *int
	ArchiveRetentionDays *int
	LogTotalBytes        *int64
	EndpointDefaults     *EndpointPatch
}

// DecodeNodePatch decodes the settings object of node.set.
func DecodeNodePatch(data []byte) (NodePatch, error) { return NodePatch{}, errUnimplemented }

// DecodeEndpointPatch decodes the settings object of endpoints.set.
func DecodeEndpointPatch(data []byte) (EndpointPatch, error) {
	return EndpointPatch{}, errUnimplemented
}

// DecodeNode decodes node.json.
func DecodeNode(data []byte) (Node, error) { return Node{}, errUnimplemented }

// Encode returns the bytes of node.json.
func (n Node) Encode() ([]byte, error) { return nil, errUnimplemented }

// DecodePolicy decodes policy.json.
func DecodePolicy(data []byte) (PolicyFile, error) { return PolicyFile{}, errUnimplemented }

// Encode returns the bytes of policy.json.
func (p PolicyFile) Encode() ([]byte, error) { return nil, errUnimplemented }

// DecodePresets decodes presets.json.
func DecodePresets(data []byte) (Presets, error) { return Presets{}, errUnimplemented }

// Encode returns the bytes of presets.json.
func (p Presets) Encode() ([]byte, error) { return nil, errUnimplemented }

// DecodePreset decodes one preset document, as presets.add carries it.
func DecodePreset(data []byte) (Preset, error) { return Preset{}, errUnimplemented }

// DecodeInstances decodes instances.json.
func DecodeInstances(data []byte) (Instances, error) { return Instances{}, errUnimplemented }

// Encode returns the bytes of instances.json.
func (i Instances) Encode() ([]byte, error) { return nil, errUnimplemented }

// DecodeEndpointIDs decodes the endpoint-id file of an instance.
func DecodeEndpointIDs(instance string, data []byte) (EndpointIDs, error) {
	return EndpointIDs{}, errUnimplemented
}

// Encode returns the bytes of an endpoint-id file.
func (e EndpointIDs) Encode() ([]byte, error) { return nil, errUnimplemented }

// DecodeBackup decodes revocation-backup.json.
func DecodeBackup(data []byte) (Backup, error) { return Backup{}, errUnimplemented }

// Encode returns the bytes of revocation-backup.json.
func (b Backup) Encode() ([]byte, error) { return nil, errUnimplemented }

// DecodeAdapterCurrent decodes the current pointer of an adapter.
func DecodeAdapterCurrent(id string, data []byte) (string, error) { return "", errUnimplemented }

// EncodeAdapterCurrent returns the bytes of a current pointer.
func EncodeAdapterCurrent(version string) ([]byte, error) { return nil, errUnimplemented }

// DecodeRevocation decodes and verifies the file of a revocation record.
func DecodeRevocation(name string, data []byte) (revocation.Revocation, record.Record, error) {
	return revocation.Revocation{}, record.Record{}, errUnimplemented
}

// Config is the configuration of the node as loaded, and the one writer of
// config/.
type Config struct{}

// Load reads and checks all of config/ and repairs the adapter directory.
func Load(w *txn.Writer) (*Config, error) { return nil, errUnimplemented }

// Node returns node.json.
func (c *Config) Node() Node { return Node{} }

// Policy returns policy.json.
func (c *Config) Policy() PolicyFile { return PolicyFile{} }

// Presets returns presets.json.
func (c *Config) Presets() Presets { return Presets{} }

// Instances returns instances.json.
func (c *Config) Instances() Instances { return Instances{} }

// EndpointIDs returns the stable endpoint ids of an instance.
func (c *Config) EndpointIDs(instance string) EndpointIDs { return EndpointIDs{} }

// EndpointSettings returns the settings that apply to an endpoint of an
// instance.
func (c *Config) EndpointSettings(instance string, id ident.EndpointID) EndpointSettings {
	return EndpointSettings{}
}

// Backup returns the confirmation of the offline backup, if there is one.
func (c *Config) Backup() (Backup, bool) { return Backup{}, false }

// Revocations returns the verified revocations the node keeps.
func (c *Config) Revocations() []revocation.Revocation { return nil }

// RevocationSet returns the revocations as the set the judging packages read.
func (c *Config) RevocationSet() *revocation.Set { return nil }

// Adapters returns the installed adapters by id.
func (c *Config) Adapters() []Adapter { return nil }

// LastOperations returns the operation recorded in each versioned file.
func (c *Config) LastOperations() []Operation { return nil }

// SetNodeSettings changes node settings.
func (c *Config) SetNodeSettings(req Request, patch []byte, result ResultFunc[Node]) (Node, error) {
	return Node{}, errUnimplemented
}

// AddPreset adds a preset.
func (c *Config) AddPreset(req Request, name string, preset []byte, result ResultFunc[Presets]) (Presets, error) {
	return Presets{}, errUnimplemented
}

// RemovePreset removes a preset.
func (c *Config) RemovePreset(req Request, name string, result ResultFunc[Presets]) (Presets, error) {
	return Presets{}, errUnimplemented
}

// EnableAdapter enables an instance of an installed adapter, creating it when
// it does not exist.
func (c *Config) EnableAdapter(req Request, instance, adapter string, instanceConfig []byte, open OpenEndpoints, result ResultFunc[Instances]) (Instances, error) {
	return Instances{}, errUnimplemented
}

// DisableAdapter disables an instance.
func (c *Config) DisableAdapter(req Request, instance string, open OpenEndpoints, result ResultFunc[Instances]) (Instances, error) {
	return Instances{}, errUnimplemented
}

// SetEndpointSettings changes the settings of one endpoint of an instance.
func (c *Config) SetEndpointSettings(req Request, instance string, endpoint ident.EndpointID, patch []byte, open OpenEndpoints, result ResultFunc[Instances]) (Instances, error) {
	return Instances{}, errUnimplemented
}

// RuntimeID returns the runtime endpoint id of an instance, minting it when the
// instance has none.
func (c *Config) RuntimeID(instance string, rnd io.Reader) (IDChange, error) {
	return IDChange{}, errUnimplemented
}

// DirectID returns the endpoint id of a direct endpoint key, minting it when
// the key is new.
func (c *Config) DirectID(instance, key string, rnd io.Reader) (IDChange, error) {
	return IDChange{}, errUnimplemented
}

// PutEndpointIDs writes a minted id, under RoleEndpointIDs; nothing for an id
// that already existed.
func (c *Config) PutEndpointIDs(tx *txn.Tx, ch IDChange) (AfterCommit, error) {
	return nil, errUnimplemented
}

// PutBackup writes the confirmation of the offline backup, under RoleBackup.
func (c *Config) PutBackup(tx *txn.Tx, b Backup) (AfterCommit, error) {
	return nil, errUnimplemented
}

// PutRevocation verifies a revocation and writes it, under RoleRevocation.
func (c *Config) PutRevocation(tx *txn.Tx, r record.Record) (AfterCommit, error) {
	return nil, errUnimplemented
}

// StageAdapter copies the package that src is rooted at next to the versions.
func (c *Config) StageAdapter(src fsx.Reader, rnd io.Reader) (Staged, error) {
	return Staged{}, errUnimplemented
}

// Unstage drops the leftovers of an installation that did not complete.
func (c *Config) Unstage(s Staged) error { return errUnimplemented }

// Install is the plan of the writes that make a staged package the current
// version of its adapter.
type Install struct {
	// AlreadyCurrent is true when the version is the current one: there is
	// nothing to write.
	AlreadyCurrent bool
}

// PlanInstall plans the installation of a staged package.
func (c *Config) PlanInstall(s Staged) (Install, error) { return Install{}, errUnimplemented }

// DiscardStale discards a leftover directory of the version, under RoleStale.
func (i Install) DiscardStale(tx *txn.Tx) error { return errUnimplemented }

// Promote renames the staging directory to the version, under RoleVersion.
func (i Install) Promote(tx *txn.Tx) error { return errUnimplemented }

// Point replaces the current pointer, under RoleCurrent.
func (i Install) Point(tx *txn.Tx) (AfterCommit, error) { return nil, errUnimplemented }

// DiscardOld discards the previous version, under RoleOld.
func (i Install) DiscardOld(tx *txn.Tx) error { return errUnimplemented }

// AdapterGCWork lists what adapter-gc removes. It only reads.
func AdapterGCWork(r fsx.Reader) ([]GCItem, error) { return nil, errUnimplemented }

// InitPlan is what `init` has to do on the side of config/.
type InitPlan struct{}

// PlanInit checks the files `init` would write and decides which are missing.
func PlanInit(r fsx.Reader) (*InitPlan, error) { return nil, errUnimplemented }

// MakeDirs creates the missing directories, under RoleDir.
func (p *InitPlan) MakeDirs(tx *txn.Tx) error { return errUnimplemented }

// WriteFiles writes the missing files, each under its role.
func (p *InitPlan) WriteFiles(tx *txn.Tx) error { return errUnimplemented }
