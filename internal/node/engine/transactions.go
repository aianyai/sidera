package engine

import (
	"github.com/aianyai/sidera/internal/node/config"
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

// Transactions returns the declarations of the transactions of the engine that
// write the files of more than one owner, for the composition root to put in
// the table of the program. The transactions of one owner are declared by the
// owner.
func Transactions() []*txn.Decl {
	return []*txn.Decl{&declDirectRegister}
}
