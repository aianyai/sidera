package store

import (
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/pairing"
)

// PairingTargetPut records a request the target holds, replacing the file of the
// same request whole.
func (s *Store) PairingTargetPut(r pairing.TargetRecord) error {
	f, err := recordFile(PairingDir, PairingTargetRecord{r})
	if err != nil {
		return err
	}
	return s.putPairing(&declPairingTargetPut, RolePairingTarget, f)
}

// PairingTargetDelete removes the record of a request the target held and makes
// the removal durable. A request that has no record is not an error, and nothing
// is written.
func (s *Store) PairingTargetDelete(id pairing.ID) error {
	return s.deletePairing(&declPairingTargetDelete, RolePairingTarget, pairingName(KindPairingTarget, id))
}

// PairingInitiatorPut records a request the initiator started, replacing the file
// of the same request whole.
func (s *Store) PairingInitiatorPut(r pairing.InitiatorRecord) error {
	f, err := recordFile(PairingDir, PairingInitiatorRecord{r})
	if err != nil {
		return err
	}
	return s.putPairing(&declPairingInitiatorPut, RolePairingInitiator, f)
}

// PairingInitiatorDelete removes the record of a request the initiator started
// and makes the removal durable. A request that has no record is not an error,
// and nothing is written.
func (s *Store) PairingInitiatorDelete(id pairing.ID) error {
	return s.deletePairing(&declPairingInitiatorDelete, RolePairingInitiator, pairingName(KindPairingInitiator, id))
}

// PairingLimitsPut records the counters of the pairing package, replacing the
// file whole.
func (s *Store) PairingLimitsPut(l pairing.Limits) error {
	f, err := recordFile(PairingDir, PairingLimitsRecord{l})
	if err != nil {
		return err
	}
	return s.putPairing(&declPairingLimits, RolePairingLimits, f)
}

// PairingExpectationsPut records the list of expectations of the pairing
// package, replacing the file whole.
func (s *Store) PairingExpectationsPut(list []pairing.Expectation) error {
	f, err := recordFile(PairingDir, PairingExpectationsRecord{list})
	if err != nil {
		return err
	}
	return s.putPairing(&declPairingExpectations, RolePairingExpectations, f)
}

func (s *Store) putPairing(decl *txn.Decl, role string, f file) error {
	tx := s.w.Begin(decl)
	defer tx.Close()
	if err := s.replace(tx, role, f); err != nil {
		return err
	}
	return tx.Commit()
}

// deletePairing removes one file of state/pairing/ in a transaction of its own,
// after asking the disk whether it is there.
func (s *Store) deletePairing(decl *txn.Decl, role, name string) error {
	ref := FileRef{Dir: PairingDir, Name: name}
	ex, err := s.exist([]FileRef{ref})
	if err != nil {
		return err
	}
	if !ex[ref] {
		return nil
	}
	tx := s.w.Begin(decl)
	defer tx.Close()
	if _, err := s.removeRef(tx, role, ref, ex); err != nil {
		return err
	}
	if err := tx.SyncDir(RolePairingDir, PairingDir); err != nil {
		return err
	}
	return tx.Commit()
}

// PreparedPairingExpectations is the list of expectations encoded for a
// transaction another package opens, as PreparedLocalRevoked is.
type PreparedPairingExpectations struct{ f file }

// PreparePairingExpectations validates and encodes the list of expectations of
// the pairing package. It does no I/O.
func PreparePairingExpectations(list []pairing.Expectation) (PreparedPairingExpectations, error) {
	f, err := recordFile(PairingDir, PairingExpectationsRecord{list})
	if err != nil {
		return PreparedPairingExpectations{}, err
	}
	return PreparedPairingExpectations{f}, nil
}

// PutPairingExpectations writes the prepared list in a transaction another
// package opened, under the role the declaration gives it
// (RolePairingExpectations by convention). It panics for a value
// PreparePairingExpectations did not return.
func PutPairingExpectations(tx *txn.Tx, role string, p PreparedPairingExpectations) error {
	if p.f.path == "" {
		panic("store: PutPairingExpectations of a value PreparePairingExpectations did not return")
	}
	return tx.Replace(role, p.f.path, p.f.data, fsx.ModePrivate)
}

// PreparedPairingInitiator is a request the initiator started, encoded for a
// transaction another package opens, as PreparedPairingExpectations is.
type PreparedPairingInitiator struct{ f file }

// PreparePairingInitiator validates and encodes the record of a request the
// initiator started. It does no I/O.
func PreparePairingInitiator(r pairing.InitiatorRecord) (PreparedPairingInitiator, error) {
	f, err := recordFile(PairingDir, PairingInitiatorRecord{r})
	if err != nil {
		return PreparedPairingInitiator{}, err
	}
	return PreparedPairingInitiator{f}, nil
}

// PutPairingInitiator writes the prepared record in a transaction another package
// opened, under the role the declaration gives it (RolePairingInitiator by
// convention). It panics for a value PreparePairingInitiator did not return.
func PutPairingInitiator(tx *txn.Tx, role string, p PreparedPairingInitiator) error {
	if p.f.path == "" {
		panic("store: PutPairingInitiator of a value PreparePairingInitiator did not return")
	}
	return tx.Replace(role, p.f.path, p.f.data, fsx.ModePrivate)
}
