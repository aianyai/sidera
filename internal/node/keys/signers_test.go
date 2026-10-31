package keys_test

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/record"
)

// The identity signer is the signer of the records that bind this node to
// another: a credential made with two of them is complete and verifies with the
// rule every receiver applies, and the network key, which signs everything else,
// is refused for the same record.
func TestTheIdentitySignerSignsATrustCredential(t *testing.T) {
	a, b := newRig(t), newRig(t)
	a.mustInit(1, t0)
	b.mustInit(2, t0)
	ka, kb := a.mustLoad(), b.mustLoad()

	if node, err := ident.NodeIDFromKey(ka.IdentitySigner().Public()); err != nil || node != ka.Node() {
		t.Fatalf("the identity signer's key names %s (%v), the node is %s", node, err, ka.Node())
	}
	if ka.IdentitySigner().Public() == ka.NetworkSigner().Public() {
		t.Fatal("the identity signer is the network signer")
	}

	c := credential.New(ka.Node(), kb.Node(), t0, t0.Add(1000*keys.CertificateLifetime), credential.Nonce{1})
	signA, signB := ka.IdentitySigner(), kb.IdentitySigner()
	if c.A != ka.Node() {
		signA, signB = signB, signA
	}
	rec, err := record.Seal(credential.Descriptor, c, signA)
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = record.CoSign(credential.Descriptor, rec, signB); err != nil {
		t.Fatal(err)
	}
	if _, err := record.Verify(credential.Descriptor, rec); err != nil {
		t.Errorf("a credential signed by the two identity signers does not verify: %v", err)
	}
	if _, err := record.Seal(credential.Descriptor, c, ka.NetworkSigner()); !errors.Is(err, record.ErrSignerNotAllowed) {
		t.Errorf("the network key signed a credential: %v", err)
	}
}

// What the network layer is given is the network key itself, so that a
// connection is authenticated by the key the certificate names, and a copy: it
// signs as the network signer does, and changing it changes nothing in the keys.
func TestTheNetworkPrivateKeyIsACopyOfTheKeyTheCertificateNames(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()

	priv := k.NetworkPrivateKey()
	pub, err := ident.NewPublicKey(ident.AlgorithmEd25519, priv.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if pub != k.Certificate().Payload().NetworkKey {
		t.Error("the private key is not the key the certificate names")
	}
	if !bytes.Equal(priv.Seed(), oracleSeed(t, "network-key", r.read(keys.NetworkKeyPath))) {
		t.Error("the private key is not the one on disk")
	}
	msg := []byte("a message")
	sig, err := k.NetworkSigner().Sign(msg)
	if err != nil || !ed25519.Verify(priv.Public().(ed25519.PublicKey), msg, sig) {
		t.Errorf("the network signer's signature does not verify under the private key's public key (%v)", err)
	}

	priv[0] ^= 0xff
	if again := k.NetworkPrivateKey(); bytes.Equal(again, priv) {
		t.Error("changing the returned key changed the key held")
	}
}
