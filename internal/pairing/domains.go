package pairing

import "github.com/aianyai/sidera/internal/frame"

// Domains of the hashes and key derivations of this package. Each purpose has
// its own, so a value made for one can never be taken for another. The first
// four are the construction of the transcript: they frame the same field list,
// so a hash or derived key of one purpose is never equal to one of another.
var (
	// commitDomain frames the commitment input: the terms and the initiator's
	// nonce.
	commitDomain = frame.Domain("sidera/pairing/commit/1")
	// transcriptDomain frames the whole transcript for the pairing identifier.
	transcriptDomain = frame.Domain("sidera/pairing/transcript/1")
	// codeDomain frames the HKDF info from which the verification code is drawn.
	codeDomain = frame.Domain("sidera/pairing/sas/1")
	// nonceDomain frames the HKDF info from which the credential nonce is drawn.
	nonceDomain = frame.Domain("sidera/pairing/credential-nonce/1")
	// requestDomain frames the fingerprint of a management request about a
	// pairing.
	requestDomain = frame.Domain("sidera/pairing/request/1")
)
