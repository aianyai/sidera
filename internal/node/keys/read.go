package keys

import (
	"errors"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// readKey reads and decodes a private key file.
func readKey(r fsx.Reader, p, kind string) (*privateKey, error) {
	data, err := readOwnerOnly(r, p)
	if err != nil {
		return nil, err
	}
	seed, err := decodeKeyFile(kind, data)
	if err != nil {
		return nil, corrupt(p, err)
	}
	k, err := keyFromSeed(seed)
	if err != nil {
		return nil, corrupt(p, errors.New("the seed does not make a usable key"))
	}
	return k, nil
}

// readSerial reads and decodes the serial file.
func readSerial(r fsx.Reader) (record.SerialSeen, error) {
	data, err := readOwnerOnly(r, SerialPath)
	if err != nil {
		return record.SerialSeen{}, err
	}
	seen, err := decodeSerial(data)
	if err != nil {
		return record.SerialSeen{}, corrupt(SerialPath, err)
	}
	return seen, nil
}

// certificate is a network-key certificate that was read and verified: signed by
// the identity key that derives the node it names.
type certificate struct {
	verified record.Verified[record.NetworkKeyCertificate]
	wire     []byte
}

// readCertificate reads the certificate file and verifies it. It does not say
// whom the certificate belongs to: that is for the caller to check against the
// keys.
func readCertificate(r fsx.Reader) (certificate, error) {
	data, err := readOwnerOnly(r, CertificatePath)
	if err != nil {
		return certificate{}, err
	}
	return verifyCertificate(data)
}

func verifyCertificate(data []byte) (certificate, error) {
	rec, err := record.Decode(data, record.DefaultMaxInput)
	if err != nil {
		return certificate{}, corrupt(CertificatePath, err)
	}
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		return certificate{}, corrupt(CertificatePath, err)
	}
	return certificate{verified: v, wire: data}, nil
}

// ReadSelfRevocation reads keys/self-revocation.rec and verifies that it is a
// node's self-revocation, signed by the identity key of the node it names. It is
// the reader for a caller with no loaded keys, an export while the node is
// stopped, and it writes nothing. It does not know the identity key of this
// node, so it cannot say that the statement is this node's: Load and init check
// that, and a running node takes the statement from Keys.SelfRevocation.
func ReadSelfRevocation(r fsx.Reader) (SelfRevocation, error) {
	data, err := readOwnerOnly(r, SelfRevocationPath)
	if err != nil {
		return SelfRevocation{}, err
	}
	return verifySelfRevocation(data)
}

func verifySelfRevocation(data []byte) (SelfRevocation, error) {
	rec, err := record.Decode(data, record.DefaultMaxInput)
	if err != nil {
		return SelfRevocation{}, corrupt(SelfRevocationPath, err)
	}
	rv, err := revocation.Verify(rec)
	if err != nil {
		return SelfRevocation{}, corrupt(SelfRevocationPath, err)
	}
	if rv.Kind() != revocation.KindNodeSelf {
		return SelfRevocation{}, corrupt(SelfRevocationPath, errors.New("the statement is not a node's self-revocation"))
	}
	return SelfRevocation{wire: data, digest: rv.Digest(), node: rv.Node()}, nil
}
