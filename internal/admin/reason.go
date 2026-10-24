package admin

// Reason says why a decision function returned the verdict it did. Reasons are
// stable machine-readable values: callers and tests branch on them and never
// parse text.
type Reason string

const (
	// ReasonOK is the reason of a successful check that has nothing more to say.
	ReasonOK Reason = "ok"
	// ReasonLocalOwner means the caller is the local owner, who may do anything.
	ReasonLocalOwner Reason = "local_owner"

	// Reasons why a remote person has no rights at all.

	// ReasonNotOnList means the person is not on this node's list. Rights on
	// other nodes are irrelevant.
	ReasonNotOnList Reason = "not_on_list"
	// ReasonEntryExpired means the person's list entry has expired.
	ReasonEntryExpired Reason = "entry_expired"
	// ReasonCertExpired means the device certificate is expired.
	ReasonCertExpired Reason = "cert_expired"
	// ReasonCertNotYetValid means the device certificate is not valid yet.
	ReasonCertNotYetValid Reason = "cert_not_yet_valid"
	// ReasonCertRevoked means the device certificate is revoked.
	ReasonCertRevoked Reason = "cert_revoked"
	// ReasonRootKeyRevoked means the person's root key is revoked.
	ReasonRootKeyRevoked Reason = "root_key_revoked"
	// ReasonRevocationUnchecked means nobody looked the device certificate and
	// the root key up in the revocation sets, so the certificate cannot be
	// trusted. It is a caller bug and is refused, never treated as unrevoked.
	ReasonRevocationUnchecked Reason = "revocation_unchecked"

	// Reasons why a person with rights may not do the requested operation.

	// ReasonListLevelTooLow means the level on this node's list is below the one
	// the operation requires.
	ReasonListLevelTooLow Reason = "list_level_too_low"
	// ReasonDeviceCapTooLow means the list level would suffice but the device
	// certificate caps the person below the level the operation requires.
	ReasonDeviceCapTooLow Reason = "device_cap_too_low"
	// ReasonLocalOwnerOnly means the operation is reserved for the local owner
	// and no remote person may do it, whatever their level.
	ReasonLocalOwnerOnly Reason = "local_owner_only"
	// ReasonUnknownOperation means the operation is not in the catalogue; it is
	// denied to everyone.
	ReasonUnknownOperation Reason = "unknown_operation"
	// ReasonUnknownActor means the actor is neither the local owner nor a
	// remote person.
	ReasonUnknownActor Reason = "unknown_actor"

	// Reasons for offline directives.

	// ReasonDirectiveNotForThisNode means the directive is addressed to another
	// node.
	ReasonDirectiveNotForThisNode Reason = "directive_not_for_this_node"
	// ReasonDirectiveKindNotAllowed means the directive asks for anything other
	// than revoking a trust edge.
	ReasonDirectiveKindNotAllowed Reason = "directive_kind_not_allowed"
	// ReasonDirectiveSignedAtMissing means the directive carries no signing
	// time.
	ReasonDirectiveSignedAtMissing Reason = "directive_signed_at_missing"
	// ReasonDirectiveSignedInFuture means the directive claims to be signed
	// later than now plus the clock tolerance.
	ReasonDirectiveSignedInFuture Reason = "directive_signed_in_future"
	// ReasonDirectiveSignedBeforeOffline means the directive claims to be signed
	// earlier than this node's recorded loss of contact minus the clock
	// tolerance, so it cannot be a directive issued because the node was
	// unreachable.
	ReasonDirectiveSignedBeforeOffline Reason = "directive_signed_before_offline"
	// ReasonOfflineSinceMissing means the caller did not say when this node lost
	// contact, which is a caller bug: without it the backdating check cannot
	// run, so the directive is refused.
	ReasonOfflineSinceMissing Reason = "offline_since_missing"

	// Reasons for reachability proofs.

	// ReasonProofNodeMismatch means the proof was issued for another node.
	ReasonProofNodeMismatch Reason = "proof_node_mismatch"
	// ReasonProofPersonMismatch means the proof was issued to another person.
	ReasonProofPersonMismatch Reason = "proof_person_mismatch"
	// ReasonProofExpired means the proof is no longer valid at the instant it is
	// presented.
	ReasonProofExpired Reason = "proof_expired"
	// ReasonProofOutlivesEntry means the requested proof would be valid for
	// longer than the person's list entry.
	ReasonProofOutlivesEntry Reason = "proof_outlives_entry"

	// Reasons for management requests.

	// ReasonMissingOperationID means a side-effecting request has no operation
	// ID.
	ReasonMissingOperationID Reason = "missing_operation_id"
	// ReasonMissingExpectedVersion means a configuration change carries no
	// expected version.
	ReasonMissingExpectedVersion Reason = "missing_expected_version"
	// ReasonVersionConflict means the expected version differs from the current
	// one; the change must not be applied.
	ReasonVersionConflict Reason = "version_conflict"
	// ReasonFirstSeen means the operation ID has no stored result.
	ReasonFirstSeen Reason = "first_seen"
	// ReasonSameRequest means the operation ID has a stored result for the same
	// request, which is returned instead of executing again.
	ReasonSameRequest Reason = "same_request"
	// ReasonOperationIDReused means the operation ID has a stored result for a
	// different request.
	ReasonOperationIDReused Reason = "operation_id_reused"
)
