package authz

// LocalRequest is a request from an endpoint on this node, as only the loopback
// transport can deliver it. A remote connection that presents this node's ID is
// not a local source: its request goes through Evaluate. A LocalRequest carries
// no trust, no policy and no preset: node rules do not apply to local sources,
// and the preset of a session.create is judged by LocalCreate from the preset's
// own statement. There is no body size either: the envelope's global limit is the
// only size limit a local source meets.
type LocalRequest struct {
	// Stage says which check this is, as for Request.
	Stage Stage

	Sender Sender
	Action Action
	Target Target

	// MultiRecipient is true when the message is addressed to several
	// recipients (message.send only), as for Request.MultiRecipient: read at
	// StageIntake only.
	MultiRecipient bool
	// Reply is the caller's verified fact that this message is a reply inside an
	// outstanding reply permission, as for Request.Reply; at StageRecv the value
	// stored at intake.
	Reply bool
	// TargetDeclaration is the target endpoint's self-declaration, looked up by
	// the caller. The zero value declares nothing.
	TargetDeclaration Declaration
}

// EvaluateLocal decides one request from an endpoint on this node. Local
// sources are judged by the target's own declarations and by resource limits,
// never by node rules, so there is no policy and no trust to read: the request
// shape and the declarations are checked as Evaluate checks them, and nothing
// else. The one addition is that the sender names an endpoint: every local
// source is an endpoint, so a zero sender endpoint is malformed here, though
// Evaluate accepts it for a node-level request from another node. The verdict is
// never Suspend, since nothing here can lapse, and never carries obligations,
// since only node rules create them. A verified reply is read as in Evaluate: it
// lifts the sender list and nothing else (the declarations read it for
// message.send only).
func EvaluateLocal(r LocalRequest) Verdict {
	a := r.asked()
	if r.Sender.Endpoint.IsZero() || !a.wellFormed() {
		return verdictFor(ReasonMalformedRequest)
	}
	if reason, declined := a.declinedBy(r.Reply); declined {
		return verdictFor(reason)
	}
	return verdictFor(ReasonGranted)
}

// PresetLocalAccess is a preset's statement about local sources: whether an
// endpoint on this node may start a session with it. The zero value is not a
// statement. The preset format refuses a preset that makes none, and this package
// refuses it too, so a preset whose statement went missing is never read as open.
type PresetLocalAccess uint8

// The two statements a preset can make.
const (
	// PresetLocalAllow: local sources may start a session with the preset.
	PresetLocalAllow PresetLocalAccess = iota + 1
	// PresetLocalDeny: local sources may not. The owner declares a preset this
	// way when an agent on the node must not be able to use it, for example a
	// preset without a sandbox.
	PresetLocalDeny
)

// LocalCreate decides whether a local source may start a session with a preset
// that makes this statement. It is the only thing that limits a local
// session.create beyond the target's declarations: the statement belongs to the
// preset, not to a node rule, so it holds for local sources although node rules
// do not. The caller has already checked that the preset exists.
//
// A local session.create proceeds only if both EvaluateLocal (at intake) and
// LocalCreate (after the control record is accepted, before the adapter is
// called) allow it. Any other verdict of LocalCreate stops the creation and is
// answered with adapterreg.DaemonCreateFailure(DaemonCreatePresetClosedToLocalSources);
// it is never passed to delivery.IntakeAuthorization. The zero statement cannot
// reach LocalCreate because the preset format refuses it, so a malformed verdict
// is a bug, answered the same way (fail closed) and logged.
func LocalCreate(a PresetLocalAccess) Verdict {
	switch a {
	case PresetLocalAllow:
		return verdictFor(ReasonGranted)
	case PresetLocalDeny:
		return verdictFor(ReasonPresetClosedToLocalSources)
	}
	return verdictFor(ReasonMalformedRequest)
}
