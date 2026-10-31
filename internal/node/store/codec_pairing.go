package store

import (
	"encoding/hex"
	"errors"

	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/internal/pairing"
)

// The records of state/pairing/ are the persisted forms the pairing package
// defines: the body of a file is the bytes its Encode (EncodeExpectations for the
// list) returns, kept as given. The pairing package reads them back strictly and
// bounded by pairing.MaxPersisted, so the store checks nothing of a body but
// what the key adds: a request's record is the request its key names.

// singletonKeyText is the key text of a kind of which one file exists.
const singletonKeyText = "singleton"

// SingletonKey is the parsed key of a kind of which one file exists
// (KindPairingLimits and KindPairingExpectations).
type SingletonKey struct{}

// PairingKeyBytes is the key text of a pairing request: its identifier in
// lowercase hex.
func PairingKeyBytes(id pairing.ID) []byte { return []byte(hex.EncodeToString(id[:])) }

// pairingName is the file name of the record of a request of the given kind.
func pairingName(kind Kind, id pairing.ID) string {
	return recfile.FileName(string(kind), PairingKeyBytes(id))
}

// PairingTargetRecord is a request the target holds, as the pairing package
// persists it.
type PairingTargetRecord struct{ Record pairing.TargetRecord }

// Name is the file name of the record.
func (r PairingTargetRecord) Name() string { return pairingName(KindPairingTarget, r.Record.ID()) }

// PairingInitiatorRecord is a request the initiator started, as the pairing
// package persists it.
type PairingInitiatorRecord struct{ Record pairing.InitiatorRecord }

// Name is the file name of the record.
func (r PairingInitiatorRecord) Name() string {
	return pairingName(KindPairingInitiator, r.Record.ID())
}

// PairingLimitsRecord is the counters that limit how often this node reveals its
// code and how often a pairing fails.
type PairingLimitsRecord struct{ Limits pairing.Limits }

// Name is the file name of the record.
func (PairingLimitsRecord) Name() string {
	return recfile.FileName(string(KindPairingLimits), []byte(singletonKeyText))
}

// PairingExpectationsRecord is the list of node identifiers an owner entered
// ahead of time.
type PairingExpectationsRecord struct{ List []pairing.Expectation }

// Name is the file name of the record.
func (PairingExpectationsRecord) Name() string {
	return recfile.FileName(string(KindPairingExpectations), []byte(singletonKeyText))
}

var errPairingKey = errors.New("the record is not the request its key names")

func encodePairingTarget(r PairingTargetRecord) ([]byte, error) {
	if r.Record.ID().IsZero() {
		return nil, errors.New("a pairing record without an identifier")
	}
	return r.Record.Encode()
}

func decodePairingTarget(key pairing.ID, body []byte) (PairingTargetRecord, error) {
	rec, err := pairing.DecodeTargetRecord(body)
	if err != nil {
		return PairingTargetRecord{}, err
	}
	if rec.ID() != key {
		return PairingTargetRecord{}, errPairingKey
	}
	return PairingTargetRecord{rec}, nil
}

func encodePairingInitiator(r PairingInitiatorRecord) ([]byte, error) {
	if r.Record.ID().IsZero() {
		return nil, errors.New("a pairing record without an identifier")
	}
	return r.Record.Encode()
}

func decodePairingInitiator(key pairing.ID, body []byte) (PairingInitiatorRecord, error) {
	rec, err := pairing.DecodeInitiatorRecord(body)
	if err != nil {
		return PairingInitiatorRecord{}, err
	}
	if rec.ID() != key {
		return PairingInitiatorRecord{}, errPairingKey
	}
	return PairingInitiatorRecord{rec}, nil
}

func decodePairingLimits(body []byte) (PairingLimitsRecord, error) {
	l, err := pairing.DecodeLimits(body)
	return PairingLimitsRecord{l}, err
}

func decodePairingExpectations(body []byte) (PairingExpectationsRecord, error) {
	list, err := pairing.DecodeExpectations(body)
	return PairingExpectationsRecord{list}, err
}
