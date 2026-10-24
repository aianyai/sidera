package store

import "github.com/aianyai/sidera/internal/node/recfile"

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
