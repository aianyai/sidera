package store_test

import "testing"

// The tests below are questions the store cannot settle alone. Each is skipped
// with the question as the reason; the answer decides what the test asserts.

// The quarantined file of a delivery record whose first line cannot be read has
// no replacement, so nothing says when it may go but the time the node found it.
func TestHeaderUnreadableDeliveryQuarantineIsSweptAfterTheLostReportByBound(t *testing.T) {
	t.Skip("question: the bound of such a file is the retention of the latest report_by any delivery accepted before its discovery can have, a function the delivery package defines for lost records and that is not on the task branch; PlanSweep leaves these files where they are until it is, and this test asserts they go at that bound and not before")
}

// The drain of the records queued for a closed endpoint is the engine's
// decision, with the delivery rules; the store names candidates.
func TestDrainCandidatesAreExactlyTheRecordsAForcedCloseChanges(t *testing.T) {
	t.Skip("open question for the engine's plan: DrainCandidates names the accepted target and control records, never a lost replacement, whose endpoint is closed or has no record, and the engine decides whether that is the complete predicate of the endpoint-drain replay, that is, whether closing an endpoint forcibly changes a record in any other state or shape (a lost replacement, a record whose state is not accepted, a record under a live lease). This test compares the candidates with what the delivery package's forced close changes for every state of both sides, and can be written once that predicate is fixed")
}
