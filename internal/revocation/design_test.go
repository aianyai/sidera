package revocation

import "testing"

// Question for the owner of the synchronisation protocol, not decided here: what
// is the set digest two peers compare when they connect? Choosing it fixes new
// wire bytes, so it is not chosen by this package. The choices to make are:
//
//   - the digest domain and the framed fields (record digests sorted and
//     deduplicated, or a structure that lets a mismatch be narrowed down);
//   - which revocations a digest covers: person and device revocations are the
//     same for every peer, but node-level revocations are synchronised only for
//     the part that concerns the peer, so their digest depends on the peer's
//     trust graph and cached declarations, which the peer must be able to
//     reproduce without seeing them;
//   - the digest of the empty set.
//
// Once those are settled the function belongs here, next to Set, and takes the
// record digests that Revocation.Digest returns.
func TestSetDigestForSynchronisation(t *testing.T) {
	t.Skip("open question: the set digest for synchronisation fixes wire bytes and the scope of node-level revocations; see the comment above")
}
