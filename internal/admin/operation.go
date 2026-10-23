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
// alone. The second result is false for an operation that is not in the
// catalogue.
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
	case OpConfirmTrust, OpEnableInheritance, OpExtendGracePeriod,
		OpClearLocallyRevoked, OpInstallAdapter:
		return RequireFullControl, true
	case OpModifyAuthorizedList:
		return RequireLocalOwner, true
	default:
		return 0, false
	}
}

// SideEffecting reports whether the operation changes state. Every
// side-effecting management request carries an operation ID.
func (op Operation) SideEffecting() bool {
	return op.Kind != OpRead && op.Kind != OpQueryOperationResult && op.Kind != OpUnknown
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
		OpEditDeclaration, OpChangeRelaySettings, OpSetLANDiscovery:
		return true
	default:
		return false
	}
}
