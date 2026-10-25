package engine_test

import "testing"

// A control record that is damaged is quarantined, and the quarantine closes the
// session endpoints it left being created. A control signal that failed to be
// accepted leaves such an endpoint, and the signal accepted again makes another
// under the same record name: when that record is damaged in turn, both are
// the endpoints of the quarantined record, and both are closed by the same
// transaction, not only the first in the order of the scan.
//
// The quarantine of damaged files comes with the formation of the replacement
// records of lost deliveries (the scan entry refuses to open over a damaged
// file until then), and the transaction of the store takes one session
// endpoint. This test is the requirement kept in sight until the path is wired; it is skipped until then and must not be removed.
func TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord(t *testing.T) {
	t.Skip("pending: the quarantine of damaged files is wired with the replacement records of lost deliveries; " +
		"store.Quarantine and store.QuarantineResume take a single *EndpointRecord, so closing every creating " +
		"endpoint whose created_by names the record needs the store transaction to take a list first")
}
