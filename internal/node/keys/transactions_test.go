package keys_test

import (
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/txn"
)

// The declarations are the order of writes written out by hand: the serial
// before the certificate in a renewal, and in init every write of this package
// optional (it reuses what is valid) except the marker, which is last and
// required.
func TestTransactionsAreDeclaredAndValid(t *testing.T) {
	decls := keys.Transactions()
	got := map[string]*txn.Decl{}
	for _, d := range decls {
		got[d.Name] = d
	}
	if len(decls) != 2 || got[keys.TxCertRenew] == nil || got[keys.TxClockHighWater] == nil {
		t.Fatalf("Transactions() = %d declarations %v, want cert-renew and clock-high-water", len(decls), got)
	}
	want := map[string][]txn.Write{
		"cert-renew": {
			{Role: "serial", Op: txn.OpReplace},
			{Role: "cert", Op: txn.OpReplace},
		},
		"clock-high-water": {
			{Role: "clock", Op: txn.OpReplace},
		},
	}
	for name, writes := range want {
		if d := got[name]; d.Kind != txn.Normal || !reflect.DeepEqual(d.Writes, writes) {
			t.Errorf("%s: kind %v writes %v, want a Normal transaction of %v", name, d.Kind, d.Writes, writes)
		}
	}

	wantInit := []txn.Write{
		{Role: "identity", Op: txn.OpReplace, Optional: true},
		{Role: "network", Op: txn.OpReplace, Optional: true},
		{Role: "serial", Op: txn.OpReplace, Optional: true},
		{Role: "cert", Op: txn.OpReplace, Optional: true},
		{Role: "selfrev", Op: txn.OpReplace, Optional: true},
		{Role: "clock", Op: txn.OpReplace, Optional: true},
	}
	if got := keys.InitWrites(); !reflect.DeepEqual(got, wantInit) {
		t.Errorf("InitWrites() = %v, want %v", got, wantInit)
	}
	if got, want := keys.InitMarkerWrite(), (txn.Write{Role: "marker", Op: txn.OpReplace}); got != want {
		t.Errorf("InitMarkerWrite() = %v, want %v", got, want)
	}

	// the table the composition root builds from them is valid, with the
	// declarations of the composers
	if _, err := txn.NewTable(append(append(txn.Internal(), decls...), initDecl, clearDecl)...); err != nil {
		t.Errorf("NewTable: %v", err)
	}
}

// The paths and roles are the documented ones.
func TestPathsAreTheDocumentedOnes(t *testing.T) {
	for got, want := range map[string]string{
		keys.Dir:                "keys",
		keys.IdentityKeyPath:    "keys/identity.key",
		keys.NetworkKeyPath:     "keys/network.key",
		keys.CertificatePath:    "keys/network.cert",
		keys.SerialPath:         "keys/network-serial",
		keys.SelfRevocationPath: "keys/self-revocation.rec",
		keys.ClockPath:          "keys/clock.rec",
		keys.InitMarkerPath:     "keys/init.rec",
	} {
		if got != want {
			t.Errorf("path %q, want %q", got, want)
		}
	}
	if keys.CertificateLifetime.Hours() != 30*24 || keys.RenewBefore.Hours() != 10*24 {
		t.Errorf("certificate lifetime %v and renewal margin %v, want 30 and 10 days", keys.CertificateLifetime, keys.RenewBefore)
	}
}
