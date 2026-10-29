package recipes

import (
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

// feedbackDrain retains one phase's two minutes across interrupted drains.
// Pulls run once a minute and feedback merges for a minute: the first step lets
// the source learn a final result, and the second makes its notice readable.
type feedbackDrain struct {
	completedSteps int
	advanceIssued  bool
}

func (d *feedbackDrain) run(e scenario.Env, facts *DeliveryEvidence, sender string) error {
	for d.completedSteps < 2 {
		if !d.advanceIssued {
			// Advancing selects the new logical time before its resulting writes
			// can be cut, even if the call has not returned when the process dies.
			d.advanceIssued = true
			e.Advance(time.Minute)
		}
		if err := e.Quiesce(); err != nil {
			return err
		}
		d.completedSteps++
		d.advanceIssued = false
	}
	// Completion of the timed steps does not complete receipt: receive or ack
	// may itself have been interrupted, so always resume the real agent calls.
	return facts.RecvAndAck(e, e.Agent(sender))
}
