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
// passes no list. The quarantine of a damaged control record is not wired: it
// removes the record's file, and a control signal's record must not go before the
// pull of control signals answers from it, so the scan entry refuses to open over one
// (TestOpenRefusesToOpenOverWorkOfARuleThatIsNotWiredYet). This test is the
// requirement kept in sight until the engine wires it; it is skipped until then and
// must not be removed.
func TestQuarantineClosesEveryCreatingSessionEndpointOfTheDamagedControlRecord(t *testing.T) {
	t.Skip("pending: the quarantine of a damaged control record is wired with the pull of control signals, which answers " +
		"from ctl/; the store picks the session endpoints to close from the scan it is given, so wiring it means giving " +
		"store.Quarantine that scan")
}
