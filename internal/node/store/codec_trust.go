package store

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// The records of state/trust/ and state/peers/ hold signed statements of other
// parties. The store keeps them whole and checks their structure only: the
// envelope is canonical, the record is of the type the kind names, its payload
// decodes through the descriptor of the type, and the key says what the record
// is. It never checks a signature or a validity window: those depend on facts
// the store does not hold (the revocations, the clock), so whoever reads a
// record into a decision verifies it first.

// CredentialRecord is a trust credential this node holds, as the record both
// ends signed. The body of its file is the record's canonical bytes.
type CredentialRecord struct {
	// Digest is the digest of the credential record, the key. It is derived from
	// Record and checked against it on write and on read.
	Digest record.Digest
	Record record.Record
}

// Name is the file name of the record.
func (r CredentialRecord) Name() string {
	return recfile.FileName(string(KindCredential), DigestKeyBytes(r.Digest))
}

// CredRevocationRecord is the revocation of a credential, as the record one end
// of the credential signed: kept as it was made, so that it can be sent on as it
// is. The body of its file is the record's canonical bytes.
type CredRevocationRecord struct {
	// Revoked is the digest of the revoked credential, the key. It is derived from
	// the credential the revocation embeds and checked against it on write and on
	// read.
	Revoked record.Digest
	Record  record.Record
}

// Name is the file name of the record.
func (r CredRevocationRecord) Name() string {
	return recfile.FileName(string(KindCredRevocation), DigestKeyBytes(r.Revoked))
}

// LocalRevokedRecord is a node this node removed from its own trust: a decision
// of this node alone, which no other node learns of. It is kept until the owner
// clears it.
type LocalRevokedRecord struct {
	Node ident.NodeID
	// At is when the owner removed the node.
	At time.Time
	// Operation is the name of the administrative operation that removed it, in
	// the form of AdminRecord.Op.
	Operation string
}

// Name is the file name of the record.
func (r LocalRevokedRecord) Name() string {
	return recfile.FileName(string(KindLocalRevoked), NodeKeyBytes(r.Node))
}

// PeerRecord is what this node keeps of a peer's network key: the certificate
// accepted from the node, and the highest serial seen for it with the digest of
// the certificate seen at that serial. The serial seen may be above the accepted
// certificate's serial, and it only moves up, so an older certificate and its key
// cannot come back; its digest is what tells a later certificate of the same
// serial but another content from the very one seen (record.SerialSeen).
type PeerRecord struct {
	Node        ident.NodeID
	Certificate record.Record
	SerialSeen  uint64
	// SeenDigest is the digest of the certificate seen at SerialSeen: the
	// digest of Certificate when SerialSeen is its serial, otherwise the digest of
	// a certificate that was seen and not kept.
	SeenDigest record.Digest
}

// Name is the file name of the record.
func (r PeerRecord) Name() string { return recfile.FileName(string(KindPeer), NodeKeyBytes(r.Node)) }

// checkWire is the structure check of a stored record: it has the type of d and
// a payload that d's decoding accepts. It returns the payload.
func checkWire[P any](d record.Descriptor[P], rec record.Record) (P, error) {
	u, err := record.Peek(d, rec)
	if err != nil {
		var zero P
		return zero, err
	}
	return u.Payload(), nil
}

func decodeWire(body []byte) (record.Record, error) {
	return record.Decode(body, record.DefaultMaxInput)
}

// ---- credentials and their revocations

func encodeCredential(r CredentialRecord) ([]byte, error) {
	if _, err := checkCredential(r); err != nil {
		return nil, err
	}
	return r.Record.Encode()
}

func checkCredential(r CredentialRecord) (credential.Credential, error) {
	c, err := checkWire(credential.Descriptor, r.Record)
	if err != nil {
		return c, err
	}
	if r.Record.Digest() != r.Digest {
		return c, errors.New("the credential is not the one its key names")
	}
	return c, nil
}

func decodeCredential(key record.Digest, body []byte) (CredentialRecord, error) {
	rec, err := decodeWire(body)
	if err != nil {
		return CredentialRecord{}, err
	}
	r := CredentialRecord{Digest: key, Record: rec}
	_, err = checkCredential(r)
	return r, err
}

func encodeCredRevocation(r CredRevocationRecord) ([]byte, error) {
	if err := checkCredRevocation(r); err != nil {
		return nil, err
	}
	return r.Record.Encode()
}

func checkCredRevocation(r CredRevocationRecord) error {
	rev, err := checkWire(credential.RevocationDescriptor, r.Record)
	if err != nil {
		return err
	}
	if rev.Digest() != trust.CredentialDigest(r.Revoked) {
		return errors.New("the revocation is not of the credential its key names")
	}
	return nil
}

func decodeCredRevocation(key record.Digest, body []byte) (CredRevocationRecord, error) {
	rec, err := decodeWire(body)
	if err != nil {
		return CredRevocationRecord{}, err
	}
	r := CredRevocationRecord{Revoked: key, Record: rec}
	return r, checkCredRevocation(r)
}

// ---- local revocations

type localRevokedJSON struct {
	Node      string `json:"node"`
	At        wtime  `json:"at"`
	Operation string `json:"operation"`
}

func encodeLocalRevoked(r LocalRevokedRecord) ([]byte, error) {
	if r.Node.IsZero() {
		return nil, errors.New("a local revocation without a node")
	}
	if !opRE.MatchString(r.Operation) {
		return nil, fmt.Errorf("operation name %q", r.Operation)
	}
	at, err := wreq(r.At, "the time of a local revocation")
	if err != nil {
		return nil, err
	}
	return marshal(localRevokedJSON{Node: r.Node.String(), At: at, Operation: r.Operation})
}

func decodeLocalRevoked(key ident.NodeID, body []byte) (LocalRevokedRecord, error) {
	var j localRevokedJSON
	if err := unmarshal(body, &j); err != nil {
		return LocalRevokedRecord{}, err
	}
	r := LocalRevokedRecord{Node: key, At: time.Time(j.At), Operation: j.Operation}
	if j.Node != key.String() {
		return r, errors.New("the node is not the one the key names")
	}
	if !opRE.MatchString(r.Operation) {
		return r, fmt.Errorf("operation name %q", r.Operation)
	}
	return r, nil
}

// ---- peers

type peerJSON struct {
	Cert       string `json:"cert"`
	SerialSeen uint64 `json:"serial_seen"`
	Digest     string `json:"digest"`
}

// checkPeer holds a peer record to its rules: the certificate is a network-key
// certificate of the node the key names, the serial seen is not below it, and the
// digest seen is the certificate's own when the serial seen is its serial.
func checkPeer(r PeerRecord) error {
	c, err := checkWire(record.NetworkKeyCertificateDescriptor, r.Certificate)
	if err != nil {
		return err
	}
	if c.Node != r.Node {
		return errors.New("the certificate is not the certificate of the node the key names")
	}
	if err := record.RequireCounter(r.SerialSeen); err != nil {
		return fmt.Errorf("serial seen: %w", err)
	}
	if r.SerialSeen < c.Serial {
		return fmt.Errorf("serial seen %d is below the serial %d of the certificate", r.SerialSeen, c.Serial)
	}
	if r.SeenDigest.IsZero() {
		return errors.New("the digest seen is zero")
	}
	if r.SerialSeen == c.Serial && r.SeenDigest != r.Certificate.Digest() {
		return errors.New("the digest seen is not the digest of the certificate at its serial")
	}
	return nil
}

func encodePeer(r PeerRecord) ([]byte, error) {
	if err := checkPeer(r); err != nil {
		return nil, err
	}
	wire, err := r.Certificate.Encode()
	if err != nil {
		return nil, err
	}
	return marshal(peerJSON{Cert: b64(wire), SerialSeen: r.SerialSeen, Digest: r.SeenDigest.String()})
}

func decodePeer(key ident.NodeID, body []byte) (PeerRecord, error) {
	var j peerJSON
	if err := unmarshal(body, &j); err != nil {
		return PeerRecord{}, err
	}
	wire, err := unb64(j.Cert)
	if err != nil {
		return PeerRecord{}, fmt.Errorf("cert: %w", err)
	}
	cert, err := decodeWire(wire)
	if err != nil {
		return PeerRecord{}, fmt.Errorf("cert: %w", err)
	}
	r := PeerRecord{Node: key, Certificate: cert, SerialSeen: j.SerialSeen}
	if err := parseHex(r.SeenDigest[:], j.Digest); err != nil {
		return r, fmt.Errorf("digest: %w", err)
	}
	return r, checkPeer(r)
}
