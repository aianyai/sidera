package keys

import (
	"fmt"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// Keys is everything loaded from keys/: the private keys, the certificate in
// force and the highest serial issued. It is owned by the single writer and is
// not safe for concurrent use. Its state holds private keys, so it is held
// behind one pointer and prints as an address.
type Keys struct{ s *state }

type state struct {
	w        *txn.Writer
	identity *privateKey
	network  *privateKey
	cert     record.Verified[record.NetworkKeyCertificate]
	certWire []byte
	seen     record.SerialSeen
	selfRev  SelfRevocation
}

// SelfRevocation is the node's statement that its identity key is void, as it
// is stored: the record wire bytes, the record digest and the node it names. Only
// this package makes one, from a statement it verified, so the three always
// belong together.
type SelfRevocation struct {
	wire   []byte
	digest record.Digest
	node   ident.NodeID
}

// Wire is a copy of the record wire bytes.
func (s SelfRevocation) Wire() []byte { return append([]byte(nil), s.wire...) }

// Digest is the record digest, the name of the statement.
func (s SelfRevocation) Digest() record.Digest { return s.digest }

// Node is the node whose identity key the statement voids.
func (s SelfRevocation) Node() ident.NodeID { return s.node }

// Load reads and checks every file of keys/ except the clock record, which it
// only checks to be readable, and returns the keys. It refuses, naming the file,
// a directory or file that anyone but the owner can reach, a file that is
// missing or damaged, an init that did not finish (the marker is missing: the
// writes of init are all optional, so only the marker says that the other
// files, the config and the first clock record are all there), and a file that
// contradicts the others: the
// certificate must be for the node of the identity key and name the network key,
// the serial file must be the node's and agree with the certificate, and the
// self-revocation must be the statement of this node's identity key. It writes
// nothing and repairs nothing.
//
// A certificate whose serial is below the serial file's is not a refusal: it is
// an interrupted renewal. The node loads, RenewalInterrupted says so, and the
// renewal at startup repairs it.
func Load(w *txn.Writer) (*Keys, error) {
	r := w.Reader()
	if err := CheckPermissions(r); err != nil {
		return nil, err
	}
	identity, err := readKey(r, IdentityKeyPath, kindIdentityKey)
	if err != nil {
		return nil, err
	}
	network, err := readKey(r, NetworkKeyPath, kindNetworkKey)
	if err != nil {
		return nil, err
	}
	cert, err := readCertificate(r)
	if err != nil {
		return nil, err
	}
	seen, err := readSerial(r)
	if err != nil {
		return nil, err
	}
	rev, err := ReadSelfRevocation(r)
	if err != nil {
		return nil, err
	}
	if _, err := ReadClock(r); err != nil {
		return nil, err
	}
	if done, err := Initialized(r); err != nil {
		return nil, err
	} else if !done {
		return nil, fileError(InitMarkerPath, ErrMissing, "init has not finished")
	}

	c := cert.verified.Payload()
	switch {
	case c.Node != identity.node:
		return nil, fileError(CertificatePath, ErrInconsistent, "it is not for the node of the identity key")
	case c.NetworkKey != network.pub:
		return nil, fileError(NetworkKeyPath, ErrInconsistent, "it is not the network key that the certificate names")
	}
	switch record.DecideSerial(&seen, cert.verified) {
	case record.SerialStale: // the certificate is the one seen, or older: a renewal was interrupted
	case record.SerialAccept:
		return nil, fileError(SerialPath, ErrInconsistent, "the serial %d is behind the certificate's %d", seen.Serial, c.Serial)
	case record.SerialConflict:
		return nil, fileError(SerialPath, ErrInconsistent, "it names another certificate at the serial of the certificate")
	default:
		return nil, fileError(SerialPath, ErrInconsistent, "it is not the serial of this node")
	}
	if rev.node != identity.node {
		return nil, fileError(SelfRevocationPath, ErrInconsistent, "it is not the statement of the identity key")
	}
	return &Keys{s: &state{w: w, identity: identity, network: network, cert: cert.verified, certWire: cert.wire, seen: seen, selfRev: rev}}, nil
}

// SelfRevocation is the statement that voids this node's identity key, the one
// Load verified as this node's. It is what a running node retires itself with
// and exports: a caller that read the file again would have to compare its node
// with Node, and a retirement cannot be undone.
func (k *Keys) SelfRevocation() SelfRevocation { return k.s.selfRev }

// Node is the identifier of this node, derived from its identity key.
func (k *Keys) Node() ident.NodeID { return k.s.identity.node }

// NetworkSigner signs with the network key. It prints as nothing. The identity
// key signs inside this package only: the certificates and the self-revocation.
func (k *Keys) NetworkSigner() record.Signer { return k.s.network.signer() }

// Certificate is the certificate in force. After an interrupted renewal it is
// the old one, and its serial is below SerialSeen's: it must not be published,
// because a peer that has seen the higher serial would take it for stale.
func (k *Keys) Certificate() record.Verified[record.NetworkKeyCertificate] { return k.s.cert }

// CertificateWire is the certificate in force as the record wire bytes.
func (k *Keys) CertificateWire() []byte { return append([]byte(nil), k.s.certWire...) }

// SerialSeen is the highest serial this node has issued, with the digest of the
// certificate issued at it. It is never below the certificate's serial.
func (k *Keys) SerialSeen() record.SerialSeen { return k.s.seen }

// RenewalInterrupted reports that the serial file is ahead of the certificate:
// a renewal reserved a serial and stopped before it wrote the certificate.
func (k *Keys) RenewalInterrupted() bool { return RenewalInterrupted(k.s.seen, k.s.cert) }

// CheckNotRetired refuses to run a node whose own identity has been retired:
// the statement is among the revocations that were loaded. The node stays off
// for good, because a retired identity cannot be made valid again.
func (k *Keys) CheckNotRetired(set *revocation.Set) error {
	if set.NodeRetired(k.s.identity.node) {
		return fmt.Errorf("keys: %w", ErrIdentityRetired)
	}
	return nil
}
