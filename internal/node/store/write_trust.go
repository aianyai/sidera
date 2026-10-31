package store

import (
	"errors"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// TrustCredential records a credential this node holds and, when the call has
// one, the certificate of the peer it was made with. The credential is the commit
// point; the peer record that follows may be missing after a crash, and the peer's
// next exchange brings it again.
func (s *Store) TrustCredential(c CredentialRecord, peer *PeerRecord) error {
	cf, err := recordFile(TrustDir, c)
	if err != nil {
		return err
	}
	var pf *file
	if peer != nil {
		f, err := recordFile(PeersDir, *peer)
		if err != nil {
			return err
		}
		pf = &f
	}
	tx := s.w.Begin(&declTrustCredential)
	defer tx.Close()
	if err := s.replace(tx, "credential", cf); err != nil {
		return err
	}
	if pf != nil {
		if err := s.replace(tx, "peer", *pf); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TrustRevocation records the revocation of a credential.
func (s *Store) TrustRevocation(r CredRevocationRecord) error {
	f, err := recordFile(TrustDir, r)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declTrustRevocation)
	defer tx.Close()
	if err := s.replace(tx, RoleCredRevocation, f); err != nil {
		return err
	}
	return tx.Commit()
}

// PeerCert records the certificate accepted from a peer and the highest serial
// seen for it. The record replaces the peer's earlier one whole: the caller has
// decided, from the record it read, that the new one is not lower.
func (s *Store) PeerCert(p PeerRecord) error {
	f, err := recordFile(PeersDir, p)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declPeerCert)
	defer tx.Close()
	if err := s.replace(tx, "peer", f); err != nil {
		return err
	}
	return tx.Commit()
}

// LocalRevokedClear takes a node back into this node's trust: it removes the
// record of its local removal and makes the removal durable. A node that has no
// record is not an error, and nothing is written.
func (s *Store) LocalRevokedClear(node ident.NodeID) error {
	if node.IsZero() {
		return errors.New("store: LocalRevokedClear of a zero node")
	}
	ref := FileRef{Dir: TrustDir, Name: LocalRevokedRecord{Node: node}.Name()}
	ex, err := s.exist([]FileRef{ref})
	if err != nil {
		return err
	}
	if !ex[ref] {
		return nil
	}
	tx := s.w.Begin(&declTrustLocalClear)
	defer tx.Close()
	if _, err := s.removeRef(tx, RoleLocalRevoked, ref, ex); err != nil {
		return err
	}
	if err := tx.SyncDir("local-revoked-dir", TrustDir); err != nil {
		return err
	}
	return tx.Commit()
}

// PreparedLocalRevoked is a local-revoked record encoded for a transaction
// another package opens, as PreparedEndpoint is.
type PreparedLocalRevoked struct{ f file }

// PrepareLocalRevoked validates and encodes the record of a node removed from
// this node's trust. It does no I/O.
func PrepareLocalRevoked(r LocalRevokedRecord) (PreparedLocalRevoked, error) {
	f, err := recordFile(TrustDir, r)
	if err != nil {
		return PreparedLocalRevoked{}, err
	}
	return PreparedLocalRevoked{f}, nil
}

// PutLocalRevoked writes a prepared record in a transaction another package
// opened, under the role the declaration gives it (RoleLocalRevoked by
// convention). It panics for a PreparedLocalRevoked that PrepareLocalRevoked did
// not return.
func PutLocalRevoked(tx *txn.Tx, role string, p PreparedLocalRevoked) error {
	if p.f.path == "" {
		panic("store: PutLocalRevoked of a value PrepareLocalRevoked did not return")
	}
	return tx.Replace(role, p.f.path, p.f.data, fsx.ModePrivate)
}

// PreparedCredRevocation is the revocation of a credential encoded for a
// transaction another package opens, as PreparedEndpoint is.
type PreparedCredRevocation struct{ f file }

// PrepareCredRevocation validates and encodes the revocation of a credential. It
// does no I/O.
func PrepareCredRevocation(r CredRevocationRecord) (PreparedCredRevocation, error) {
	f, err := recordFile(TrustDir, r)
	if err != nil {
		return PreparedCredRevocation{}, err
	}
	return PreparedCredRevocation{f}, nil
}

// PutCredRevocation writes a prepared revocation in a transaction another
// package opened, under the role the declaration gives it (RoleCredRevocation by
// convention). It panics for a PreparedCredRevocation that PrepareCredRevocation
// did not return.
func PutCredRevocation(tx *txn.Tx, role string, p PreparedCredRevocation) error {
	if p.f.path == "" {
		panic("store: PutCredRevocation of a value PrepareCredRevocation did not return")
	}
	return tx.Replace(role, p.f.path, p.f.data, fsx.ModePrivate)
}
