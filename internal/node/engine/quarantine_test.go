package engine_test

import "testing"

// A control record that is damaged is quarantined, and the quarantine closes the
// session endpoints it left being created. A control signal that failed to be
// accepted leaves such an endpoint, and the signal accepted again makes another
// under the same record name: when that record is damaged in turn, both are
// the endpoints of the quarantined record, and both are closed by the same
// transaction, not only the first in the order of the scan.
//
// The store decides which endpoints a control record's quarantine closes
// (store.QuarantineSessions), from the scan the engine hands it, so the engine
// passes no list. The quarantine of damaged files comes with the formation of the
// replacement records of lost deliveries (the scan entry refuses to open over a
// damaged file until then). This test is the requirement kept in sight until the
// engine wires the quarantine; it is skipped until then and must not be removed.
func TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord(t *testing.T) {
	t.Skip("pending: the quarantine of damaged files is wired with the replacement records of lost deliveries; " +
		"the store picks the session endpoints to close from the scan it is given, so wiring it means giving " +
		"store.Quarantine and store.QuarantineResume that scan")
}
