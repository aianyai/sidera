package engine

import (
	"slices"

	"github.com/aianyai/sidera/internal/node/config"
	nodekeys "github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// declDirectRegister registers a direct endpoint: the id of the endpoint is
// kept in config/ first, when it is new, and the endpoint record in state/
// second. One transaction, because the two owners' files have to agree; the
// engine opens it, as the declarations of config and store say a transaction of
// two owners must be. A crash between the two writes leaves an id that no
// endpoint record uses, which is no endpoint, takes no place in the cap, and is
// given back for the same key when the adapter asks again.
var declDirectRegister = txn.Decl{Name: "direct-register", Kind: txn.Normal, Writes: []txn.Write{
	config.EndpointIDsWrite(),
	{Role: store.RoleEndpoint, Op: txn.OpReplace},
}}

// The administrative operations whose effect is a file of one owner and whose
// result is kept under state/admin/ (those that change a versioned setting keep
// theirs in the file they change, and need no transaction of the engine). Each is
// one transaction of two owners, opened by the engine: the effect, then the
// result, which is the last write. The transaction commits with its first write,
// so a crash between the two leaves the effect without its result, and the request
// sent again finds the effect there; each operation is written so that doing it
// again is harmless (see the operation).
var (
	// clock-clear: the high water mark is set to the clock as it reads and the
	// pause flag removed, in one replacement of keys/clock.rec. The write is
	// optional because a request that finds no pause has nothing to write there and
	// still stores its result.
	declClockClear = txn.Decl{Name: "clock-clear", Kind: txn.Normal, Writes: []txn.Write{
		{Role: nodekeys.RoleClock, Op: txn.OpReplace, Optional: true},
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
	// backup-confirm: the owner's confirmation that the self-revocation is kept
	// outside this device.
	declBackupConfirm = txn.Decl{Name: "backup-confirm", Kind: txn.Normal, Writes: []txn.Write{
		config.BackupWrite(),
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
	// identity-retire: the node's own self-revocation, as a verified revocation
	// under config/revocations/. It is under config/ and not state/ because
	// deleting state/ must not bring a retired identity back.
	declIdentityRetire = txn.Decl{Name: "identity-retire", Kind: txn.Normal, Writes: []txn.Write{
		config.RevocationWrite(),
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
)

// declRuntimeAdmit makes the runtime endpoint of an adapter instance exist when
// the adapter declares a runtime: the id in config/ first, when it is new, and
// the endpoint record in state/ second, in one transaction opened by the engine
// for the same reason as direct-register's. A crash between the two writes leaves
// an id that no record uses, and the next admission of the instance builds the
// endpoint under it.
var declRuntimeAdmit = txn.Decl{Name: "runtime-admit", Kind: txn.Normal, Writes: []txn.Write{
	config.EndpointIDsWrite(),
	{Role: store.RoleEndpoint, Op: txn.OpReplace},
}}

// The installation of an adapter package. The package is copied into a directory
// next to the versions outside the loop (config's adapter-stage), and this is the
// part that goes through the loop: the staged directory is renamed to its version,
// the pointer that names the current version is replaced (the commit point), the
// result is stored, and the previous version is discarded. A crash before the
// pointer leaves a directory that the repair of the adapter directory removes, and
// the old version current; after it the new version is current, and a version left
// over is removed by the same repair. The writes of the package's files are config's
// and are composed here in the order its Install documents, with the result between
// the pointer and the discarding of the old version.
var declAdapterInstall = txn.Decl{Name: "adapter-install", Kind: txn.Normal, Writes: slices.Concat(
	config.InstallWrites(),
	[]txn.Write{{Role: store.RoleResult, Op: txn.OpReplace}},
	[]txn.Write{config.InstallOldWrite()},
)}

// adapter-install-current stores the result of an installation that found its
// version current: there is nothing to rename, and the result is all there is.
var declAdapterInstallCurrent = txn.Decl{Name: "adapter-install-current", Kind: txn.Normal, Writes: []txn.Write{
	{Role: store.RoleResult, Op: txn.OpReplace},
}}

// declTrustLocalRevoke removes a node from this node's trust: the entry that says
// so is the commit point, so a crash after it leaves the node refused, and the
// revocations of the credentials this node holds with it, which the entry makes
// necessary, follow it; a restart or a resend finds the entry and writes the ones
// still missing. The result is last. It is the engine's because the entry is the
// store's, the revocations are the credentials' and the result is the owner's
// record of the operation, one transaction of the three.
var declTrustLocalRevoke = txn.Decl{Name: "trust-local-revoke", Kind: txn.Normal, Writes: []txn.Write{
	{Role: store.RoleLocalRevoked, Op: txn.OpReplace},
	{Role: store.RoleCredRevocation, Op: txn.OpReplace, Many: true, Optional: true},
	{Role: store.RoleResult, Op: txn.OpReplace},
}}

// The owner's operations on pairing and on a node removed from trust. Each is one
// transaction of two owners, opened by the engine: the effect, then the result,
// which is the last write.
var (
	// trust-expect and trust-forget: the whole list of expectations, replaced, and
	// the result.
	declTrustExpect = txn.Decl{Name: "trust-expect", Kind: txn.Normal, Writes: []txn.Write{
		{Role: store.RolePairingExpectations, Op: txn.OpReplace},
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
	declTrustForget = txn.Decl{Name: "trust-forget", Kind: txn.Normal, Writes: []txn.Write{
		{Role: store.RolePairingExpectations, Op: txn.OpReplace},
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
	// trust-request: the request the initiator saved when the target accepted it
	// and the result of the owner's request for the pairing, so that the result
	// never exists without the request it reports, nor the request without the
	// result.
	declTrustRequest = txn.Decl{Name: "trust-request", Kind: txn.Normal, Writes: []txn.Write{
		{Role: store.RolePairingInitiator, Op: txn.OpReplace},
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
	// trust-reject: the record of the request the owner rejected, which is either
	// one the node holds as a target or one it started, and the result.
	declTrustReject = txn.Decl{Name: "trust-reject", Kind: txn.Normal, Writes: []txn.Write{
		{Role: store.RolePairingTarget, Op: txn.OpReplace, Optional: true},
		{Role: store.RolePairingInitiator, Op: txn.OpReplace, Optional: true},
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
	// trust-clear: the removal of the entry of a node removed from trust, made
	// durable, and the result.
	declTrustClear = txn.Decl{Name: "trust-clear", Kind: txn.Normal, Writes: []txn.Write{
		{Role: store.RoleLocalRevoked, Op: txn.OpRemove},
		{Role: store.RoleLocalRevokedDir, Op: txn.OpSyncDir},
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
	// trust-result stores the result of an operation on trust whose effect wrote
	// nothing: a request for a pairing that the target refused, and the clearing of
	// a node that was not removed.
	declTrustResult = txn.Decl{Name: "trust-result", Kind: txn.Normal, Writes: []txn.Write{
		{Role: store.RoleResult, Op: txn.OpReplace},
	}}
)

// Transactions returns the declarations of the transactions of the engine that
// write the files of more than one owner, for the composition root to put in
// the table of the program. The transactions of one owner are declared by the
// owner.
func Transactions() []*txn.Decl {
	return []*txn.Decl{&declDirectRegister, &declRuntimeAdmit, &declClockClear, &declBackupConfirm, &declIdentityRetire, &declAdapterInstall, &declAdapterInstallCurrent, &declTrustLocalRevoke,
		&declTrustExpect, &declTrustForget, &declTrustRequest, &declTrustReject, &declTrustClear, &declTrustResult}
}
