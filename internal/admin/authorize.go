package admin

import "time"

// Verdict is the result of Authorize.
type Verdict struct {
	Allowed bool
	Reason  Reason
	// Level is the actor's effective level when the actor is a remote person
	// with rights. It is LevelNone for the local owner (who has no list level)
	// and for anyone denied before a level could be determined.
	Level Level
}

// Authorize decides whether actor may perform op on this node at now.
//
// The local owner may perform every catalogued operation. A remote person is
// judged by EffectiveLevel against the operation's requirement; an operation
// reserved for the local owner is denied to every remote person, full control
// included. An operation outside the catalogue and an unknown actor are denied.
//
// Authorize decides one operation. A request that touches several settings is
// authorized once per operation it contains and applied only if all are
// allowed; the caller splits it, this function never sees the whole request.
func Authorize(list List, actor Actor, op Operation, now time.Time) Verdict {
	req, ok := RequirementOf(op)
	if !ok {
		return Verdict{Reason: ReasonUnknownOperation}
	}
	switch actor.Kind {
	case ActorLocalOwner:
		return Verdict{Allowed: true, Reason: ReasonLocalOwner}
	case ActorRemotePerson:
		return authorizeRemote(list, actor, req, now)
	default:
		return Verdict{Reason: ReasonUnknownActor}
	}
}

func authorizeRemote(list List, actor Actor, req Requirement, now time.Time) Verdict {
	// No list level satisfies a requirement that maps to LevelNone: the
	// local-owner-only requirement, and any requirement value this code does not
	// know. Denying both keeps a future requirement from being granted by
	// accident.
	need := req.level()
	if need == LevelNone {
		return Verdict{Reason: ReasonLocalOwnerOnly}
	}
	s := EffectiveLevel(list, actor.Person, actor.Cert, now)
	switch {
	case s.Reason != ReasonOK && s.Reason != ReasonDeviceCapTooLow:
		// Disqualified before a level could be determined.
		return Verdict{Reason: s.Reason}
	case s.Level >= need:
		return Verdict{Allowed: true, Reason: ReasonOK, Level: s.Level}
	case s.ListLevel >= need:
		return Verdict{Reason: ReasonDeviceCapTooLow, Level: s.Level}
	default:
		return Verdict{Reason: ReasonListLevelTooLow, Level: s.Level}
	}
}
