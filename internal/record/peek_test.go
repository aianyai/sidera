package record

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPeek(t *testing.T) {
	p := pairOf(t)
	s1, s2 := rfcSigner(t, 1), rfcSigner(t, 2)
	good, err := Seal(pairDescriptor, p, s1, s2)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a well-formed record of the type yields its payload", func(t *testing.T) {
		u, err := Peek(pairDescriptor, good)
		if err != nil || u.Payload != p {
			t.Fatalf("Peek = %+v, %v", u, err)
		}
	})
	t.Run("a record still lacking a signer yields its payload", func(t *testing.T) {
		first, err := Seal(pairDescriptor, p, s1)
		if err != nil {
			t.Fatal(err)
		}
		if u, err := Peek(pairDescriptor, first); err != nil || u.Payload != p {
			t.Fatalf("Peek = %+v, %v", u, err)
		}
	})
	t.Run("the payload is the one Verify returns", func(t *testing.T) {
		u, err := Peek(pairDescriptor, good)
		if err != nil {
			t.Fatal(err)
		}
		v, err := Verify(pairDescriptor, good)
		if err != nil || v.Payload != u.Payload {
			t.Fatalf("Verify payload %+v, Peek payload %+v, error %v", v.Payload, u.Payload, err)
		}
	})
	t.Run("a record of another type is refused", func(t *testing.T) {
		if _, err := Peek(pairDescriptorV2, good); !errors.Is(err, ErrType) {
			t.Fatalf("error = %v, want ErrType", err)
		}
	})
	t.Run("a zero descriptor is refused", func(t *testing.T) {
		if _, err := Peek(Descriptor[pairRecord]{}, good); !errors.Is(err, ErrDescriptor) {
			t.Fatalf("error = %v, want ErrDescriptor", err)
		}
	})
	t.Run("a payload that is not strictly encoded is refused", func(t *testing.T) {
		garbage, err := Sign(pairType, []byte{0xff, 0x00, 0x01}, s1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Peek(pairDescriptor, garbage); !errors.Is(err, ErrMalformed) {
			t.Fatalf("error = %v, want ErrMalformed", err)
		}
	})
	t.Run("a payload the type's validation refuses is refused", func(t *testing.T) {
		same, err := MarshalCanonical(pairWire{First: p.First.Bytes(), Second: p.First.Bytes()})
		if err != nil {
			t.Fatal(err)
		}
		rec, err := Sign(pairType, same, s1)
		if err != nil {
			t.Fatal(err)
		}
		_, peekErr := Peek(pairDescriptor, rec)
		if !errors.Is(peekErr, ErrPayload) {
			t.Fatalf("Peek error = %v, want ErrPayload", peekErr)
		}
		// The same decoding path as verification, not a second one.
		if _, verifyErr := VerifyPartial(pairDescriptor, rec); verifyErr == nil || verifyErr.Error() != peekErr.Error() {
			t.Fatalf("Peek error %q differs from VerifyPartial error %v", peekErr, verifyErr)
		}
	})
	t.Run("signatures are not checked: a forged signature still peeks, and Verify refuses the record", func(t *testing.T) {
		forged := Record{Type: good.Type, Payload: good.Payload, Signatures: append([]Signature(nil), good.Signatures...)}
		forged.Signatures[0].Sig = append([]byte(nil), forged.Signatures[0].Sig...)
		forged.Signatures[0].Sig[0] ^= 0xff
		if _, err := Verify(pairDescriptor, forged); !errors.Is(err, ErrSignature) {
			t.Fatalf("Verify error = %v, want ErrSignature", err)
		}
		if u, err := Peek(pairDescriptor, forged); err != nil || u.Payload != p {
			t.Fatalf("Peek = %+v, %v", u, err)
		}
	})
	t.Run("the signer rule is not applied: a signer the type does not allow still peeks", func(t *testing.T) {
		stranger := seedSigner(t, 9)
		rec, err := Sign(pairType, good.Payload, stranger)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(pairDescriptor, rec); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("Verify error = %v, want ErrSignerNotAllowed", err)
		}
		if u, err := Peek(pairDescriptor, rec); err != nil || u.Payload != p {
			t.Fatalf("Peek = %+v, %v", u, err)
		}
	})
}

// The use Peek exists for: a record whose signer rule depends on the node it
// names. The node comes from Peek, the context for it from the caller's own
// state, and only Verify's answer is trusted.
func TestPeekChoosesTheVerificationContext(t *testing.T) {
	identity, network := rfcSigner(t, 1), rfcSigner(t, 2)
	cert := netCert(t, identity, network, 1)
	node := cert.Node
	mid := epoch.Add(time.Hour)
	accepted := map[string]AcceptedNetworkKey{
		node.String(): {Cert: &cert, Revocation: KeyRevocationClear},
	}

	// The descriptor used for Seal is only for building the test record.
	rec, err := Seal(declDescriptor(accepted[node.String()], mid), declRecord{Node: node, Text: "hello"}, network)
	if err != nil {
		t.Fatal(err)
	}

	// A reader that has no descriptor context yet peeks with any descriptor of
	// the type: the payload decoding does not depend on the context.
	u, err := Peek(declDescriptor(AcceptedNetworkKey{}, mid), rec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, ok := accepted[u.Payload.Node.String()]
	if !ok {
		t.Fatal("the claimed node has no accepted certificate")
	}
	v, err := Verify(declDescriptor(ctx, mid), rec)
	if err != nil || v.Payload.Text != "hello" {
		t.Fatalf("Verify = %+v, %v", v, err)
	}
	// With no context for the claimed node, verification refuses the record.
	if _, err := Verify(declDescriptor(AcceptedNetworkKey{}, mid), rec); !errors.Is(err, ErrSignerNotAllowed) {
		t.Fatalf("Verify without a context: %v", err)
	}
}

func TestUnverifiedIsADistinctType(t *testing.T) {
	u := reflect.TypeOf(Unverified[pairRecord]{})
	for _, other := range []reflect.Type{
		reflect.TypeOf(Verified[pairRecord]{}),
		reflect.TypeOf(Partial[pairRecord]{}),
	} {
		if u.AssignableTo(other) || u.ConvertibleTo(other) {
			t.Errorf("Unverified is assignable or convertible to %v", other)
		}
		if other.AssignableTo(u) || other.ConvertibleTo(u) {
			t.Errorf("%v is assignable or convertible to Unverified", other)
		}
	}
}
