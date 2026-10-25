package engine

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
)

// BackupConfirmed is what the owner's confirmation of the backup did, for the
// result to say.
type BackupConfirmed struct {
	// At is the time the confirmation was recorded.
	At time.Time
}

// ConfirmRevocationBackupOp is the operation that records that the owner keeps a
// copy of the self-revocation outside this device. The owner names the digest of
// the statement the copy is of, which is the one this node holds and exported: a
// confirmation without it would be a button pressed blind, and one of another
// statement would put the reminder out for a copy that cannot retire this node.
// The confirmation is kept in config/, beside the statement it is for, and is not
// a setting: it carries no version, so it does not make a request that was based
// on the settings stale. encode makes the result; it runs in the loop and must not
// wait.
//
// Sent again under a new identifier it records a new time, which changes nothing
// that matters: the reminder is off in both.
func ConfirmRevocationBackupOp(digest record.Digest, encode func(BackupConfirmed) ([]byte, error)) Operation {
	return &confirmBackupOp{digest: digest, encode: encode}
}

type confirmBackupOp struct {
	digest record.Digest
	encode func(BackupConfirmed) ([]byte, error)
}

func (o *confirmBackupOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpConfirmRevocationBackup}
}
func (o *confirmBackupOp) stops() bool { return false }
func (o *confirmBackupOp) run(e *Engine, x *opRun) ([]byte, error) {
	if e.d.Keys == nil {
		return nil, errNoKeys
	}
	if o.digest != e.d.Keys.SelfRevocation().Digest() {
		return nil, &Refusal{Reason: ReasonInvalidArgument, Field: "digest", Detail: DetailDigestMismatch}
	}
	at := x.now.UTC()
	backup, err := config.PrepareBackup(config.Backup{ConfirmedAt: at, Digest: o.digest})
	if err != nil {
		return nil, err
	}
	result, err := o.encode(BackupConfirmed{At: at})
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	pr, err := store.PrepareAdminResult(x.record(result))
	if err != nil {
		return nil, err
	}
	tx := e.d.Writer.Begin(&declBackupConfirm)
	defer tx.Close()
	after, err := e.d.Config.PutBackup(tx, backup)
	if err != nil {
		return nil, err
	}
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	after()
	return result, nil
}

// RetireIdentityOp is the operation that retires the identity of this node for
// good: the self-revocation the keys verified as this node's is kept as a verified
// revocation under config/revocations/, and the daemon is asked to stop. The owner
// names the node, so that a retirement meant for another data directory is
// refused. After the commit nothing is signed in the node's name any more, even
// before the daemon is down, and the next start refuses to run (it finds the
// revocation among those it loads). It is not undone by deleting state/.
// encode makes the result; it runs in the loop and must not wait.
//
// A retirement is whole when the revocation is on disk: the result is written in
// the same transaction, after it. A crash between the two leaves the identity
// retired without a result, and the daemon is not running to be asked again.
func RetireIdentityOp(node ident.NodeID, encode func() ([]byte, error)) Operation {
	return &retireIdentityOp{node: node, encode: encode}
}

type retireIdentityOp struct {
	node   ident.NodeID
	encode func() ([]byte, error)
}

func (o *retireIdentityOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpRetireIdentity}
}
func (o *retireIdentityOp) stops() bool { return true }
func (o *retireIdentityOp) run(e *Engine, x *opRun) ([]byte, error) {
	if e.d.Keys == nil {
		return nil, errNoKeys
	}
	if o.node != e.d.Node {
		return nil, &Refusal{Reason: ReasonInvalidArgument, Field: "node", Detail: DetailNodeMismatch}
	}
	// The statement the keys verified at load as this node's: it is not read again,
	// so no node is compared here that the keys did not already compare.
	rec, err := record.Decode(e.d.Keys.SelfRevocation().Wire(), record.DefaultMaxInput)
	if err != nil {
		return nil, fmt.Errorf("engine: the self-revocation of the node: %w", err)
	}
	revocation, err := config.PrepareRevocation(rec)
	if err != nil {
		return nil, err
	}
	result, err := o.encode()
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	pr, err := store.PrepareAdminResult(x.record(result))
	if err != nil {
		return nil, err
	}
	tx := e.d.Writer.Begin(&declIdentityRetire)
	defer tx.Close()
	after, err := e.d.Config.PutRevocation(tx, revocation)
	if err != nil {
		return nil, err
	}
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	after()
	e.retired = true
	return result, nil
}
