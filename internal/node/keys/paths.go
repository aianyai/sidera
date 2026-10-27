package keys

import (
	"time"

	"github.com/aianyai/sidera/internal/node/txn"
)

// Paths of the files, relative to the root of the data directory.
const (
	// Dir is the directory this package owns.
	Dir = "keys"
	// IdentityKeyPath holds the node's long-term identity private key.
	IdentityKeyPath = "keys/identity.key"
	// NetworkKeyPath holds the private key of the node's network connections.
	NetworkKeyPath = "keys/network.key"
	// CertificatePath holds the current network-key certificate, as the record
	// wire bytes.
	CertificatePath = "keys/network.cert"
	// SerialPath holds the highest certificate serial this node has issued, with
	// the digest of the certificate issued at it.
	SerialPath = "keys/network-serial"
	// SelfRevocationPath holds the node's self-revocation statement, as the
	// record wire bytes.
	SelfRevocationPath = "keys/self-revocation.rec"
	// ClockPath holds the wall-clock high water mark and the rollback pause flag.
	ClockPath = "keys/clock.rec"
	// InitMarkerPath exists when init has finished.
	InitMarkerPath = "keys/init.rec"
)

// The roles of the writes this package declares, and offers to the packages
// that compose a transaction with writes of several owners. A composing
// transaction lists the roles it uses; the txn package checks the order.
const (
	RoleIdentityKey    = "identity"
	RoleNetworkKey     = "network"
	RoleSerial         = "serial"
	RoleCertificate    = "cert"
	RoleSelfRevocation = "selfrev"
	RoleClock          = "clock"
	RoleInitMarker     = "marker"
)

// The names of the transactions this package declares.
const (
	TxCertRenew      = "cert-renew"
	TxClockHighWater = "clock-high-water"
)

// Decided parameters of the network-key certificate.
const (
	// CertificateLifetime is how long a certificate is valid from the time it is
	// issued. Short enough that a leaked network key stops being accepted within
	// a month even if nobody revokes it, long enough that a node that is switched
	// off for a few days comes back with a valid certificate.
	CertificateLifetime = 30 * 24 * time.Hour
	// RenewBefore is the remaining validity at or below which a certificate is
	// renewed: a third of its lifetime, so that a node that fails to renew for a
	// week still holds a valid one.
	RenewBefore = 10 * 24 * time.Hour
)

var (
	certRenew = &txn.Decl{Name: TxCertRenew, Kind: txn.Normal, Writes: []txn.Write{
		// The serial is reserved first. It is the commit point, and from then on a
		// serial is never issued again, whatever happens to the certificate.
		{Role: RoleSerial, Op: txn.OpReplace},
		// The mark of the clock is raised to the time the certificate is signed at,
		// when it is below it, so that no certificate is ever above the mark. It
		// comes before the certificate: a crash after it leaves a mark that is
		// only higher than the clock has read, which costs nothing.
		{Role: RoleClock, Op: txn.OpReplace, Optional: true},
		{Role: RoleCertificate, Op: txn.OpReplace},
	}}
	clockHighWater = &txn.Decl{Name: TxClockHighWater, Kind: txn.Normal, Writes: []txn.Write{
		{Role: RoleClock, Op: txn.OpReplace},
	}}
)

// Transactions returns the declarations of the transactions this package opens
// itself. The composition root puts them in its table.
func Transactions() []*txn.Decl { return []*txn.Decl{certRenew, clockHighWater} }

// InitWrites returns the writes of init that belong to this package, in the
// order they are made, for the init transaction that its composer declares. Each
// is optional: init reuses every file that is already valid. The writes of the
// other owners come after them and InitMarkerWrite is the last write of all.
func InitWrites() []txn.Write {
	return []txn.Write{
		{Role: RoleIdentityKey, Op: txn.OpReplace, Optional: true},
		{Role: RoleNetworkKey, Op: txn.OpReplace, Optional: true},
		{Role: RoleSerial, Op: txn.OpReplace, Optional: true},
		{Role: RoleCertificate, Op: txn.OpReplace, Optional: true},
		{Role: RoleSelfRevocation, Op: txn.OpReplace, Optional: true},
		{Role: RoleClock, Op: txn.OpReplace, Optional: true},
	}
}

// InitMarkerWrite is the last write of init: the marker that says it finished.
func InitMarkerWrite() txn.Write { return txn.Write{Role: RoleInitMarker, Op: txn.OpReplace} }
