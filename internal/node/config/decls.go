package config

import "github.com/aianyai/sidera/internal/node/txn"

// The roles of the writes this package offers to transactions of other
// packages. A transaction that spans owners is declared by the package that
// composes it, with these names, and every write function below writes only
// under the role it is documented with.
const (
	// RoleDir creates the directories of config/ (init).
	RoleDir = "dir"
	// RoleNode, RolePolicy, RolePresets and RoleInstances replace the file of
	// that name; RoleNetworkConfig replaces network.json (the role "network" is
	// the network key of keys, in the same transaction).
	RoleNode          = "node"
	RolePolicy        = "policy"
	RolePresets       = "presets"
	RoleInstances     = "instances"
	RoleNetworkConfig = "network-config"
	// RoleEndpointIDs replaces the endpoint-id file of one instance.
	RoleEndpointIDs = "ids"
	// RoleBackup replaces revocation-backup.json.
	RoleBackup = "backup"
	// RoleRevocation writes one revocation record.
	RoleRevocation = "revocation"
	// RoleVersion, RoleCurrent and RoleOld are the writes of an adapter
	// installation: rename the staging directory to the version, replace the
	// current pointer, discard the previous version.
	RoleVersion = "version"
	RoleCurrent = "current"
	RoleOld     = "old"
)

// The write lists this package contributes to the transactions other packages
// compose, as keys does for init: the composer declares the transaction, puts
// these writes in the order documented here, and adds its own writes around
// them. The writes are the ones the write functions make, so a composer's
// declaration and the functions cannot drift apart.

// InitWrites are the writes of init that belong to this package, in the order
// they are made: the directories, then the files that are not already valid.
// The writes of the other owners and the marker come after.
func InitWrites() []txn.Write {
	return []txn.Write{
		{Role: RoleDir, Op: txn.OpMakeDir, Many: true, Optional: true},
		{Role: RoleNode, Op: txn.OpReplace, Optional: true},
		{Role: RolePolicy, Op: txn.OpReplace, Optional: true},
		{Role: RolePresets, Op: txn.OpReplace, Optional: true},
		{Role: RoleInstances, Op: txn.OpReplace, Optional: true},
		{Role: RoleNetworkConfig, Op: txn.OpReplace, Optional: true},
	}
}

// EndpointIDsWrite is the write of PutEndpointIDs, in direct-register and
// runtime-admit. It is optional: an id that already exists is not written.
func EndpointIDsWrite() txn.Write {
	return txn.Write{Role: RoleEndpointIDs, Op: txn.OpReplace, Optional: true}
}

// BackupWrite is the write of PutBackup, in backup-confirm.
func BackupWrite() txn.Write { return txn.Write{Role: RoleBackup, Op: txn.OpReplace} }

// RevocationWrite is the write of PutRevocation, in identity-retire.
func RevocationWrite() txn.Write { return txn.Write{Role: RoleRevocation, Op: txn.OpReplace} }

// InstallWrites are the writes of adapter-install up to and including the
// current pointer, in the order of Install's methods: Promote is preparation (a
// crash before the pointer changes leaves a directory adapter-gc discards), and
// Point is the commit point. The composer's own write
// follows, then InstallOldWrite.
func InstallWrites() []txn.Write {
	return []txn.Write{
		{Role: RoleVersion, Op: txn.OpRenameDir, Prepare: true},
		{Role: RoleCurrent, Op: txn.OpReplace},
	}
}

// InstallOldWrite is the write of Install.DiscardOld, the last of
// adapter-install. It is optional: a first installation has no previous version.
func InstallOldWrite() txn.Write { return txn.Write{Role: RoleOld, Op: txn.OpDiscard, Optional: true} }

// The roles of adapter-stage and adapter-unstage, which only this package
// uses.
const (
	roleParent = "parent"
	roleStage  = "stage"
	roleSubdir = "subdir"
	roleFile   = "file"

	// The roles of adapter-remove and adapter-state-drop.
	roleState       = "state"
	roleTrash       = "trash"
	roleTrashParent = "trash-parent"
)

var (
	// nodeSet changes node.json: the settings, with the operation that did it.
	nodeSet = &txn.Decl{Name: "node-set", Kind: txn.Normal, Writes: []txn.Write{
		{Role: RoleNode, Op: txn.OpReplace},
	}}
	// presetsChange adds or removes a preset.
	presetsChange = &txn.Decl{Name: "presets-change", Kind: txn.Normal, Writes: []txn.Write{
		{Role: RolePresets, Op: txn.OpReplace},
	}}
	// endpointSet changes the settings of an endpoint.
	endpointSet = &txn.Decl{Name: "endpoint-set", Kind: txn.Normal, Writes: []txn.Write{
		{Role: RoleInstances, Op: txn.OpReplace},
	}}
	// networkSet changes network.json: the listen addresses or the address book,
	// with the operation that did it.
	networkSet = &txn.Decl{Name: "network-set", Kind: txn.Normal, Writes: []txn.Write{
		{Role: RoleNetworkConfig, Op: txn.OpReplace},
	}}
	// adapterEnable enables an instance, creating it when it is new.
	adapterEnable = &txn.Decl{Name: "adapter-enable", Kind: txn.Normal, Writes: []txn.Write{
		{Role: RoleInstances, Op: txn.OpReplace},
	}}
	// adapterConfig replaces the configuration of an instance.
	adapterConfig = &txn.Decl{Name: "adapter-config", Kind: txn.Normal, Writes: []txn.Write{
		{Role: RoleInstances, Op: txn.OpReplace},
	}}
	// adapterRemove removes a disabled instance from instances.json. Before that
	// it moves the state directory of the instance aside, to a garbage name: what
	// is left of a crash between the two is a directory the next start removes, and
	// the instance still configured with an empty state, which a start of its
	// adapter makes anew.
	adapterRemove = &txn.Decl{Name: "adapter-remove", Kind: txn.Normal, Writes: []txn.Write{
		{Role: roleState, Op: txn.OpRenameDir, Optional: true, Prepare: true},
		{Role: RoleInstances, Op: txn.OpReplace},
	}}
	// adapterStateDrop deletes the state directory that adapter-remove moved
	// aside, after the removal was committed and outside the loop that committed
	// it. It is all preparation: what a crash leaves is the garbage name the next
	// start removes, so no point of it can lose anything.
	adapterStateDrop = &txn.Decl{Name: "adapter-state-drop", Kind: txn.Normal, Writes: []txn.Write{
		{Role: roleTrash, Op: txn.OpRemoveAll, Prepare: true},
		{Role: roleTrashParent, Op: txn.OpSyncDir, Prepare: true},
	}}
	// adapterDisable disables an instance. The instance stays, with its
	// endpoint ids.
	adapterDisable = &txn.Decl{Name: "adapter-disable", Kind: txn.Normal, Writes: []txn.Write{
		{Role: RoleInstances, Op: txn.OpReplace},
	}}
	// adapterStage copies a package next to the versions. Everything it writes
	// is preparation: nothing is installed until the current pointer changes.
	adapterStage = &txn.Decl{Name: "adapter-stage", Kind: txn.Normal, Writes: []txn.Write{
		{Role: roleParent, Op: txn.OpMakeDir, Many: true, Optional: true, Prepare: true},
		{Role: roleStage, Op: txn.OpMakeDir, Prepare: true},
		{Role: roleSubdir, Op: txn.OpMakeDir, Many: true, Optional: true, Prepare: true},
		{Role: roleFile, Op: txn.OpReplace, Many: true, Prepare: true},
	}}
	// adapterUnstage drops the staging of a refused or failed installation.
	adapterUnstage = &txn.Decl{Name: "adapter-unstage", Kind: txn.Normal, Writes: []txn.Write{
		{Role: roleStage, Op: txn.OpDiscard, Optional: true},
		{Role: roleParent, Op: txn.OpDiscard, Optional: true},
	}}
	// adapterGC repairs what a crash left among the adapter packages.
	adapterGC = &txn.Decl{Name: "adapter-gc", Kind: txn.Recovery, Writes: []txn.Write{
		{Role: RoleDir, Op: txn.OpDiscard, Many: true},
	}}
)

// Transactions lists the transactions this package runs itself. The composition
// root puts them in the table of the program.
func Transactions() []*txn.Decl {
	return []*txn.Decl{nodeSet, networkSet, presetsChange, endpointSet, adapterEnable, adapterConfig, adapterDisable, adapterRemove, adapterStateDrop, adapterStage, adapterUnstage, adapterGC}
}
