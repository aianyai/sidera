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

// Transactions returns the declarations of the transactions of the engine that
// write the files of more than one owner, for the composition root to put in
// the table of the program. The transactions of one owner are declared by the
// owner.
func Transactions() []*txn.Decl {
	return []*txn.Decl{&declDirectRegister, &declRuntimeAdmit, &declClockClear, &declBackupConfirm, &declIdentityRetire, &declAdapterInstall, &declAdapterInstallCurrent}
}
