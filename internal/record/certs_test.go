package record

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
)

func netCert(t *testing.T, identity, network Signer, serial uint64) NetworkKeyCertificate {
	t.Helper()
	return NetworkKeyCertificate{Node: nodeOf(t, identity), NetworkKey: network.Public(), Serial: serial, NotBefore: epoch, NotAfter: endTime}
}

func TestGoldenNetworkKeyCertificate(t *testing.T) {
	identity, network := rfcSigner(t, 1), rfcSigner(t, 2)
	cert := netCert(t, identity, network, 1)

	rec, err := Seal(NetworkKeyCertificateDescriptor, cert, identity)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire, goldenNetworkKeyCertificate) {
		t.Fatalf("encoded certificate differs from the golden bytes\n got %x\nwant %x", wire, goldenNetworkKeyCertificate)
	}
	if rec.Digest().String() != goldenNetworkKeyCertificateDigest {
		t.Fatalf("digest = %s", rec.Digest())
	}
	if !bytes.Equal(SignedBytes(rec.Type, rec.Payload), mustHexBytes(t, goldenNetworkKeyCertificateSignedBytes)) {
		t.Fatal("signed bytes differ from the golden bytes")
	}

	dec, err := Decode(goldenNetworkKeyCertificate, DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(NetworkKeyCertificateDescriptor, dec)
	if err != nil {
		t.Fatal(err)
	}
	if v.Payload != cert {
		t.Fatalf("payload = %+v, want %+v", v.Payload, cert)
	}
	if got := v.Payload.Node.String(); got != "node130llww2yztfmjn2fsslfx45qzye83306u3q7nzfs300qvxly7wpqgs2d9f" {
		t.Fatalf("node text = %s", got)
	}
}

func TestNetworkKeyCertificateRules(t *testing.T) {
	identity, network, other := rfcSigner(t, 1), rfcSigner(t, 2), rfcSigner(t, 3)
	good := netCert(t, identity, network, 1)

	t.Run("identity and network key roles swapped: signed by the network key", func(t *testing.T) {
		rec, err := Sign(NetworkKeyCertificateType, mustEncode(t, good), network)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(NetworkKeyCertificateDescriptor, rec); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
		if _, err := Seal(NetworkKeyCertificateDescriptor, good, network); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("Seal error = %v", err)
		}
	})
	t.Run("identity and network key roles swapped: node derived from the network key", func(t *testing.T) {
		swapped := good
		swapped.Node = nodeOf(t, network)
		swapped.NetworkKey = identity.Public()
		rec, err := Sign(NetworkKeyCertificateType, mustEncode(t, swapped), identity)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(NetworkKeyCertificateDescriptor, rec); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a certificate signed by an unrelated key is refused", func(t *testing.T) {
		if _, err := Seal(NetworkKeyCertificateDescriptor, good, other); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a certificate signed by both keys is refused", func(t *testing.T) {
		rec, _ := Sign(NetworkKeyCertificateType, mustEncode(t, good), identity, network)
		if _, err := Verify(NetworkKeyCertificateDescriptor, rec); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a device certificate is not a network-key certificate", func(t *testing.T) {
		dev, _ := Seal(DeviceCertificateDescriptor, DeviceCertificate{Person: personOf(t, identity), DeviceKey: network.Public(), MaxLevel: admin.LevelView, NotBefore: epoch, NotAfter: endTime}, identity)
		if _, err := Verify(NetworkKeyCertificateDescriptor, dev); !errors.Is(err, ErrType) {
			t.Fatalf("error = %v", err)
		}
	})

	bad := []struct {
		rule string
		mut  func(*NetworkKeyCertificate)
		want error
	}{
		{"serial zero", func(c *NetworkKeyCertificate) { c.Serial = 0 }, ErrCounter},
		{"zero node", func(c *NetworkKeyCertificate) { c.Node = ident.NodeID{} }, ident.ErrZero},
		{"zero network key", func(c *NetworkKeyCertificate) { c.NetworkKey = ident.PublicKey{} }, ErrKey},
		{"zero not_before", func(c *NetworkKeyCertificate) { c.NotBefore = time.Time{} }, ErrTime},
		{"zero not_after", func(c *NetworkKeyCertificate) { c.NotAfter = time.Time{} }, ErrTime},
		{"not_before before the epoch", func(c *NetworkKeyCertificate) { c.NotBefore = time.Unix(-5, 0) }, ErrTime},
		{"not_after beyond the wire range", func(c *NetworkKeyCertificate) { c.NotAfter = time.Unix(MaxUnixSeconds+1, 0) }, ErrTime},
		{"empty validity window", func(c *NetworkKeyCertificate) { c.NotAfter = c.NotBefore }, ErrTime},
		{"inverted validity window", func(c *NetworkKeyCertificate) { c.NotAfter = c.NotBefore.Add(-time.Second) }, ErrTime},
	}
	for _, r := range bad {
		t.Run(r.rule+" is refused", func(t *testing.T) {
			c := good
			r.mut(&c)
			if _, err := Seal(NetworkKeyCertificateDescriptor, c, identity); !errors.Is(err, r.want) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
		})
	}

	// The same rules on the decoding side, from hand-written payloads.
	node, key := nodeOf(t, identity).Bytes(), cborw.key(1, network.Public().Bytes())
	payload := func(serial, nb, na uint64) []byte {
		return cborw.mapOf(cborw.uint(1), cborw.bstr(node), cborw.uint(2), key, cborw.uint(3), cborw.uint(serial), cborw.uint(4), cborw.uint(nb), cborw.uint(5), cborw.uint(na))
	}
	badWire := []struct {
		rule string
		in   []byte
		want error
	}{
		{"wire serial 0", payload(0, 1700000000, 1702592000), ErrCounter},
		{"wire not_before 0", payload(1, 0, 1702592000), ErrTime},
		{"wire not_after before not_before", payload(1, 1702592000, 1700000000), ErrTime},
		{"wire time beyond the range", payload(1, 1700000000, uint64(MaxUnixSeconds)+1), ErrTime},
		{"wire node of 31 bytes", cborw.mapOf(cborw.uint(1), cborw.bstr(node[:31]), cborw.uint(2), key, cborw.uint(3), cborw.uint(1), cborw.uint(4), cborw.uint(1700000000), cborw.uint(5), cborw.uint(1702592000)), ident.ErrLength},
		{"wire network key with the P-256 tag", cborw.mapOf(cborw.uint(1), cborw.bstr(node), cborw.uint(2), cborw.key(2, network.Public().Bytes()), cborw.uint(3), cborw.uint(1), cborw.uint(4), cborw.uint(1700000000), cborw.uint(5), cborw.uint(1702592000)), ident.ErrAlgorithm},
		{"wire field missing", cborw.mapOf(cborw.uint(1), cborw.bstr(node), cborw.uint(2), key, cborw.uint(3), cborw.uint(1), cborw.uint(4), cborw.uint(1700000000)), ErrNotCanonical},
		{"wire unknown field", cborw.mapOf(cborw.uint(1), cborw.bstr(node), cborw.uint(2), key, cborw.uint(3), cborw.uint(1), cborw.uint(4), cborw.uint(1700000000), cborw.uint(5), cborw.uint(1702592000), cborw.uint(6), cborw.uint(1)), ErrMalformed},
		{"wire negative serial", cborw.mapOf(cborw.uint(1), cborw.bstr(node), cborw.uint(2), key, cborw.uint(3), []byte{0x20}, cborw.uint(4), cborw.uint(1700000000), cborw.uint(5), cborw.uint(1702592000)), ErrMalformed},
	}
	for _, r := range badWire {
		t.Run(r.rule, func(t *testing.T) {
			rec, err := Sign(NetworkKeyCertificateType, r.in, identity)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(NetworkKeyCertificateDescriptor, rec); !errors.Is(err, r.want) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
		})
	}
}

func mustEncode(t *testing.T, c NetworkKeyCertificate) []byte {
	t.Helper()
	b, err := NetworkKeyCertificateDescriptor.encode(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNetworkKeyCertificateValidity(t *testing.T) {
	c := NetworkKeyCertificate{NotBefore: epoch, NotAfter: endTime}
	rows := []struct {
		rule string
		now  time.Time
		want Validity
	}{
		{"before the window", epoch.Add(-time.Nanosecond), NotYetValid},
		{"valid at the NotBefore instant", epoch, Valid},
		{"valid inside the window", epoch.Add(time.Hour), Valid},
		{"valid just before NotAfter", endTime.Add(-time.Nanosecond), Valid},
		{"expired at the NotAfter instant", endTime, Expired},
		{"expired after the window", endTime.Add(time.Hour), Expired},
		{"no tolerance is applied before the window", epoch.Add(-time.Minute), NotYetValid},
		{"no tolerance is applied after the window", endTime.Add(time.Minute), Expired},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := c.ValidityAt(r.now); got != r.want {
				t.Fatalf("ValidityAt = %v, want %v", got, r.want)
			}
		})
	}
	t.Run("an unset window is never valid", func(t *testing.T) {
		if got := (NetworkKeyCertificate{}).ValidityAt(epoch); got != ValidityUnknown {
			t.Fatalf("ValidityAt = %v", got)
		}
	})
}

func TestDecideSerial(t *testing.T) {
	identity, a, b := rfcSigner(t, 1), rfcSigner(t, 2), rfcSigner(t, 3)
	known := netCert(t, identity, a, 5)
	otherNode := netCert(t, rfcSigner(t, 2), a, 6)
	rows := []struct {
		rule     string
		known    *NetworkKeyCertificate
		incoming NetworkKeyCertificate
		want     SerialVerdict
	}{
		{"nothing known yet: accept", nil, netCert(t, identity, a, 1), SerialAccept},
		{"higher serial with a new key: accept", &known, netCert(t, identity, b, 6), SerialAccept},
		{"higher serial with the same key (renewal): accept", &known, netCert(t, identity, a, 6), SerialAccept},
		{"much higher serial: accept", &known, netCert(t, identity, b, 500), SerialAccept},
		{"lower serial: stale", &known, netCert(t, identity, b, 4), SerialStale},
		{"lowest serial: stale", &known, netCert(t, identity, b, 1), SerialStale},
		{"the same certificate again: stale", &known, known, SerialStale},
		{"same serial, different key: conflict", &known, netCert(t, identity, b, 5), SerialConflict},
		{"another node's certificate: wrong node", &known, otherNode, SerialWrongNode},
		{"incoming serial zero: invalid", &known, NetworkKeyCertificate{Node: known.Node, NetworkKey: b.Public()}, SerialInvalid},
		{"incoming without a node: invalid", nil, NetworkKeyCertificate{Serial: 1, NetworkKey: b.Public()}, SerialInvalid},
		{"an expired certificate with a higher serial still advances the serial", &known, func() NetworkKeyCertificate {
			c := netCert(t, identity, b, 7)
			c.NotAfter = c.NotBefore.Add(time.Second)
			return c
		}(), SerialAccept},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := DecideSerial(r.known, r.incoming); got != r.want {
				t.Fatalf("DecideSerial = %v, want %v", got, r.want)
			}
		})
	}
}

func TestNetworkKeyBelongs(t *testing.T) {
	identity, a, b := rfcSigner(t, 1), rfcSigner(t, 2), rfcSigner(t, 3)
	cert := netCert(t, identity, a, 3)
	node := cert.Node
	mid := epoch.Add(time.Hour)
	rows := []struct {
		rule string
		c    *NetworkKeyCertificate
		node ident.NodeID
		key  ident.PublicKey
		now  time.Time
		want Binding
	}{
		{"the certified key of the node, inside the window", &cert, node, a.Public(), mid, BindingOK},
		{"valid at NotBefore", &cert, node, a.Public(), epoch, BindingOK},
		{"no certificate known", nil, node, a.Public(), mid, BindingNoCertificate},
		{"another node", &cert, nodeOf(t, b), a.Public(), mid, BindingNodeMismatch},
		{"zero node", &cert, ident.NodeID{}, a.Public(), mid, BindingNodeMismatch},
		{"a key the certificate does not name", &cert, node, b.Public(), mid, BindingKeyMismatch},
		{"the identity key is not the network key", &cert, node, identity.Public(), mid, BindingKeyMismatch},
		{"zero key", &cert, node, ident.PublicKey{}, mid, BindingKeyMismatch},
		{"before the window", &cert, node, a.Public(), epoch.Add(-time.Second), BindingNotYetValid},
		{"at expiry", &cert, node, a.Public(), endTime, BindingExpired},
		{"after expiry", &cert, node, a.Public(), endTime.Add(time.Hour), BindingExpired},
		{"a certificate with an unset window", &NetworkKeyCertificate{Node: node, NetworkKey: a.Public(), Serial: 1}, node, a.Public(), mid, BindingNoCertificate},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := NetworkKeyBelongs(r.c, r.node, r.key, r.now); got != r.want {
				t.Fatalf("NetworkKeyBelongs = %v, want %v", got, r.want)
			}
		})
	}
}

// Rotating the network key leaves the node identifier unchanged: the identity
// key signs a second certificate with a higher serial, peers move to the new
// key, the old key stops belonging to the node, and a second certificate at the
// same serial is detected.
func TestRotatingTheNetworkKeyKeepsTheNodeID(t *testing.T) {
	identity := rfcSigner(t, 1)
	oldKey, newKey, rogueKey := rfcSigner(t, 2), rfcSigner(t, 3), seedSigner(t, 9)
	node := nodeOf(t, identity)
	now := epoch.Add(24 * time.Hour)

	accept := func(known *NetworkKeyCertificate, c NetworkKeyCertificate) (SerialVerdict, NetworkKeyCertificate) {
		rec, err := Seal(NetworkKeyCertificateDescriptor, c, identity)
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := rec.Encode()
		dec, err := Decode(wire, DefaultMaxInput)
		if err != nil {
			t.Fatal(err)
		}
		v, err := Verify(NetworkKeyCertificateDescriptor, dec)
		if err != nil {
			t.Fatal(err)
		}
		return DecideSerial(known, v.Payload), v.Payload
	}

	verdict, c1 := accept(nil, netCert(t, identity, oldKey, 1))
	if verdict != SerialAccept {
		t.Fatalf("first certificate: %v", verdict)
	}
	if got := NetworkKeyBelongs(&c1, node, oldKey.Public(), now); got != BindingOK {
		t.Fatalf("old key before rotation: %v", got)
	}

	verdict, c2 := accept(&c1, netCert(t, identity, newKey, 2))
	if verdict != SerialAccept {
		t.Fatalf("rotation certificate: %v", verdict)
	}
	if c2.Node != c1.Node || c2.Node != node {
		t.Fatal("the node identifier changed with the network key")
	}
	if c2.NetworkKey == c1.NetworkKey {
		t.Fatal("test setup: the key did not change")
	}
	if got := NetworkKeyBelongs(&c2, node, newKey.Public(), now); got != BindingOK {
		t.Fatalf("new key after rotation: %v", got)
	}
	if got := NetworkKeyBelongs(&c2, node, oldKey.Public(), now); got != BindingKeyMismatch {
		t.Fatalf("old key after rotation: %v", got)
	}
	if verdict, _ := accept(&c2, netCert(t, identity, oldKey, 1)); verdict != SerialStale {
		t.Fatalf("replay of the old certificate: %v", verdict)
	}
	if verdict, _ := accept(&c2, netCert(t, identity, rogueKey, 2)); verdict != SerialConflict {
		t.Fatalf("second key at serial 2: %v", verdict)
	}
}

func devCert(t *testing.T, root, device Signer, level admin.Level) DeviceCertificate {
	t.Helper()
	return DeviceCertificate{Person: personOf(t, root), DeviceKey: device.Public(), MaxLevel: level, NotBefore: epoch, NotAfter: epoch.Add(2 * time.Hour)}
}

func TestGoldenDeviceCertificate(t *testing.T) {
	root, device := rfcSigner(t, 3), rfcSigner(t, 2)
	cert := devCert(t, root, device, admin.LevelManage)
	rec, err := Seal(DeviceCertificateDescriptor, cert, root)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := rec.Encode()
	if !bytes.Equal(wire, goldenDeviceCertificate) {
		t.Fatalf("encoded certificate differs from the golden bytes\n got %x\nwant %x", wire, goldenDeviceCertificate)
	}
	if rec.Digest().String() != goldenDeviceCertificateDigest {
		t.Fatalf("digest = %s", rec.Digest())
	}
	if !bytes.Equal(SignedBytes(rec.Type, rec.Payload), mustHexBytes(t, goldenDeviceCertificateSignedBytes)) {
		t.Fatal("signed bytes differ from the golden bytes")
	}
	dec, err := Decode(goldenDeviceCertificate, DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(DeviceCertificateDescriptor, dec)
	if err != nil {
		t.Fatal(err)
	}
	if v.Payload != cert {
		t.Fatalf("payload = %+v, want %+v", v.Payload, cert)
	}
}

func TestDeviceCertificateRules(t *testing.T) {
	root, device, other := rfcSigner(t, 3), rfcSigner(t, 2), rfcSigner(t, 1)
	good := devCert(t, root, device, admin.LevelManage)

	t.Run("a certificate not signed by the person's root key is refused", func(t *testing.T) {
		if _, err := Seal(DeviceCertificateDescriptor, good, other); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a device cannot certify itself", func(t *testing.T) {
		if _, err := Seal(DeviceCertificateDescriptor, good, device); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a node identity key does not stand in for a root key", func(t *testing.T) {
		// The same key derives a node ID and a different person ID; a
		// certificate for the node-ID-shaped person is not signed by it.
		forged := good
		forged.Person, _ = ident.PersonIDFromBytes(nodeOf(t, root).Bytes())
		if _, err := Seal(DeviceCertificateDescriptor, forged, root); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a network-key certificate is not a device certificate", func(t *testing.T) {
		nk, _ := Seal(NetworkKeyCertificateDescriptor, netCert(t, root, device, 1), root)
		if _, err := Verify(DeviceCertificateDescriptor, nk); !errors.Is(err, ErrType) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("validity is not judged here: an expired certificate still verifies", func(t *testing.T) {
		old := good
		old.NotBefore, old.NotAfter = time.Unix(1000, 0), time.Unix(2000, 0)
		rec, err := Seal(DeviceCertificateDescriptor, old, root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(DeviceCertificateDescriptor, rec); err != nil {
			t.Fatalf("error = %v", err)
		}
	})

	bad := []struct {
		rule string
		mut  func(*DeviceCertificate)
		want error
	}{
		{"level none", func(c *DeviceCertificate) { c.MaxLevel = admin.LevelNone }, ErrLevel},
		{"level out of range", func(c *DeviceCertificate) { c.MaxLevel = admin.Level(4) }, ErrLevel},
		{"negative level", func(c *DeviceCertificate) { c.MaxLevel = admin.Level(-1) }, ErrLevel},
		{"zero person", func(c *DeviceCertificate) { c.Person = ident.PersonID{} }, ident.ErrZero},
		{"zero device key", func(c *DeviceCertificate) { c.DeviceKey = ident.PublicKey{} }, ErrKey},
		{"zero not_before", func(c *DeviceCertificate) { c.NotBefore = time.Time{} }, ErrTime},
		{"zero not_after", func(c *DeviceCertificate) { c.NotAfter = time.Time{} }, ErrTime},
		{"inverted window", func(c *DeviceCertificate) { c.NotAfter = c.NotBefore }, ErrTime},
	}
	for _, r := range bad {
		t.Run(r.rule+" is refused", func(t *testing.T) {
			c := good
			r.mut(&c)
			if _, err := Seal(DeviceCertificateDescriptor, c, root); !errors.Is(err, r.want) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
		})
	}

	person, key := personOf(t, root).Bytes(), cborw.key(1, device.Public().Bytes())
	payload := func(level uint64) []byte {
		return cborw.mapOf(cborw.uint(1), cborw.bstr(person), cborw.uint(2), key, cborw.uint(3), cborw.uint(level), cborw.uint(4), cborw.uint(1700000000), cborw.uint(5), cborw.uint(1700007200))
	}
	for _, level := range []uint64{0, 4, 255} {
		t.Run("wire level outside 1..3 is refused", func(t *testing.T) {
			rec, _ := Sign(DeviceCertificateType, payload(level), root)
			if _, err := Verify(DeviceCertificateDescriptor, rec); !errors.Is(err, ErrLevel) {
				t.Fatalf("level %d: error = %v", level, err)
			}
		})
	}
	t.Run("a certificate that states no cap must state full control", func(t *testing.T) {
		rec, _ := Sign(DeviceCertificateType, cborw.mapOf(cborw.uint(1), cborw.bstr(person), cborw.uint(2), key, cborw.uint(4), cborw.uint(1700000000), cborw.uint(5), cborw.uint(1700007200)), root)
		if _, err := Verify(DeviceCertificateDescriptor, rec); err == nil {
			t.Fatal("a certificate without a level was accepted")
		}
	})
}

func TestDeviceCertificateToAdminFacts(t *testing.T) {
	root, device := rfcSigner(t, 3), rfcSigner(t, 2)
	for _, level := range []admin.Level{admin.LevelView, admin.LevelManage, admin.LevelFullControl} {
		t.Run(level.String()+" cap is carried over", func(t *testing.T) {
			c := devCert(t, root, device, level)
			got := c.AdminCert()
			want := admin.DeviceCert{MaxLevel: level, NotBefore: c.NotBefore, NotAfter: c.NotAfter}
			if got != want {
				t.Fatalf("AdminCert = %+v, want %+v", got, want)
			}
			if got.CertRevoked || got.RootKeyRevoked {
				t.Fatal("revocation flags are filled in later, from the revocation sets")
			}
		})
	}
	t.Run("the facts are the ones admin judges: valid inside the window only", func(t *testing.T) {
		c := devCert(t, root, device, admin.LevelFullControl)
		list, problems := admin.NewList([]admin.Entry{{Person: c.Person, Level: admin.LevelFullControl}})
		if len(problems) != 0 {
			t.Fatal(problems)
		}
		if got := admin.EffectiveLevel(list, c.Person, c.AdminCert(), c.NotBefore.Add(time.Minute)); got.Level != admin.LevelFullControl {
			t.Fatalf("inside the window: %+v", got)
		}
		if got := admin.EffectiveLevel(list, c.Person, c.AdminCert(), c.NotAfter); got.Level != admin.LevelNone {
			t.Fatalf("at expiry: %+v", got)
		}
	})
	t.Run("a view cap limits a full-control list entry", func(t *testing.T) {
		c := devCert(t, root, device, admin.LevelView)
		list, _ := admin.NewList([]admin.Entry{{Person: c.Person, Level: admin.LevelFullControl}})
		if got := admin.EffectiveLevel(list, c.Person, c.AdminCert(), c.NotBefore.Add(time.Minute)); got.Level != admin.LevelView {
			t.Fatalf("standing = %+v", got)
		}
	})
}
