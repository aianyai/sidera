package config_test

import (
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/txn"
)

// The write lists that composers take from this package, written out by hand:
// the roles, their order, and which writes are optional or preparation. A
// composer's declaration is built from them, so a change here is a change of
// every transaction that composes them.
func TestExportedWriteListsAreTheDocumentedOnes(t *testing.T) {
	for name, c := range map[string]struct{ got, want []txn.Write }{
		"init": {config.InitWrites(), []txn.Write{
			{Role: "dir", Op: txn.OpMakeDir, Many: true, Optional: true},
			{Role: "node", Op: txn.OpReplace, Optional: true},
			{Role: "policy", Op: txn.OpReplace, Optional: true},
			{Role: "presets", Op: txn.OpReplace, Optional: true},
			{Role: "instances", Op: txn.OpReplace, Optional: true},
		}},
		"endpoint ids": {[]txn.Write{config.EndpointIDsWrite()}, []txn.Write{{Role: "ids", Op: txn.OpReplace, Optional: true}}},
		"backup":       {[]txn.Write{config.BackupWrite()}, []txn.Write{{Role: "backup", Op: txn.OpReplace}}},
		"revocation":   {[]txn.Write{config.RevocationWrite()}, []txn.Write{{Role: "revocation", Op: txn.OpReplace}}},
		"install": {config.InstallWrites(), []txn.Write{
			{Role: "stale", Op: txn.OpDiscard, Optional: true, Prepare: true},
			{Role: "version", Op: txn.OpRenameDir, Prepare: true},
			{Role: "current", Op: txn.OpReplace},
		}},
		"install, previous version": {[]txn.Write{config.InstallOldWrite()}, []txn.Write{{Role: "old", Op: txn.OpDiscard, Optional: true}}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: %v, want %v", name, c.got, c.want)
		}
	}
}
