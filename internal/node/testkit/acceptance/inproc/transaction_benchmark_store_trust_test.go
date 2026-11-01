package inproc

import (
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/pairing"
)

// The trust, peer and pairing records are each one write of one file (a
// removal also syncs the directory) through the store's own method. A sample
// seeds what the method needs, calls it once, and reads the record back from
// disk by a scan.

func transactionBenchmarkStoreTrustCases() []transactionBenchmarkCase {
	seedTarget := func(st *store.Store, r transactionBenchmarkPairingRecords) error {
		return st.PairingTargetPut(r.target)
	}
	seedInitiator := func(st *store.Store, r transactionBenchmarkPairingRecords) error {
		return st.PairingInitiatorPut(r.initiator)
	}
	return []transactionBenchmarkCase{
		{name: "trust-credential", prepare: prepareTransactionStoreTrustCredential},
		{name: "trust-revocation", prepare: prepareTransactionStoreTrustRevocation},
		{name: "peer-cert", prepare: prepareTransactionStorePeerCert},
		transactionBenchmarkStorePairingCase("pairing-target-put", nil,
			func(st *store.Store, r transactionBenchmarkPairingRecords) error {
				return st.PairingTargetPut(r.target)
			},
			func(sc *store.Scan, r transactionBenchmarkPairingRecords) bool {
				return len(sc.PairingTargets) == 1 && sc.PairingTargets[0].ID() == r.target.ID()
			}),
		transactionBenchmarkStorePairingCase("pairing-target-delete", seedTarget,
			func(st *store.Store, r transactionBenchmarkPairingRecords) error {
				return st.PairingTargetDelete(r.target.ID())
			},
			func(sc *store.Scan, _ transactionBenchmarkPairingRecords) bool { return len(sc.PairingTargets) == 0 }),
		transactionBenchmarkStorePairingCase("pairing-initiator-put", nil,
			func(st *store.Store, r transactionBenchmarkPairingRecords) error {
				return st.PairingInitiatorPut(r.initiator)
			},
			func(sc *store.Scan, r transactionBenchmarkPairingRecords) bool {
				return len(sc.PairingInitiators) == 1 && sc.PairingInitiators[0].ID() == r.initiator.ID()
			}),
		transactionBenchmarkStorePairingCase("pairing-initiator-delete", seedInitiator,
			func(st *store.Store, r transactionBenchmarkPairingRecords) error {
				return st.PairingInitiatorDelete(r.initiator.ID())
			},
			func(sc *store.Scan, _ transactionBenchmarkPairingRecords) bool { return len(sc.PairingInitiators) == 0 }),
		transactionBenchmarkStorePairingCase("pairing-limits", nil,
			func(st *store.Store, r transactionBenchmarkPairingRecords) error {
				return st.PairingLimitsPut(r.limits)
			},
			func(sc *store.Scan, r transactionBenchmarkPairingRecords) bool {
				return len(sc.PairingLimits.Reveals()) == len(r.limits.Reveals()) && len(sc.PairingLimits.Failures()) == len(r.limits.Failures())
			}),
		transactionBenchmarkStorePairingCase("pairing-expectations", nil,
			func(st *store.Store, r transactionBenchmarkPairingRecords) error {
				return st.PairingExpectationsPut(r.expectations)
			},
			func(sc *store.Scan, r transactionBenchmarkPairingRecords) bool {
				return len(sc.PairingExpectations) == 1 && sc.PairingExpectations[0].Node == r.expectations[0].Node
			}),
	}
}

// transactionBenchmarkScan reads the store back; a damaged or unknown file
// means the fixture did not leave what it meant to.
func transactionBenchmarkScan(st *store.Store) (*store.Scan, error) {
	sc, err := st.Scan()
	if err != nil {
		return nil, err
	}
	if len(sc.Damaged) != 0 || len(sc.Unknown) != 0 {
		return nil, fmt.Errorf("unexpected damaged or unknown files: %+v %+v", sc.Damaged, sc.Unknown)
	}
	return sc, nil
}

func prepareTransactionStoreTrustCredential(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	_, st := newTransactionMaintenanceStore(b, observer)
	local, remote := newTransactionBenchmarkPeer(b, 1), newTransactionBenchmarkPeer(b, 2)
	cred, peer := transactionBenchmarkCredential(b, local.identity, remote.identity), remote.peerRecord(b)
	return transactionBenchmarkFixture{
		invoke: func() error { return st.TrustCredential(cred, &peer) },
		verify: func() error {
			sc, err := transactionBenchmarkScan(st)
			if err == nil && (len(sc.Credentials) != 1 || sc.Credentials[0].Digest != cred.Digest || len(sc.Peers) != 1 || sc.Peers[0].Node != peer.Node) {
				return fmt.Errorf("the credential and the peer certificate were not both persisted")
			}
			return err
		},
	}
}

func prepareTransactionStoreTrustRevocation(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	_, st := newTransactionMaintenanceStore(b, observer)
	local, remote := newTransactionBenchmarkPeer(b, 1), newTransactionBenchmarkPeer(b, 2)
	revocation := transactionBenchmarkRevocation(b, transactionBenchmarkCredential(b, local.identity, remote.identity), local.identity)
	return transactionBenchmarkFixture{
		invoke: func() error { return st.TrustRevocation(revocation) },
		verify: func() error {
			sc, err := transactionBenchmarkScan(st)
			if err == nil && (len(sc.CredRevocations) != 1 || sc.CredRevocations[0].Revoked != revocation.Revoked) {
				return fmt.Errorf("the revocation was not persisted")
			}
			return err
		},
	}
}

func prepareTransactionStorePeerCert(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	_, st := newTransactionMaintenanceStore(b, observer)
	peer := newTransactionBenchmarkPeer(b, 2).peerRecord(b)
	return transactionBenchmarkFixture{
		invoke: func() error { return st.PeerCert(peer) },
		verify: func() error {
			sc, err := transactionBenchmarkScan(st)
			if err == nil && (len(sc.Peers) != 1 || sc.Peers[0].Node != peer.Node || sc.Peers[0].SeenDigest != peer.SeenDigest) {
				return fmt.Errorf("the peer certificate was not persisted")
			}
			return err
		},
	}
}

// transactionBenchmarkPairingRecords are one request on each side between two
// peers, the counters and one expectation.
type transactionBenchmarkPairingRecords struct {
	target       pairing.TargetRecord
	initiator    pairing.InitiatorRecord
	limits       pairing.Limits
	expectations []pairing.Expectation
}

func newTransactionBenchmarkPairingRecords(b *testing.B) transactionBenchmarkPairingRecords {
	b.Helper()
	first, second := newTransactionBenchmarkPeer(b, 1), newTransactionBenchmarkPeer(b, 2)
	transcript := transactionBenchmarkTranscript(b, first.party(b), second.party(b), 10)
	at := transactionBenchmarkTargetRecord(transcript).ReceivedAt
	limits, err := pairing.NewLimits([]time.Time{at}, nil)
	if err != nil {
		b.Fatal(err)
	}
	return transactionBenchmarkPairingRecords{
		target: transactionBenchmarkTargetRecord(transcript), initiator: transactionBenchmarkInitiatorRecord(transcript),
		limits: limits, expectations: transactionBenchmarkExpectations(second.node),
	}
}

// transactionBenchmarkStorePairingCase makes the sample of one pairing record:
// seed, when set, puts the record the call removes on disk first, through the
// store's own put and outside the observed window; stored judges the scan
// afterwards.
func transactionBenchmarkStorePairingCase(name string, seed func(*store.Store, transactionBenchmarkPairingRecords) error,
	call func(*store.Store, transactionBenchmarkPairingRecords) error,
	stored func(*store.Scan, transactionBenchmarkPairingRecords) bool) transactionBenchmarkCase {
	return transactionBenchmarkCase{name: name, prepare: func(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
		_, st := newTransactionMaintenanceStore(b, observer)
		records := newTransactionBenchmarkPairingRecords(b)
		if seed != nil {
			if err := seed(st, records); err != nil {
				b.Fatal(err)
			}
		}
		return transactionBenchmarkFixture{
			invoke: func() error { return call(st, records) },
			verify: func() error {
				sc, err := transactionBenchmarkScan(st)
				if err == nil && !stored(sc, records) {
					return fmt.Errorf("%s did not leave the expected records", name)
				}
				return err
			},
		}
	}}
}
