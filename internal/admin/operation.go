package admin

import "github.com/aianyai/sidera/internal/authz"

// OpKind names an administrative operation.
type OpKind int

const (
	// OpUnknown is the zero value. It is in no catalogue and is denied to
	// everyone.
	OpUnknown OpKind = iota

	// OpRead reads state, policy, endpoints, logs and diagnostics.
	OpRead
	// OpQueryOperationResult reads the stored result of an operation the
	// requester sent earlier. It changes nothing. The caller looks the result up
	// under KeyFor(requester, id), so a person can only ever see their own
	// results.
	OpQueryOperationResult

	// Operations that only withdraw or tighten.

	// OpRevokeTrust revokes a trust credential.
	OpRevokeTrust
	// OpLocallyRevokeNode removes one node from this node's trust and records
	// the intent so inheritance cannot bring it back.
	OpLocallyRevokeNode
	// OpDisableInheritance turns off acceptance of inherited trust.
	OpDisableInheritance
	// OpShortenGracePeriod lowers the online-voucher grace period.
	OpShortenGracePeriod
	// OpRejectTrustRequest rejects a pending request to establish bidirectional
	// trust. It only declines to let a node in, so it is withdrawing, not
	// admitting; it is a one-off decision about one request, not an edit of a
	// setting.
	OpRejectTrustRequest

	// Day-to-day management that adds no code and lets no node in.

	// OpManageEndpointSession manages the lifecycle of endpoints and sessions,
	// choosing only among adapters and presets already approved on this node.
	OpManageEndpointSession
	// OpEditDeclaration edits only those declaration fields that have no
	// operation of their own. The inheritance switch (OpEnableInheritance,
	// OpDisableInheritance) and the relay settings (OpChangeRelaySettings) are
	// excluded: changing one of them through the declaration is authorized as
	// that operation, never as this one.
	OpEditDeclaration
	// OpChangeRelaySettings changes whether and for whom this node relays.
	OpChangeRelaySettings
	// OpSetLANDiscovery switches LAN auto-discovery on or off. Discovery only
	// finds candidates and admits no one, so both directions need manage.
	OpSetLANDiscovery
	// OpIssueOfflineDirective signs an offline directive that revokes a trust
	// edge of a node that is currently offline. The level it needs is enforced
	// by AcceptDirective when the node executes the directive, which reads it
	// from this catalogue entry.
	OpIssueOfflineDirective

	// OpChangePolicy changes an access or endpoint policy. Whether it is
	// permitted depends on Operation.PolicyChange.
	OpChangePolicy

	// Operations that let more nodes in or back, run more code, or loosen
	// internal policy.

	// OpConfirmTrust confirms a pending request to establish bidirectional
	// trust.
	OpConfirmTrust
	// OpEnableInheritance turns on acceptance of inherited trust.
	OpEnableInheritance
	// OpExtendGracePeriod raises the online-voucher grace period.
	OpExtendGracePeriod
	// OpClearLocallyRevoked clears a node from the locally-revoked record.
	OpClearLocallyRevoked
	// OpInstallAdapter installs or upgrades an adapter package.
	OpInstallAdapter
	// OpChangePreset changes the approved session presets (working directory,
	// sandbox, permission mode, usable tools). Whether it is permitted depends
	// on Operation.PresetChange: removing or narrowing a preset needs manage;
	// adding or loosening one needs full control.
	OpChangePreset

	// OpModifyAuthorizedList changes the authorized-persons list. Only the
	// local owner may.
	OpModifyAuthorizedList

	// Operations for managing a single node. Each is held to the local owner
	// until remote management exists: a level that cannot yet be exercised
	// remotely cannot be validated, and loosening one later is a one-line,
	// tested change of RequirementOf.

	// OpEnableAdapter enables an installed adapter instance, which makes this
	// machine run its code. Kept apart from OpDisableAdapter because enabling
	// adds code and disabling only withdraws, so a remote level would differ.
	OpEnableAdapter
	// OpDisableAdapter disables an adapter instance.
	OpDisableAdapter
	// OpChangeEndpointSettings changes the settings of one endpoint, such as its
	// backlog and longest queueing time. It is not OpManageEndpointSession,
	// which only manages lifecycles among what is already approved.
	OpChangeEndpointSettings
	// OpChangeNodeSettings changes the node-wide settings.
	OpChangeNodeSettings
	// OpAcknowledgeAlert acknowledges an alert raised to the owner.
	OpAcknowledgeAlert
	// OpClearClockRollback clears the pause that a detected rollback of the
	// wall clock put on signing.
	OpClearClockRollback
	// OpConfirmRevocationBackup records that the owner has stored the
	// revocation statement safely.
	OpConfirmRevocationBackup
	// OpRetireIdentity retires this node's identity for good.
	OpRetireIdentity
	// OpExportRevocation returns the revocation statement, which can void this
	// node's identity. It is a read that needs the local owner, not view: it
	// changes nothing, and its result must never be stored.
	OpExportRevocation
	// OpAttachSession attaches a session a person opened to this node: it
	// registers the session as an endpoint, or finds the endpoint it already
	// has, and hands the session its token. It changes state, but a request
	// repeats as often as the session wants to be found again and means the
	// same each time, so it carries no operation ID and stores no result. The
	// reply carries a token, so the local owner alone may ask.
	OpAttachSession
	// OpReadAdapterSetup reads what the core CLI needs to run the client setup of an
	// adapter instance: the instance's configuration, which may hold a secret, and
	// the program the installed package declares for it. It is a read that needs the
	// local owner, not view: it changes nothing and carries no operation ID.
	OpReadAdapterSetup
	// OpStopDaemon asks the running daemon to shut down normally.
	OpStopDaemon
	// OpRestartDaemon asks the running daemon to shut down normally and start the
	// program again in its place.
	OpRestartDaemon

	// opEnd is the end sentinel: it is one past the last operation in the
	// catalogue and is itself in none. Tests iterate every kind below it and
	// use it as an out-of-catalogue sample, so a kind added above it is covered
	// without touching them. Add every new operation above it.
	opEnd
)

// PresetChangeClass is the effect of a preset change, classified by the caller
// from the preset before and after. It has the same shape as authz.Change:
// the zero value is not a classification.
type PresetChangeClass int

const (
	// PresetChangeUnclassified is the zero value: the change could not be
	// classified. It is treated as loosening, like an unset policy
	// classification.
	PresetChangeUnclassified PresetChangeClass = iota
	// PresetChangeNarrowing removes a preset or narrows an existing one.
	PresetChangeNarrowing
	// PresetChangeLoosening adds a preset or widens an existing one.
	PresetChangeLoosening
)

// Operation is an administrative operation with the facts the required level
// depends on.
type Operation struct {
	Kind OpKind
	// PolicyChange is the policy package's classification of the change, for
	// OpChangePolicy; it is ignored for every other kind. Its zero value is not
	// a classification, so an operation that leaves it unset needs full control.
	PolicyChange authz.Change
	// PresetChange classifies the change for OpChangePreset and is ignored for
	// every other kind.
	PresetChange PresetChangeClass
	// ReplacesEnabled marks an OpInstallAdapter that replaces the package of an
	// adapter with an enabled instance, so the instance restarts on the new
	// program; it is ignored for every other kind. Only the installation itself
	// knows it, so the interface authorizes the request without it and the
	// installation authorizes again with it.
	ReplacesEnabled bool
}

// Requirement is who may perform an operation.
type Requirement int

const (
	// RequireView is satisfied by view and above, and by the local owner.
	RequireView Requirement = iota + 1
	// RequireManage is satisfied by manage and above, and by the local owner.
	RequireManage
	// RequireFullControl is satisfied by full-control and by the local owner.
	RequireFullControl
	// RequireLocalOwner is satisfied by the local owner alone.
	RequireLocalOwner
)

// level returns the lowest list level that satisfies r, or LevelNone for
// RequireLocalOwner, which no list level satisfies.
func (r Requirement) level() Level {
	switch r {
	case RequireView:
		return LevelView
	case RequireManage:
		return LevelManage
	case RequireFullControl:
		return LevelFullControl
	default:
		return LevelNone
	}
}

// RequirementOf returns who may perform op. The one criterion: operations that
// let more nodes in or back, let this machine run more code, or loosen internal
// policy need full control; operations that only tighten or withdraw need
// manage; reading needs view; the authorized-persons list is the local owner's
// alone, as are the operations for managing a single node. The second result
// is false for an operation that is not in the catalogue.
func RequirementOf(op Operation) (Requirement, bool) {
	switch op.Kind {
	case OpRead, OpQueryOperationResult:
		return RequireView, true
	case OpRevokeTrust, OpLocallyRevokeNode, OpRejectTrustRequest,
		OpDisableInheritance, OpShortenGracePeriod,
		OpManageEndpointSession, OpEditDeclaration, OpChangeRelaySettings,
		OpSetLANDiscovery, OpIssueOfflineDirective:
		return RequireManage, true
	case OpChangePolicy:
		// Only a change known not to loosen is manage: Loosen, an unset
		// classification and any value out of range need full control.
		if op.PolicyChange == authz.Unchanged || op.PolicyChange == authz.Tighten {
			return RequireManage, true
		}
		return RequireFullControl, true
	case OpChangePreset:
		if op.PresetChange == PresetChangeNarrowing {
			return RequireManage, true
		}
		return RequireFullControl, true
	case OpInstallAdapter:
		// Replacing the package of an enabled instance runs new code on a
		// machine whose owner enabled the old: that is what enabling asks the
		// local owner for.
		if op.ReplacesEnabled {
			return RequireLocalOwner, true
		}
		return RequireFullControl, true
	case OpConfirmTrust, OpEnableInheritance, OpExtendGracePeriod,
		OpClearLocallyRevoked:
		return RequireFullControl, true
	case OpModifyAuthorizedList,
		OpEnableAdapter, OpDisableAdapter, OpChangeEndpointSettings,
		OpChangeNodeSettings, OpAcknowledgeAlert, OpClearClockRollback,
		OpConfirmRevocationBackup, OpRetireIdentity, OpExportRevocation,
		OpAttachSession, OpReadAdapterSetup, OpStopDaemon, OpRestartDaemon:
		return RequireLocalOwner, true
	default:
		return 0, false
	}
}

// ChangesState reports whether the operation changes state. A reading, the
// query of a stored result, the export of the revocation statement and the read of
// an instance's setup do not: the last two are reads that need the local owner.
func (op Operation) ChangesState() bool {
	switch op.Kind {
	case OpRead, OpQueryOperationResult, OpUnknown, OpExportRevocation, OpReadAdapterSetup:
		return false
	default:
		return true
	}
}

// ReplayProtected reports whether the operation is guarded against being carried
// out twice by one request: such a management request carries an operation ID,
// and its result is stored with the request's fingerprint. Every operation that
// changes state is, but OpAttachSession: it is idempotent by its own key and is
// repeated by design, so a stored result per repeat would only fill the store,
// and a repeat must be able to update the state it changed. What does not change
// state has nothing to guard, and the result of the revocation export must never
// be stored (nor the instance configuration the setup read returns).
func (op Operation) ReplayProtected() bool {
	return op.ChangesState() && op.Kind != OpAttachSession
}

// IsConfigChange reports whether the operation changes a versioned setting.
// Such requests carry the version the sender last saw, so a concurrent change
// is reported as a conflict instead of being overwritten. Operations that move
// an individual trust edge or act on a session are state transitions, not
// edits of a setting, and carry no version.
func (op Operation) IsConfigChange() bool {
	switch op.Kind {
	case OpEnableInheritance, OpDisableInheritance,
		OpExtendGracePeriod, OpShortenGracePeriod,
		OpChangePolicy, OpChangePreset,
		OpEditDeclaration, OpChangeRelaySettings, OpSetLANDiscovery,
		OpEnableAdapter, OpDisableAdapter,
		OpChangeEndpointSettings, OpChangeNodeSettings:
		return true
	default:
		return false
	}
}
