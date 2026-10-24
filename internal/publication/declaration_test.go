package publication

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// sampleDeclaration is the declaration of goldenDeclaration. The endpoints are
// listed out of order on purpose: the wire form is sorted.
func sampleDeclaration(t testing.TB) Declaration {
	t.Helper()
	return Declaration{
		Node: nodeOf(t, keyA), Counter: 5, BootNonce: bootNonce(0xa0),
		AcceptsInheritance: true, OffersRelay: false,
		MaxQueue: delivery.MaxQueueCap, LeaseRenewal: delivery.LeaseRenewalCap,
		Endpoints: []EndpointDeclaration{
			{ID: epID(2), Kind: EndpointRuntime},
			{ID: epID(1), Kind: EndpointSession, Name: "main", Reception: Reception{Paused: true}},
		},
	}
}

func TestGoldenDeclaration(t *testing.T) {
	r := mustDecode(t, goldenDeclaration)
	v, err := record.Verify(DeclarationDescriptor(acceptedA(t), declNow), r)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	d := v.Payload()
	if d.Node != nodeOf(t, keyA) {
		t.Errorf("node = %s", d.Node)
	}
	if d.Counter != 5 || d.BootNonce != bootNonce(0xa0) || !d.AcceptsInheritance || d.OffersRelay {
		t.Errorf("payload = %+v", d)
	}
	if d.MaxQueue != 2*time.Hour || d.LeaseRenewal != 15*time.Minute {
		t.Errorf("limits = %v %v", d.MaxQueue, d.LeaseRenewal)
	}
	if len(d.Endpoints) != 2 || d.Endpoints[0].ID != epID(1) || d.Endpoints[0].Name != "main" || !d.Endpoints[0].Reception.Paused ||
		d.Endpoints[0].Kind != EndpointSession || d.Endpoints[1].ID != epID(2) || d.Endpoints[1].Kind != EndpointRuntime ||
		d.Endpoints[1].Reception != (Reception{}) || d.Endpoints[1].Name != "" {
		t.Errorf("endpoints = %+v", d.Endpoints)
	}
	if v.Digest().String() != goldenDeclarationDigest {
		t.Errorf("digest = %s", v.Digest())
	}
	if v.Signers()[0] != signerOf(t, keyNetA).Public() {
		t.Error("not signed by the network key")
	}
	node, accepts := d.DeclaredInheritance()
	if node != d.Node || !accepts {
		t.Errorf("inheritance fact = %v %v", node, accepts)
	}
}

func TestGoldenDeclarationIsWhatTheCodeWrites(t *testing.T) {
	r, err := record.Seal(DeclarationDescriptor(acceptedA(t), declNow), sampleDeclaration(t), signerOf(t, keyNetA))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEncode(t, r); !bytes.Equal(got, goldenDeclaration) {
		t.Errorf("declaration differs from the vector:\n got %x\nwant %x", got, goldenDeclaration)
	}
}

// Endpoint order given by the caller does not change the bytes.
func TestEndpointOrderDoesNotChangeTheBytes(t *testing.T) {
	d := sampleDeclaration(t)
	d2 := d
	d2.Endpoints = []EndpointDeclaration{d.Endpoints[1], d.Endpoints[0]}
	accepted := acceptedA(t)
	r1, err := record.Seal(DeclarationDescriptor(accepted, declNow), d, signerOf(t, keyNetA))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := record.Seal(DeclarationDescriptor(accepted, declNow), d2, signerOf(t, keyNetA))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustEncode(t, r1), mustEncode(t, r2)) {
		t.Error("the encoding depends on the order the endpoints were listed in")
	}
}

func TestDeclarationSignerBinding(t *testing.T) {
	d := sampleDeclaration(t)
	good := acceptedA(t)
	expired := acceptedA(t)
	c := *expired.Cert
	c.NotAfter = declNow
	expired.Cert = &c
	notYet := acceptedA(t)
	n := *notYet.Cert
	n.NotBefore = declNow.Add(time.Second)
	notYet.Cert = &n
	revoked := acceptedA(t)
	revoked.Revocation = record.KeyRevocationRevoked
	unchecked := acceptedA(t)
	unchecked.Revocation = record.KeyRevocationUnchecked
	otherNode := record.AcceptedNetworkKey{Cert: &record.NetworkKeyCertificate{
		Node: nodeOf(t, keyOther), NetworkKey: signerOf(t, keyNetA).Public(), Serial: 1, NotBefore: t0, NotAfter: t0.Add(time.Hour * 24),
	}, Revocation: record.KeyRevocationClear}
	tests := []struct {
		name     string
		accepted record.AcceptedNetworkKey
		signer   int
		want     record.Binding
	}{
		{"signed by the identity key instead of the network key", good, keyA, record.BindingKeyMismatch},
		{"signed by another node's network key", good, keyNetOther, record.BindingKeyMismatch},
		{"the accepted certificate is for another node", otherNode, keyNetA, record.BindingNodeMismatch},
		{"no certificate accepted", record.AcceptedNetworkKey{}, keyNetA, record.BindingNoCertificate},
		{"certificate expired at the instant", expired, keyNetA, record.BindingExpired},
		{"certificate not valid yet", notYet, keyNetA, record.BindingNotYetValid},
		{"certificate revoked", revoked, keyNetA, record.BindingRevoked},
		{"revocation never looked up", unchecked, keyNetA, record.BindingRevocationUnchecked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := mustDecode(t, goldenDeclaration).Payload
			forged, err := record.Sign(DeclarationType, payload, signerOf(t, tc.signer))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(DeclarationDescriptor(tc.accepted, declNow), forged)
			requireErr(t, err, record.ErrSignerNotAllowed)
			var be *record.BindingError
			if !errors.As(err, &be) || be.Binding != tc.want {
				t.Errorf("binding = %v, want %v", be, tc.want)
			}
			if _, err := record.Seal(DeclarationDescriptor(tc.accepted, declNow), d, signerOf(t, tc.signer)); err == nil {
				t.Error("Seal accepted the signer")
			}
		})
	}
}

func TestDeclarationVerifyRejects(t *testing.T) {
	desc := DeclarationDescriptor(acceptedA(t), declNow)
	payload := mustDecode(t, goldenDeclaration).Payload
	signed := func(typ string, signers ...int) record.Record {
		var ss []record.Signer
		for _, n := range signers {
			ss = append(ss, signerOf(t, n))
		}
		r, err := record.Sign(typ, payload, ss...)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	tests := []struct {
		name string
		r    record.Record
		want error
	}{
		{"extra signature: the identity key next to the network key", signed(DeclarationType, keyNetA, keyA), record.ErrSignerNotAllowed},
		{"wrong type string: next version", signed("sidera/node-declaration/2", keyNetA), record.ErrType},
		{"wrong type string: reachability record", signed(ReachabilityType, keyNetA), record.ErrType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := record.Verify(desc, tc.r)
			requireErr(t, err, tc.want)
		})
	}
	t.Run("missing signature", func(t *testing.T) {
		r := mustDecode(t, goldenDeclaration)
		r.Signatures = nil
		if _, err := record.Verify(desc, r); err == nil {
			t.Error("a declaration without a signature verified")
		}
	})
}

func goodDeclarationWire(t testing.TB) declarationWire {
	t.Helper()
	w, err := declarationToWire(sampleDeclaration(t))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func verifyWire(t *testing.T, w declarationWire) error {
	t.Helper()
	payload, err := record.MarshalCanonical(w)
	if err != nil {
		t.Fatal(err)
	}
	r, err := record.Sign(DeclarationType, payload, signerOf(t, keyNetA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = record.Verify(DeclarationDescriptor(acceptedA(t), declNow), r)
	return err
}

func TestDeclarationPayloadRules(t *testing.T) {
	sec := func(d time.Duration) uint64 { return uint64(d / time.Second) }
	tests := []struct {
		name   string
		mutate func(w *declarationWire)
		want   error
	}{
		{"valid", func(w *declarationWire) {}, nil},
		{"valid without endpoints", func(w *declarationWire) { w.Endpoints = []endpointWire{} }, nil},
		{"queue time at the protocol limit", func(w *declarationWire) { w.MaxQueueSecs = sec(delivery.MaxQueueCap) }, nil},
		{"queue time one second over the protocol limit", func(w *declarationWire) { w.MaxQueueSecs = sec(delivery.MaxQueueCap) + 1 }, ErrQueueTime},
		{"queue time far over the protocol limit", func(w *declarationWire) { w.MaxQueueSecs = 1 << 62 }, ErrQueueTime},
		{"queue time overflowing a duration", func(w *declarationWire) { w.MaxQueueSecs = ^uint64(0) }, ErrQueueTime},
		{"queue time that wraps around to a small duration", func(w *declarationWire) { w.MaxQueueSecs = 18446744074 }, ErrQueueTime},
		{"queue time zero", func(w *declarationWire) { w.MaxQueueSecs = 0 }, ErrQueueTime},
		{"queue time of one second", func(w *declarationWire) { w.MaxQueueSecs = 1 }, nil},
		{"lease renewal at the protocol limit", func(w *declarationWire) { w.LeaseRenewalSecs = sec(delivery.LeaseRenewalCap) }, nil},
		{"lease renewal one second over the protocol limit", func(w *declarationWire) { w.LeaseRenewalSecs = sec(delivery.LeaseRenewalCap) + 1 }, ErrLeaseRenewal},
		{"lease renewal overflowing a duration", func(w *declarationWire) { w.LeaseRenewalSecs = ^uint64(0) }, ErrLeaseRenewal},
		{"lease renewal that wraps around to a small duration", func(w *declarationWire) { w.LeaseRenewalSecs = 18446744074 }, ErrLeaseRenewal},
		{"lease renewal zero", func(w *declarationWire) { w.LeaseRenewalSecs = 0 }, ErrLeaseRenewal},
		{"node unset", func(w *declarationWire) { w.Node = make([]byte, 32) }, ident.ErrZero},
		{"counter zero", func(w *declarationWire) { w.Counter = 0 }, record.ErrCounter},
		{"boot nonce short", func(w *declarationWire) { w.BootNonce = w.BootNonce[:15] }, ErrBootNonce},
		{"boot nonce all zero", func(w *declarationWire) { w.BootNonce = make([]byte, BootNonceLen) }, ErrBootNonce},
		{"boot nonce missing", func(w *declarationWire) { w.BootNonce = nil }, ErrBootNonce},
		{"endpoint ID unset", func(w *declarationWire) { w.Endpoints[0].ID = make([]byte, 16) }, ident.ErrZero},
		{"endpoint repeated", func(w *declarationWire) { w.Endpoints[1].ID = w.Endpoints[0].ID }, ErrEndpoint},
		{"endpoints out of order", func(w *declarationWire) { w.Endpoints[0], w.Endpoints[1] = w.Endpoints[1], w.Endpoints[0] }, ErrEndpoint},
		{"endpoint kind zero", func(w *declarationWire) { w.Endpoints[0].Kind = 0 }, ErrEndpoint},
		{"endpoint kind unknown", func(w *declarationWire) { w.Endpoints[0].Kind = 3 }, ErrEndpoint},
		{"endpoint name at the limit", func(w *declarationWire) { w.Endpoints[0].Name = strings.Repeat("n", MaxNameLen) }, nil},
		{"endpoint name over the limit", func(w *declarationWire) { w.Endpoints[0].Name = strings.Repeat("n", MaxNameLen+1) }, ErrEndpoint},
		{"endpoint name that is not UTF-8", func(w *declarationWire) { w.Endpoints[0].Name = "a\xffb" }, record.ErrMalformed},
		{"endpoint name in another script", func(w *declarationWire) { w.Endpoints[0].Name = "代理一号" }, nil},
		{"unknown reception bit", func(w *declarationWire) { w.Endpoints[0].Reception = 1 << 4 }, ErrEndpoint},
		{"every reception bit", func(w *declarationWire) { w.Endpoints[0].Reception = 15 }, nil},
		{"too many endpoints", func(w *declarationWire) {
			w.Endpoints = nil
			for i := 0; i <= MaxEndpointsPerNode; i++ {
				id := make([]byte, 16)
				id[0], id[1] = byte(i>>8), byte(i)
				id[15] = 1
				w.Endpoints = append(w.Endpoints, endpointWire{ID: id, Kind: 1})
			}
		}, ErrEndpoint},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := goodDeclarationWire(t)
			tc.mutate(&w)
			err := verifyWire(t, w)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				return
			}
			if errors.Is(tc.want, record.ErrMalformed) {
				requireErr(t, err, tc.want)
				return
			}
			requireErr(t, err, record.ErrPayload, tc.want)
		})
	}
	t.Run("exactly the endpoint limit", func(t *testing.T) {
		w := goodDeclarationWire(t)
		w.Endpoints = nil
		for i := 0; i < MaxEndpointsPerNode; i++ {
			id := make([]byte, 16)
			id[0], id[1] = byte(i>>8), byte(i)
			id[15] = 1
			w.Endpoints = append(w.Endpoints, endpointWire{ID: id, Kind: 2})
		}
		if err := verifyWire(t, w); err != nil {
			t.Fatalf("rejected: %v", err)
		}
	})
}

// A declaration over the protocol limits is refused when it is made, at the
// signer's side, and when it is received.
func TestSealRefusesDeclarationsOverTheProtocolLimits(t *testing.T) {
	accepted := acceptedA(t)
	tests := []struct {
		name   string
		mutate func(d *Declaration)
		want   error
	}{
		{"queue time over the cap", func(d *Declaration) { d.MaxQueue = delivery.MaxQueueCap + time.Second }, ErrQueueTime},
		{"queue time not in whole seconds", func(d *Declaration) { d.MaxQueue = time.Second + time.Millisecond }, ErrQueueTime},
		{"queue time negative", func(d *Declaration) { d.MaxQueue = -time.Second }, ErrQueueTime},
		{"queue time unset", func(d *Declaration) { d.MaxQueue = 0 }, ErrQueueTime},
		{"lease renewal over the cap", func(d *Declaration) { d.LeaseRenewal = delivery.LeaseRenewalCap + time.Second }, ErrLeaseRenewal},
		{"lease renewal not in whole seconds", func(d *Declaration) { d.LeaseRenewal = 1500 * time.Millisecond }, ErrLeaseRenewal},
		{"lease renewal unset", func(d *Declaration) { d.LeaseRenewal = 0 }, ErrLeaseRenewal},
		{"endpoint kind unset", func(d *Declaration) { d.Endpoints[0].Kind = EndpointKindInvalid }, ErrEndpoint},
		{"endpoint listed twice", func(d *Declaration) { d.Endpoints = append(d.Endpoints, d.Endpoints[0]) }, ErrEndpoint},
		{"boot nonce unset", func(d *Declaration) { d.BootNonce = BootNonce{} }, ErrBootNonce},
		{"counter unset", func(d *Declaration) { d.Counter = 0 }, record.ErrCounter},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := sampleDeclaration(t)
			d.Endpoints = append([]EndpointDeclaration(nil), d.Endpoints...)
			tc.mutate(&d)
			_, err := record.Seal(DeclarationDescriptor(accepted, declNow), d, signerOf(t, keyNetA))
			requireErr(t, err, tc.want)
		})
	}
}

// An optional field written explicitly as its zero value is a second spelling
// of "not provided" and is refused.
func TestExplicitZeroOptionalFields(t *testing.T) {
	type explicitEndpoint struct {
		ID        []byte `cbor:"1,keyasint"`
		Kind      uint64 `cbor:"2,keyasint"`
		Name      string `cbor:"3,keyasint"`
		Reception uint64 `cbor:"4,keyasint"`
	}
	type explicitDeclaration struct {
		Node               []byte             `cbor:"1,keyasint"`
		Counter            uint64             `cbor:"2,keyasint"`
		BootNonce          []byte             `cbor:"3,keyasint"`
		AcceptsInheritance bool               `cbor:"4,keyasint"`
		OffersRelay        bool               `cbor:"5,keyasint"`
		MaxQueueSecs       uint64             `cbor:"6,keyasint"`
		LeaseRenewalSecs   uint64             `cbor:"7,keyasint"`
		Endpoints          []explicitEndpoint `cbor:"8,keyasint"`
	}
	base := goodDeclarationWire(t)
	mk := func(eps ...explicitEndpoint) []byte {
		b, err := record.MarshalCanonical(explicitDeclaration{
			Node: base.Node, Counter: base.Counter, BootNonce: base.BootNonce, AcceptsInheritance: true,
			MaxQueueSecs: base.MaxQueueSecs, LeaseRenewalSecs: base.LeaseRenewalSecs, Endpoints: eps,
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ep := func(name string, reception uint64) explicitEndpoint {
		return explicitEndpoint{ID: epID(1).Bytes(), Kind: 1, Name: name, Reception: reception}
	}
	tests := []struct {
		name    string
		payload []byte
	}{
		{"an explicit empty name", mk(ep("", 0x01))},
		{"an explicit zero reception", mk(ep("x", 0))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := record.Sign(DeclarationType, tc.payload, signerOf(t, keyNetA))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(DeclarationDescriptor(acceptedA(t), declNow), r)
			requireErr(t, err, record.ErrNotCanonical)
		})
	}
	// The same fields left out are fine.
	if _, err := record.Verify(DeclarationDescriptor(acceptedA(t), declNow), mustDecode(t, goldenDeclaration)); err != nil {
		t.Fatal(err)
	}
}

func TestDeclarationPayloadEncodingRules(t *testing.T) {
	good := mustDecode(t, goldenDeclaration).Payload
	// A required boolean must be written: dropping its key (here 5, offers_relay,
	// "f4" after "05") is not the canonical form of anything.
	dropped := bytes.Replace(good, []byte{0x05, 0xf4}, nil, 1)
	// The map head says 8 pairs; with a pair gone the head must say 7.
	dropped[0] = 0xa7
	wide := bytes.Replace(good, []byte{0x02, 0x05}, []byte{0x02, 0x18, 0x05}, 1)
	tests := []struct {
		name    string
		payload []byte
		want    error
	}{
		{"a required boolean left out", dropped, record.ErrNotCanonical},
		{"non-shortest counter", wide, record.ErrNotCanonical},
		{"trailing byte", append(append([]byte(nil), good...), 0), record.ErrMalformed},
		{"a boolean written as an integer", bytes.Replace(good, []byte{0x04, 0xf5}, []byte{0x04, 0x01}, 1), record.ErrMalformed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := record.Sign(DeclarationType, tc.payload, signerOf(t, keyNetA))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(DeclarationDescriptor(acceptedA(t), declNow), r)
			requireErr(t, err, tc.want)
		})
	}
}

// TestReceptionBits pins the wire value of every reception flag: the values are
// part of what a declaration means, so they are written out here and not derived.
func TestReceptionBits(t *testing.T) {
	tests := []struct {
		name string
		r    Reception
		bits uint64
	}{
		{"none", Reception{}, 0},
		{"paused", Reception{Paused: true}, 1},
		{"refuses several recipients", Reception{RefuseMultiRecipient: true}, 2},
		{"restricts senders", Reception{RestrictSenders: true}, 4},
		{"hidden from the directory", Reception{HiddenFromDirectory: true}, 8},
		{"all", Reception{true, true, true, true}, 15},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.bits(); got != tc.bits {
				t.Errorf("bits = %d, want %d", got, tc.bits)
			}
			back, err := receptionFromBits(tc.bits)
			if err != nil || back != tc.r {
				t.Errorf("round trip = %+v, %v", back, err)
			}
		})
	}
}

// The claimed node is read before verification, only to choose the context. It
// is whatever the sender wrote: a record with a forged signature still yields it.
func TestDeclarationNodeReadsTheClaimedNode(t *testing.T) {
	node, err := DeclarationNode(mustDecode(t, goldenDeclaration))
	if err != nil || node != nodeOf(t, keyA) {
		t.Fatalf("DeclarationNode = %v, %v", node, err)
	}
	forged, err := record.Sign(DeclarationType, mustDecode(t, goldenDeclaration).Payload, signerOf(t, keyOther))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DeclarationNode(forged); err != nil || got != nodeOf(t, keyA) {
		t.Errorf("a forged declaration claims %v, %v", got, err)
	}
	if _, err := record.Verify(DeclarationDescriptor(acceptedA(t), declNow), forged); err == nil {
		t.Error("the forged declaration verifies")
	}
}

func TestDeclarationNodeRefuses(t *testing.T) {
	bad := mustDecode(t, goldenDeclaration)
	bad.Payload = append([]byte{}, bad.Payload...)
	bad.Payload[len(bad.Payload)-1] ^= 0xff
	tests := []struct {
		name string
		r    record.Record
		want error
	}{
		{"another record type", mustDecode(t, goldenReachability), record.ErrType},
		{"a payload that does not decode", bad, nil},
		{"a zero record", record.Record{}, record.ErrType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DeclarationNode(tc.r)
			if err == nil {
				t.Fatal("accepted")
			}
			if tc.want != nil {
				requireErr(t, err, tc.want)
			}
		})
	}
}
