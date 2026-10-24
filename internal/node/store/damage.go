package store

import (
	"fmt"

	"github.com/aianyai/sidera/internal/node/recfile"
)

// The damage of a record file and the limits of its framing are those of
// recfile, which owns the format of the first line; they are named here because
// a decoded record's damage is part of what this package reports.
type (
	Damage       = recfile.Damage
	DamageClass  = recfile.DamageClass
	DamageReason = recfile.DamageReason
)

const (
	MaxHeaderLen = recfile.MaxHeaderLen
	MaxBodyLen   = recfile.MaxBodyLen

	HeaderUnreadable = recfile.HeaderUnreadable
	BodyDamaged      = recfile.BodyDamaged

	ReasonHeaderNoNewline  = recfile.ReasonHeaderNoNewline
	ReasonHeaderFields     = recfile.ReasonHeaderFields
	ReasonHeaderMagic      = recfile.ReasonHeaderMagic
	ReasonHeaderKind       = recfile.ReasonHeaderKind
	ReasonHeaderChecksum   = recfile.ReasonHeaderChecksum
	ReasonHeaderLength     = recfile.ReasonHeaderLength
	ReasonHeaderKey        = recfile.ReasonHeaderKey
	ReasonKindNotAllowed   = recfile.ReasonKindNotAllowed
	ReasonNameMismatch     = recfile.ReasonNameMismatch
	ReasonKeyInvalid       = recfile.ReasonKeyInvalid
	ReasonLengthMismatch   = recfile.ReasonLengthMismatch
	ReasonBodyTooLarge     = recfile.ReasonBodyTooLarge
	ReasonChecksumMismatch = recfile.ReasonChecksumMismatch
	ReasonBodyInvalid      = recfile.ReasonBodyInvalid
)

// DamagedFileError is the error of a write that had to read a record file and
// found it damaged. The write did nothing. File is what the scan would have
// listed under Damaged: the caller quarantines it by the usual rule and tries
// again. A damaged file is never read as if it were empty and overwritten, which
// would lose what it holds without an alert.
type DamagedFileError struct{ File DamagedFile }

func (e *DamagedFileError) Error() string {
	return fmt.Sprintf("store: %s/%s is damaged: %s", e.File.Dir, e.File.Name, e.File.Damage.Error())
}
