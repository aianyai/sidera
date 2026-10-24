package store_test

import "testing"

// The tests below are questions the store cannot settle alone. Each is skipped
// with the question as the reason; the answer decides what the test asserts.

// A delivery record read from disk is checked by the delivery package, not by
// the store: the consistency of state, substate, reason and lease is its rule.
func TestStoreRefusesAnInvalidDecodedRecord(t *testing.T) {
	t.Skip("delivery.Record.Valid is not on the task branch yet; once it is, DecodeRecord must call it on every delivery record of an out, in and ctl body and treat a failure as a damaged body, and this test sets a record that Valid refuses and expects a body damage")
}

// The quarantined file of a delivery record whose first line cannot be read has
// no replacement, so nothing says when it may go but the time the node found it.
func TestHeaderUnreadableDeliveryQuarantineIsSweptAfterTheLostReportByBound(t *testing.T) {
	t.Skip("question: the bound of such a file is the retention of the latest report_by any delivery accepted before its discovery can have, a function the delivery package defines for lost records and that is not on the task branch; PlanSweep leaves these files where they are until it is, and this test asserts they go at that bound and not before")
}

// The drain of the records queued for a closed endpoint is the engine's
// decision, with the delivery rules; the store names candidates.
func TestDrainCandidatesAreExactlyTheRecordsAForcedCloseChanges(t *testing.T) {
	t.Skip("question: is the store's candidate (an accepted, not lost, target or control record whose endpoint is closed or has no record) the whole predicate of the drain replay, or does a forced close also change records in other states once the lost-record rules land? The engine's plan decides; this test would compare the candidates with the delivery package's answer for every state of both sides")
}
