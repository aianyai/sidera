package record

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
)

func pairOf(t *testing.T) pairRecord {
	t.Helper()
	return pairRecord{First: nodeOf(t, rfcSigner(t, 1)), Second: nodeOf(t, rfcSigner(t, 2))}
}

func TestGoldenPairRecord(t *testing.T) {
	p := pairOf(t)
	s1, s2 := rfcSigner(t, 1), rfcSigner(t, 2)

	rec, err := Seal(pairDescriptor, p, s1, s2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, goldenPairComplete) {
		t.Fatalf("encoded record differs from the golden bytes\n got %x\nwant %x", got, goldenPairComplete)
	}
	if hex := rec.Digest().String(); hex != goldenPairCompleteDigest {
		t.Fatalf("digest = %s, want %s", hex, goldenPairCompleteDigest)
	}
	if !bytes.Equal(rec.Payload, mustHexBytes(t, goldenPairCompletePayload)) {
		t.Fatalf("payload = %x", rec.Payload)
	}
	if !bytes.Equal(SignedBytes(rec.Type, rec.Payload), mustHexBytes(t, goldenPairCompleteSignedBytes)) {
		t.Fatal("signed bytes differ from the golden bytes")
	}
	// The lowest encoded key (RFC 8032 key 2) is listed first although key 1
	// signed first.
	if rec.Signatures[0].Key != s2.Public() || rec.Signatures[1].Key != s1.Public() {
		t.Fatal("signatures are not sorted by encoded public key")
	}

	dec, err := Decode(goldenPairComplete, DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(pairDescriptor, dec)
	if err != nil {
		t.Fatal(err)
	}
	if v.Payload != p || len(v.Signers) != 2 || v.Digest.String() != goldenPairCompleteDigest {
		t.Fatalf("verified = %+v", v)
	}
}

// One party signs, the other appends; the result is the complete record.
func TestPartialSignatures(t *testing.T) {
	p := pairOf(t)
	s1, s2 := rfcSigner(t, 1), rfcSigner(t, 2)

	first, err := Seal(pairDescriptor, p, s1)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := first.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire, goldenPairFirstOnly) {
		t.Fatalf("partial record differs from the golden bytes\n got %x\nwant %x", wire, goldenPairFirstOnly)
	}

	t.Run("Verify refuses a record whose signer set is incomplete", func(t *testing.T) {
		if _, err := Verify(pairDescriptor, first); !errors.Is(err, ErrIncomplete) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("VerifyPartial accepts it and reports it incomplete", func(t *testing.T) {
		v, err := VerifyPartial(pairDescriptor, first)
		if err != nil || v.Complete() {
			t.Fatalf("VerifyPartial = %+v, %v", v, err)
		}
		if v.Digest.String() != goldenPairFirstOnlyDigest || v.Digest != first.Digest() {
			t.Fatal("digest of the partial record is not the digest of the record")
		}
	})
	t.Run("a partial result lists the slots in the rule's order with who filled them", func(t *testing.T) {
		v, err := VerifyPartial(pairDescriptor, first)
		if err != nil {
			t.Fatal(err)
		}
		want := []SlotState{{Signed: true, Signer: s1.Public()}, {}}
		if len(v.Slots) != 2 || v.Slots[0] != want[0] || v.Slots[1] != want[1] || v.Payload != p {
			t.Fatalf("Slots = %+v", v.Slots)
		}
		recv, _ := Decode(goldenPairComplete, DefaultMaxInput)
		v, err = VerifyPartial(pairDescriptor, recv)
		if err != nil || !v.Complete() {
			t.Fatalf("complete record: %+v, %v", v, err)
		}
		// The record lists key 2 first, but slot 0 is the first node's slot.
		if v.Slots[0].Signer != s1.Public() || v.Slots[1].Signer != s2.Public() {
			t.Fatalf("slots are not in the rule's order: %+v", v.Slots)
		}
	})
	t.Run("a partial result cannot stand in for a verified one", func(t *testing.T) {
		pt := reflect.TypeOf(VerifyPartial[pairRecord]).Out(0)
		vt := reflect.TypeOf(Verify[pairRecord]).Out(0)
		if pt == vt || pt.AssignableTo(vt) || pt.ConvertibleTo(vt) {
			t.Fatalf("%v can be used as %v", pt, vt)
		}
		// A complete record is still only a Partial until Verify accepts it.
		if _, err := Verify(pairDescriptor, first); !errors.Is(err, ErrIncomplete) {
			t.Fatalf("Verify of an incomplete record: %v", err)
		}
	})
	t.Run("the digest does not depend on how many signatures are attached", func(t *testing.T) {
		if goldenPairFirstOnlyDigest != goldenPairCompleteDigest {
			t.Fatal("golden digests differ")
		}
	})
	t.Run("the second party completes the record the first party sent", func(t *testing.T) {
		recv, err := Decode(wire, DefaultMaxInput)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyPartial(pairDescriptor, recv); err != nil {
			t.Fatal(err)
		}
		done, err := recv.CoSign(s2)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(pairDescriptor, done); err != nil {
			t.Fatal(err)
		}
		out, _ := done.Encode()
		if !bytes.Equal(out, goldenPairComplete) {
			t.Fatal("co-signed record is not the golden complete record")
		}
	})
	t.Run("a signer cannot sign the same record twice", func(t *testing.T) {
		if _, err := first.CoSign(s1); !errors.Is(err, ErrDuplicateSigner) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("the first signer may not sign for the other slot too", func(t *testing.T) {
		if _, err := Seal(pairDescriptor, p, s1, s1); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a partial record signed by someone outside the rule is refused", func(t *testing.T) {
		rec, err := Sign(pairType, mustHexBytes(t, goldenPairCompletePayload), rfcSigner(t, 3))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyPartial(pairDescriptor, rec); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
}

// payload and signature for decode-level tests; the signature need not verify.
func decodeFixtures(t *testing.T) (payload, pub1, pub2, sig []byte) {
	t.Helper()
	return mustHexBytes(t, goldenPairCompletePayload),
		rfcSigner(t, 1).Public().Bytes(), rfcSigner(t, 2).Public().Bytes(), bytes.Repeat([]byte{0x11}, 64)
}

func TestDecodeRejects(t *testing.T) {
	payload, pub1, pub2, sig := decodeFixtures(t)
	good := envelope(pairType, payload, sigItem(pub2, sig), sigItem(pub1, sig))
	p256 := cborw.key(2, pub1)
	pOverflow := cborw.key(257, pub1) // 257 wraps to the Ed25519 tag in a byte
	items := func(n int) [][]byte {
		var out [][]byte
		for i := 0; i < n; i++ {
			k := seedSigner(t, byte(i+1)).Public().Bytes()
			out = append(out, sigItem(k, sig))
		}
		return out
	}
	// Order of signatures by encoded key: sort the two real keys.
	lo, hi := pub1, pub2
	if bytes.Compare(lo, hi) > 0 {
		lo, hi = hi, lo
	}
	keyPlus := func(b []byte) []byte { return cborw.mapOf(cborw.uint(1), b, cborw.uint(2), cborw.bstr(sig)) }
	smallOrderItem := sigItem(mustHexBytes(t, "0100000000000000000000000000000000000000000000000000000000000000"), sig)
	repeatItem := func(n int, item []byte) [][]byte {
		out := make([][]byte, n)
		for i := range out {
			out[i] = item
		}
		return out
	}
	rows := []struct {
		rule string
		in   []byte
		max  int
		want error
	}{
		{"a well-formed envelope is accepted", good, DefaultMaxInput, nil},
		{"trailing bytes", append(append([]byte(nil), good...), 0x00), DefaultMaxInput, ErrMalformed},
		{"input over the size limit", good, len(good) - 1, ErrOversize},
		{"a limit of zero leaves no room", good, 0, ErrOversize},
		{"empty input", nil, DefaultMaxInput, ErrMalformed},
		{"duplicate map key in the envelope",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), cborw.bstr(payload), cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrMalformed},
		{"indefinite-length map", append(append([]byte{0xbf}, good[1:]...), 0xff), DefaultMaxInput, ErrMalformed},
		{"indefinite-length byte string payload",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), append(append([]byte{0x5f}, cborw.bstr(payload)...), 0xff), cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrMalformed},
		{"indefinite-length signature array",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), cborw.bstr(payload), cborw.uint(3), append(append([]byte{0x9f}, sigItem(pub1, sig)...), 0xff)),
			DefaultMaxInput, ErrMalformed},
		{"tag around the envelope", append([]byte{0xc1}, good...), DefaultMaxInput, ErrMalformed},
		{"tag around the payload",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), append([]byte{0xc2}, cborw.bstr(payload)...), cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrMalformed},
		{"unknown field in the envelope",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), cborw.bstr(payload), cborw.uint(3), cborw.arr(sigItem(pub1, sig)), cborw.uint(4), cborw.uint(0)),
			DefaultMaxInput, ErrMalformed},
		{"unknown field in a signature item",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), cborw.bstr(payload), cborw.uint(3),
				cborw.arr(cborw.mapOf(cborw.uint(1), cborw.key(1, pub1), cborw.uint(2), cborw.bstr(sig), cborw.uint(3), cborw.uint(1)))),
			DefaultMaxInput, ErrMalformed},
		{"unknown field in a public key",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), cborw.bstr(payload), cborw.uint(3),
				cborw.arr(cborw.mapOf(cborw.uint(1), cborw.mapOf(cborw.uint(1), cborw.uint(1), cborw.uint(2), cborw.bstr(pub1), cborw.uint(3), cborw.uint(1)), cborw.uint(2), cborw.bstr(sig)))),
			DefaultMaxInput, ErrMalformed},
		{"missing signature list",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), cborw.bstr(payload)),
			DefaultMaxInput, ErrNotCanonical},
		{"missing payload",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrNotCanonical},
		{"null payload",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), []byte{0xf6}, cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrNotCanonical},
		{"non-shortest integer key",
			cborw.mapOf([]byte{0x18, 0x01}, cborw.tstr(pairType), cborw.uint(2), cborw.bstr(payload), cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrNotCanonical},
		{"non-shortest length of the type string",
			cborw.mapOf(cborw.uint(1), append([]byte{0x78, byte(len(pairType))}, pairType...), cborw.uint(2), cborw.bstr(payload), cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrNotCanonical},
		{"map keys out of order",
			cborw.mapOf(cborw.uint(2), cborw.bstr(payload), cborw.uint(1), cborw.tstr(pairType), cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrNotCanonical},
		{"type is not text",
			cborw.mapOf(cborw.uint(1), cborw.bstr([]byte(pairType)), cborw.uint(2), cborw.bstr(payload), cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrMalformed},
		{"type with invalid UTF-8",
			cborw.mapOf(cborw.uint(1), append([]byte{0x62}, 0xff, 0xfe), cborw.uint(2), cborw.bstr(payload), cborw.uint(3), cborw.arr(sigItem(pub1, sig))),
			DefaultMaxInput, ErrMalformed},
		{"empty type", envelope("", payload, sigItem(pub1, sig)), DefaultMaxInput, ErrType},
		{"over-long type", envelope(strings.Repeat("t", MaxTypeLen+1), payload, sigItem(pub1, sig)), DefaultMaxInput, ErrType},
		{"empty payload", envelope(pairType, nil, sigItem(pub1, sig)), DefaultMaxInput, ErrPayload},
		{"no signatures", envelope(pairType, payload), DefaultMaxInput, ErrSignatureCount},
		// Every key below is small order, so converting any of them fails with a
		// key error. The count error winning shows that no key was converted
		// first: surplus signatures cost the decoder no curve work.
		{"too many signatures are refused before any key is decoded", envelope(pairType, payload, repeatItem(MaxSignatures+1, smallOrderItem)...), DefaultMaxInput, ErrSignatureCount},
		{"far too many signatures are refused before any key is decoded", envelope(pairType, payload, repeatItem(500, smallOrderItem)...), DefaultMaxInput, ErrSignatureCount},
		{"the largest allowed number of signatures reaches key decoding", envelope(pairType, payload, repeatItem(MaxSignatures, smallOrderItem)...), DefaultMaxInput, ident.ErrKeySmallOrder},
		{"more than the allowed number of signatures", envelope(pairType, payload, items(MaxSignatures+1)...), DefaultMaxInput, ErrSignatureCount},
		{"the same signer twice", envelope(pairType, payload, sigItem(pub1, sig), sigItem(pub1, sig)), DefaultMaxInput, ErrDuplicateSigner},
		{"signatures not sorted by encoded key", envelope(pairType, payload, sigItem(hi, sig), sigItem(lo, sig)), DefaultMaxInput, ErrSignatureOrder},
		{"signatures sorted by encoded key", envelope(pairType, payload, sigItem(lo, sig), sigItem(hi, sig)), DefaultMaxInput, nil},
		{"signature one byte short", envelope(pairType, payload, sigItem(pub1, sig[:63])), DefaultMaxInput, ErrSignature},
		{"signature one byte long", envelope(pairType, payload, sigItem(pub1, append(sig, 0))), DefaultMaxInput, ErrSignature},
		{"reserved P-256 algorithm tag", envelope(pairType, payload, keyPlus(p256)), DefaultMaxInput, ident.ErrAlgorithm},
		{"algorithm tag that wraps to Ed25519 in a byte", envelope(pairType, payload, keyPlus(pOverflow)), DefaultMaxInput, ident.ErrAlgorithm},
		{"unset algorithm tag", envelope(pairType, payload, keyPlus(cborw.key(0, pub1))), DefaultMaxInput, ident.ErrAlgorithm},
		{"public key one byte short", envelope(pairType, payload, sigItem(pub1[:31], sig)), DefaultMaxInput, ident.ErrKeyLength},
		{"non-canonical public key (y = p+1)",
			envelope(pairType, payload, sigItem(mustHexBytes(t, "eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"), sig)),
			DefaultMaxInput, ident.ErrKeyNotCanonical},
		{"public key that is not a point",
			envelope(pairType, payload, sigItem(mustHexBytes(t, "0200000000000000000000000000000000000000000000000000000000000000"), sig)),
			DefaultMaxInput, ident.ErrKeyNotCanonical},
		{"small-order public key (identity point)",
			envelope(pairType, payload, sigItem(mustHexBytes(t, "0100000000000000000000000000000000000000000000000000000000000000"), sig)),
			DefaultMaxInput, ident.ErrKeySmallOrder},
		{"small-order public key (order 8)",
			envelope(pairType, payload, sigItem(mustHexBytes(t, "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05"), sig)),
			DefaultMaxInput, ident.ErrKeySmallOrder},
		{"nesting deeper than the limit",
			cborw.mapOf(cborw.uint(1), cborw.tstr(pairType), cborw.uint(2), cborw.bstr(payload), cborw.uint(3), bytes.Repeat([]byte{0x81}, 20)),
			DefaultMaxInput, ErrMalformed},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec, err := Decode(r.in, r.max)
			if !errors.Is(err, r.want) && !(r.want == nil && err == nil) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
			if err == nil {
				back, err := rec.Encode()
				if err != nil || !bytes.Equal(back, r.in) {
					t.Fatalf("accepted input does not re-encode identically: %v", err)
				}
			}
		})
	}
}

func TestVerifyRejects(t *testing.T) {
	p := pairOf(t)
	s1, s2, s3 := rfcSigner(t, 1), rfcSigner(t, 2), rfcSigner(t, 3)
	payload := mustHexBytes(t, goldenPairCompletePayload)
	good, err := Seal(pairDescriptor, p, s1, s2)
	if err != nil {
		t.Fatal(err)
	}
	withSigs := func(sigs ...Signature) Record {
		return Record{Type: good.Type, Payload: good.Payload, Signatures: sigs}
	}
	sigOf := func(n int, domain string) Signature {
		priv := rfcPriv(t, n)
		pub, _ := ident.NewPublicKey(ident.AlgorithmEd25519, priv.Public().(ed25519.PublicKey))
		msg := framedByHand(t, domain, pairType, payload)
		return Signature{Key: pub, Sig: ed25519.Sign(priv, msg)}
	}
	sortedPair := func(a, b Signature) Record {
		if bytes.Compare(sortKey(a.Key), sortKey(b.Key)) > 0 {
			a, b = b, a
		}
		return withSigs(a, b)
	}

	t.Run("a correctly signed record is accepted", func(t *testing.T) {
		if _, err := Verify(pairDescriptor, good); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a record of another type is refused", func(t *testing.T) {
		if _, err := Verify(pairDescriptorV2, good); !errors.Is(err, ErrType) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("the same payload under a different type does not verify", func(t *testing.T) {
		other, err := Seal(pairDescriptorV2, p, s1, s2)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(other.Payload, good.Payload) {
			t.Fatal("test setup: payloads differ")
		}
		if other.Digest() == good.Digest() {
			t.Fatal("the digest ignores the type")
		}
		relabelled := Record{Type: pairType, Payload: other.Payload, Signatures: other.Signatures}
		if _, err := Verify(pairDescriptor, relabelled); !errors.Is(err, ErrSignature) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a signature over another signing domain does not verify", func(t *testing.T) {
		for _, d := range []string{"sidera/other", "libp2p-peer-record", testDigestDomain} {
			if _, err := Verify(pairDescriptor, sortedPair(sigOf(1, d), sigOf(2, testSigningDomain))); !errors.Is(err, ErrSignature) {
				t.Fatalf("domain %q: error = %v", d, err)
			}
		}
		if _, err := Verify(pairDescriptor, sortedPair(sigOf(1, testSigningDomain), sigOf(2, testSigningDomain))); err != nil {
			t.Fatalf("control: %v", err)
		}
	})
	t.Run("the same signer twice is refused", func(t *testing.T) {
		s := sigOf(1, testSigningDomain)
		if _, err := Verify(pairDescriptor, withSigs(s, s)); !errors.Is(err, ErrDuplicateSigner) {
			t.Fatalf("error = %v", err)
		}
		if _, err := withSigs(s, s).Encode(); !errors.Is(err, ErrDuplicateSigner) {
			t.Fatalf("Encode error = %v", err)
		}
	})
	t.Run("a signature scalar written as S+L is refused", func(t *testing.T) {
		s := sigOf(1, testSigningDomain)
		s.Sig = addGroupOrder(s.Sig)
		if _, err := Verify(pairDescriptor, sortedPair(s, sigOf(2, testSigningDomain))); !errors.Is(err, ErrSignature) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a flipped signature bit is refused", func(t *testing.T) {
		s := sigOf(1, testSigningDomain)
		s.Sig[10] ^= 1
		if _, err := Verify(pairDescriptor, sortedPair(s, sigOf(2, testSigningDomain))); !errors.Is(err, ErrSignature) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a third, unrelated signer is refused", func(t *testing.T) {
		r, _ := good.CoSign(s3)
		if _, err := Verify(pairDescriptor, r); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a signer outside the rule in place of a required one is refused", func(t *testing.T) {
		r, _ := Sign(pairType, payload, s1, s3)
		if _, err := Verify(pairDescriptor, r); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a record with a missing required signer is incomplete", func(t *testing.T) {
		r, _ := Sign(pairType, payload, s2)
		if _, err := Verify(pairDescriptor, r); !errors.Is(err, ErrIncomplete) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a record with no signatures is refused", func(t *testing.T) {
		if _, err := Verify(pairDescriptor, withSigs()); !errors.Is(err, ErrSignatureCount) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a signature with no key is refused", func(t *testing.T) {
		if _, err := Verify(pairDescriptor, withSigs(Signature{Sig: make([]byte, 64)})); !errors.Is(err, ErrKey) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("the zero descriptor fails closed", func(t *testing.T) {
		if _, err := Verify(Descriptor[pairRecord]{}, good); !errors.Is(err, ErrDescriptor) {
			t.Fatalf("Verify error = %v", err)
		}
		if _, err := Seal(Descriptor[pairRecord]{}, p, s1); !errors.Is(err, ErrDescriptor) {
			t.Fatalf("Seal error = %v", err)
		}
	})
	t.Run("a payload the type refuses is refused when it is sealed", func(t *testing.T) {
		same := pairRecord{First: p.First, Second: p.First}
		if _, err := Seal(pairDescriptor, same, s1); !errors.Is(err, ErrPayload) {
			t.Fatalf("Seal error = %v", err)
		}
	})
	t.Run("a payload the type refuses is refused when it is received", func(t *testing.T) {
		both := cborw.mapOf(cborw.uint(1), cborw.bstr(p.First.Bytes()), cborw.uint(2), cborw.bstr(p.First.Bytes()))
		bad, err := Sign(pairType, both, s1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyPartial(pairDescriptor, bad); !errors.Is(err, ErrPayload) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("Seal refuses a signer the record type does not allow", func(t *testing.T) {
		if _, err := Seal(pairDescriptor, p, s3); !errors.Is(err, ErrSignerNotAllowed) {
			t.Fatalf("error = %v", err)
		}
	})
}

// addGroupOrder returns the signature with S replaced by S+L.
func addGroupOrder(sig []byte) []byte {
	l, _ := new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	s := new(big.Int).SetBytes(reverseBytes(sig[32:]))
	s.Add(s, l)
	out := append([]byte(nil), sig[:32]...)
	return append(out, reverseBytes(s.FillBytes(make([]byte, 32)))...)
}

func reverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

// Every single-bit change anywhere in an encoded record must be refused by
// Decode or Verify: type, payload, keys, signatures and framing are all
// covered.
func TestEveryBitOfARecordIsProtected(t *testing.T) {
	cases := []struct {
		name string
		wire []byte
		d    func([]byte) error
	}{
		{"pair", goldenPairComplete, func(b []byte) error {
			r, err := Decode(b, DefaultMaxInput)
			if err != nil {
				return err
			}
			_, err = Verify(pairDescriptor, r)
			return err
		}},
		{"network-key certificate", goldenNetworkKeyCertificate, func(b []byte) error {
			r, err := Decode(b, DefaultMaxInput)
			if err != nil {
				return err
			}
			_, err = Verify(NetworkKeyCertificateDescriptor, r)
			return err
		}},
		{"device certificate", goldenDeviceCertificate, func(b []byte) error {
			r, err := Decode(b, DefaultMaxInput)
			if err != nil {
				return err
			}
			_, err = Verify(DeviceCertificateDescriptor, r)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.d(tc.wire); err != nil {
				t.Fatalf("control: %v", err)
			}
			tried := 0
			for i := range tc.wire {
				for bit := 0; bit < 8; bit++ {
					m := append([]byte(nil), tc.wire...)
					m[i] ^= 1 << bit
					tried++
					if err := tc.d(m); err == nil {
						t.Fatalf("byte %d bit %d changed and the record was still accepted", i, bit)
					}
				}
			}
			if tried != 8*len(tc.wire) {
				t.Fatalf("tried %d flips", tried)
			}
		})
	}
}

func TestEnvelopeRules(t *testing.T) {
	t.Run("signed bytes and digest are the domain, type and payload, each behind its length", func(t *testing.T) {
		payload := []byte{1, 2, 3}
		wantSigned := framedByHand(t, testSigningDomain, "x/1", payload)
		if got := SignedBytes("x/1", payload); !bytes.Equal(got, wantSigned) {
			t.Fatalf("SignedBytes = %x, want %x", got, wantSigned)
		}
		wantDigest := sha256.Sum256(framedByHand(t, testDigestDomain, "x/1", payload))
		if got := DigestOf("x/1", payload); got != Digest(wantDigest) {
			t.Fatalf("DigestOf = %s, want %x", got, wantDigest)
		}
	})
	t.Run("the framing of signed bytes and digest is the one the format always had", func(t *testing.T) {
		// A network-key certificate payload, its signed bytes and digest as
		// first written down, before the framing moved into package frame.
		// Recomputed with the independent reference implementation.
		const typ = "sidera/network-key-cert/1"
		payload := mustHexBytes(t, "a50158208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f38202a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0301041a6553f100051a657b7e00")
		const wantSigned = "117369646572612f7265636f72642d736967197369646572612f6e6574776f726b2d6b65792d636572742f3159a50158208bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f38202a201010258203d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c0301041a6553f100051a657b7e00"
		const wantDigest = "fa7d58c643363d6ba9c2fceec7f84ad56f051e561f6aac8f14b1b064a7816a0f"
		if got := SignedBytes(typ, payload); !bytes.Equal(got, mustHexBytes(t, wantSigned)) {
			t.Fatalf("SignedBytes = %x", got)
		}
		if got := DigestOf(typ, payload).String(); got != wantDigest {
			t.Fatalf("DigestOf = %s", got)
		}
	})
	t.Run("signed bytes start with a length below 64, never n or l", func(t *testing.T) {
		b := SignedBytes("x/1", []byte{1})
		if b[0] >= 64 || b[0] == 'n' || b[0] == 'l' {
			t.Fatalf("first byte %d", b[0])
		}
	})
	t.Run("signing and digest domains differ", func(t *testing.T) {
		if testSigningDomain == testDigestDomain {
			t.Fatal("same domain")
		}
		if DigestOf("x/1", []byte{1}) == (Digest{}) {
			t.Fatal("zero digest")
		}
		if bytes.Equal(SignedBytes("x/1", []byte{1}), framedByHand(t, testDigestDomain, "x/1", []byte{1})) {
			t.Fatal("signed bytes use the digest domain")
		}
	})
	t.Run("the digest changes with the type and with the payload", func(t *testing.T) {
		base := DigestOf("a/1", []byte{1, 2})
		if base == DigestOf("a/2", []byte{1, 2}) || base == DigestOf("a/1", []byte{1, 3}) {
			t.Fatal("digest ignores part of its input")
		}
	})
	t.Run("field boundaries cannot shift between type and payload", func(t *testing.T) {
		if DigestOf("ab", []byte("c")) == DigestOf("a", []byte("bc")) {
			t.Fatal("digest input is ambiguous")
		}
		if !bytes.Equal(SignedBytes("ab", []byte("c")), SignedBytes("ab", []byte("c"))) ||
			bytes.Equal(SignedBytes("ab", []byte("c")), SignedBytes("a", []byte("bc"))) {
			t.Fatal("signed bytes are ambiguous")
		}
	})
	t.Run("Sign needs a type, a payload and a signer", func(t *testing.T) {
		s := rfcSigner(t, 1)
		if _, err := Sign("", []byte{1}, s); !errors.Is(err, ErrType) {
			t.Fatalf("type: %v", err)
		}
		if _, err := Sign("a/1", nil, s); !errors.Is(err, ErrPayload) {
			t.Fatalf("payload: %v", err)
		}
		if _, err := Sign("a/1", []byte{1}); !errors.Is(err, ErrSignatureCount) {
			t.Fatalf("signers: %v", err)
		}
	})
	t.Run("Sign copies the payload", func(t *testing.T) {
		pl := []byte{1, 2, 3}
		r, _ := Sign("a/1", pl, rfcSigner(t, 1))
		pl[0] = 9
		if r.Payload[0] != 1 {
			t.Fatal("record shares the caller's payload")
		}
	})
}

type brokenSigner struct {
	pub ident.PublicKey
	sig []byte
	err error
}

func (b brokenSigner) Public() ident.PublicKey     { return b.pub }
func (b brokenSigner) Sign([]byte) ([]byte, error) { return b.sig, b.err }

func TestSignerFailuresAreCaughtAtTheSigner(t *testing.T) {
	good := rfcSigner(t, 1)
	rows := []struct {
		rule string
		s    Signer
		want error
	}{
		{"a signer that errors", brokenSigner{pub: good.Public(), err: errors.New("hsm offline")}, ErrSigner},
		{"a signer that returns a short signature", brokenSigner{pub: good.Public(), sig: []byte{1}}, ErrSigner},
		{"a signer that returns a signature of another key", brokenSigner{pub: good.Public(), sig: make([]byte, 64)}, ErrSigner},
		{"a signer without a key", brokenSigner{}, ErrKey},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if _, err := Sign("a/1", []byte{1}, r.s); !errors.Is(err, r.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	t.Run("an Ed25519 signer needs a full private key", func(t *testing.T) {
		if _, err := NewEd25519Signer(ed25519.PrivateKey{1, 2}); !errors.Is(err, ErrKey) {
			t.Fatalf("error = %v", err)
		}
	})
}

// A payload is validated in one place, the decoding conversion. Encoding runs
// that path on its own output, so a conversion to wire form that is more lenient
// than the decoding one cannot produce a payload a receiver would refuse.
func TestSealRunsTheDecodingPath(t *testing.T) {
	s := rfcSigner(t, 1)
	t.Run("an empty text passes the lenient encoding conversion and is refused", func(t *testing.T) {
		if _, err := noteDescriptor.encode(noteRecord{Text: ""}); !errors.Is(err, ErrPayload) {
			t.Fatalf("encode error = %v", err)
		}
		if _, err := Seal(noteDescriptor, noteRecord{Text: ""}, s); !errors.Is(err, ErrPayload) {
			t.Fatalf("Seal error = %v", err)
		}
	})
	t.Run("a valid payload is sealed and read back unchanged", func(t *testing.T) {
		in := noteRecord{Text: "hello", NotAfter: epoch}
		rec, err := Seal(noteDescriptor, in, s)
		if err != nil {
			t.Fatal(err)
		}
		v, err := Verify(noteDescriptor, rec)
		if err != nil || v.Payload != in {
			t.Fatalf("Verify = %+v, %v", v, err)
		}
	})
}

func TestMalformedDescriptorPanics(t *testing.T) {
	ok := func(pairRecord) (pairWire, error) { return pairWire{}, nil }
	back := func(pairWire) (pairRecord, error) { return pairRecord{}, nil }
	sl := func(pairRecord) []SignerSlot { return nil }
	rows := []struct {
		rule string
		f    func()
	}{
		{"empty type", func() { NewDescriptor("", ok, back, sl) }},
		{"over-long type", func() { NewDescriptor(strings.Repeat("x", MaxTypeLen+1), ok, back, sl) }},
		{"nil encoder", func() { NewDescriptor[pairWire, pairRecord]("a/1", nil, back, sl) }},
		{"nil decoder", func() { NewDescriptor[pairWire, pairRecord]("a/1", ok, nil, sl) }},
		{"nil signer rule", func() { NewDescriptor[pairWire, pairRecord]("a/1", ok, back, nil) }},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			r.f()
		})
	}
}

var _ = ident.ErrZero
