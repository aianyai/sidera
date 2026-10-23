package record

import "testing"

// Open questions: each is a decision that changes signed bytes and was settled
// by the implementer because the written rules do not state it. They are
// skipped so that they stay visible; if the answer differs, the golden vectors
// change with it.

func TestQuestionDoesTheNetworkKeyCertificateHaveANotBefore(t *testing.T) {
	t.Skip("settled as two required fields, not_before and not_after, like the device certificate; " +
		"the alternative is an expiry only, which drops field 4 and changes the certificate bytes")
}

func TestQuestionAreTheIdentifierDerivationDomainsLengthPrefixed(t *testing.T) {
	t.Skip("settled as the fixed strings \"sidera/id/node/1\" and \"sidera/id/person/1\" followed by the " +
		"algorithm byte and the key, without a length prefix; changing either changes every identifier")
}
