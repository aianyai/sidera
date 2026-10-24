package keys

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

// Paths, relative to the data directory root.
const (
	Dir                = "keys"
	IdentityKeyPath    = "keys/identity.key"
	NetworkKeyPath     = "keys/network.key"
	CertificatePath    = "keys/network.cert"
	SerialPath         = "keys/network-serial"
	SelfRevocationPath = "keys/self-revocation.rec"
	ClockPath          = "keys/clock.rec"
	InitMarkerPath     = "keys/init.rec"
)

// Roles of the writes this package declares or offers to a composing package.
const (
	RoleIdentityKey    = "identity"
	RoleNetworkKey     = "network"
	RoleSerial         = "serial"
	RoleCertificate    = "cert"
	RoleSelfRevocation = "selfrev"
	RoleClock          = "clock"
	RoleInitMarker     = "marker"
)

// Names of the transactions this package declares.
const (
	TxCertRenew      = "cert-renew"
	TxClockHighWater = "clock-high-water"
)

// Decided parameters.
const (
	CertificateLifetime = 30 * 24 * time.Hour
	RenewBefore         = 10 * 24 * time.Hour
)

var errUnimplemented = errors.New("keys: unimplemented")

// Errors.
var (
	ErrPermissionsTooBroad = errors.New("permissions are too broad")
	ErrMissing             = errors.New("file is missing")
	ErrCorrupt             = errors.New("file is corrupt")
	ErrInconsistent        = errors.New("file contradicts the rest of the keys")
	ErrAlreadyInitialized  = errors.New("already initialized")
	ErrRandom              = errors.New("random source failed")
	ErrRollbackPaused      = errors.New("rollback pause is in force")
	ErrClockRolledBack     = errors.New("wall clock is behind the high water mark")
	ErrClockRegression     = errors.New("clock record would go back")
	ErrIdentityRetired     = errors.New("the identity of this node is retired")
)

// FileError names the file a failure is about.
type FileError struct {
	Path string
	Err  error
}

func (e *FileError) Error() string { return "keys: " + e.Path + ": " + e.Err.Error() }
func (e *FileError) Unwrap() error { return e.Err }

// Keys is everything loaded from keys/.
type Keys struct{}

func Load(w *txn.Writer) (*Keys, error)                                        { return nil, errUnimplemented }
func (k *Keys) Node() (n ident.NodeID)                                         { return }
func (k *Keys) NetworkSigner() record.Signer                                   { return nil }
func (k *Keys) Certificate() (c record.Verified[record.NetworkKeyCertificate]) { return }
func (k *Keys) CertificateWire() []byte                                        { return nil }
func (k *Keys) SerialSeen() (s record.SerialSeen)                              { return }
func (k *Keys) RenewalInterrupted() bool                                       { return false }
func (k *Keys) RenewalDue(now time.Time) (RenewVerdict, RenewReason) {
	return 0, 0
}
func (k *Keys) Renew(now time.Time) error                 { return errUnimplemented }
func (k *Keys) SaveClock(s ClockState) error              { return errUnimplemented }
func (k *Keys) CheckNotRetired(set *revocation.Set) error { return errUnimplemented }

// Renewal.
type RenewVerdict uint8
type RenewReason uint8

const (
	RenewNotDue RenewVerdict = iota + 1
	RenewDue
)

const (
	RenewReasonCurrent RenewReason = iota + 1
	RenewReasonInterrupted
	RenewReasonExpiring
)

func RenewalDue(cert record.NetworkKeyCertificate, interrupted bool, now time.Time) (RenewVerdict, RenewReason) {
	return 0, 0
}

func RenewalInterrupted(seen record.SerialSeen, cert record.Verified[record.NetworkKeyCertificate]) bool {
	return false
}

// Recovery work.
type WorkReason uint8

const WorkRenewalInterrupted WorkReason = iota + 1

type Work struct {
	Reason WorkReason
	Path   string
}

func RecoveryWork(r fsx.Reader) ([]Work, error) { return nil, errUnimplemented }

// Clock.
type ClockState struct {
	HighWater           time.Time
	RollbackPausedSince time.Time
}

func (c ClockState) Paused() bool                     { return !c.RollbackPausedSince.IsZero() }
func ReadClock(r fsx.Reader) (ClockState, error)      { return ClockState{}, errUnimplemented }
func PutClockCleared(tx *txn.Tx, now time.Time) error { return errUnimplemented }

// Self-revocation.
type SelfRevocation struct {
	Wire   []byte
	Digest record.Digest
	Node   ident.NodeID
}

func ReadSelfRevocation(r fsx.Reader) (SelfRevocation, error) {
	return SelfRevocation{}, errUnimplemented
}
func FormatRevocationExport(sr SelfRevocation) ([]byte, record.Digest) { return nil, record.Digest{} }

// Init.
type InitPlan struct{}

func Initialized(r fsx.Reader) (bool, error) { return false, errUnimplemented }
func CheckPermissions(r fsx.Reader) error    { return errUnimplemented }
func PlanInit(r fsx.Reader, rand io.Reader, now time.Time) (*InitPlan, error) {
	return nil, errUnimplemented
}
func (p *InitPlan) Node() (n ident.NodeID)         { return }
func (p *InitPlan) SelfRevocation() SelfRevocation { return SelfRevocation{} }
func (p *InitPlan) WriteKeys(tx *txn.Tx) error     { return errUnimplemented }
func (p *InitPlan) WriteMarker(tx *txn.Tx) error   { return errUnimplemented }

// Declarations.
var (
	certRenew = &txn.Decl{Name: TxCertRenew, Kind: txn.Normal, Writes: []txn.Write{
		{Role: RoleSerial, Op: txn.OpReplace},
		{Role: RoleCertificate, Op: txn.OpReplace},
	}}
	clockHighWater = &txn.Decl{Name: TxClockHighWater, Kind: txn.Normal, Writes: []txn.Write{
		{Role: RoleClock, Op: txn.OpReplace},
	}}
)

func Transactions() []*txn.Decl { return []*txn.Decl{certRenew, clockHighWater} }
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
func InitMarkerWrite() txn.Write { return txn.Write{Role: RoleInitMarker, Op: txn.OpReplace} }

// Codecs of the files of this package.
const (
	kindIdentityKey = "identity-key"
	kindNetworkKey  = "network-key"
)

func encodeKeyFile(kind string, seed [32]byte) []byte { return nil }
func decodeKeyFile(kind string, data []byte) ([32]byte, error) {
	return [32]byte{}, errUnimplemented
}
func encodeClock(c ClockState) []byte             { return nil }
func decodeClock(data []byte) (ClockState, error) { return ClockState{}, errUnimplemented }
func encodeSerial(s record.SerialSeen) []byte     { return nil }
func decodeSerial(data []byte) (record.SerialSeen, error) {
	return record.SerialSeen{}, errUnimplemented
}
func encodeInit(t time.Time) []byte             { return nil }
func decodeInit(data []byte) (time.Time, error) { return time.Time{}, errUnimplemented }
