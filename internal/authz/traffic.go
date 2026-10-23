package authz

// TrafficKind classifies what travels between nodes, so that the callers agree
// on which traffic the endpoint policy governs.
type TrafficKind uint8

// The kinds of traffic.
const (
	// TrafficMessage is an ordinary message to an endpoint (message.send).
	TrafficMessage TrafficKind = iota + 1
	// TrafficControlSignal is a request to act on a session or runtime
	// (session.create, session.close, session.interrupt, endpoint.status).
	TrafficControlSignal
	// TrafficStatusReport is a receipt or status correction for a message.
	TrafficStatusReport
	// TrafficControlReply is the outcome of a control signal.
	TrafficControlReply
	// TrafficSystemFeedback is a feedback message from a node's system endpoint
	// to a local endpoint.
	TrafficSystemFeedback
	// TrafficTrustMaintenance covers certificate exchange, trust graph sync,
	// online guarantees, offline admin orders, revocation sync and requests to
	// establish trust.
	TrafficTrustMaintenance
)

// PolicyExempt reports whether endpoint policy leaves this kind of traffic
// alone. Exempt traffic is what the channel needs in order to do its job, so it
// is limited by resource limits only. The unknown kind is not exempt.
//
// Exemption does not drop the caller's own preconditions: a status report must
// match an outbound record the receiver still holds and come from the original
// target node, and a control reply must match the original control signal.
func (k TrafficKind) PolicyExempt() bool {
	switch k {
	case TrafficStatusReport, TrafficControlReply, TrafficSystemFeedback, TrafficTrustMaintenance:
		return true
	}
	return false
}
