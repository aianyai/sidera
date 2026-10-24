package pairing

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/frame"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

const vectorProtocol = "/sidera/pairing/1.0.0"

var (
	vectorCreated  = time.Unix(1798761600, 0).UTC()
	vectorNotAfter = time.Unix(1830297600, 0).UTC()
)

// vectorParties are the two ends of the vectors: the initiator A has the
// identity key TEST 1, the network key TEST 2 and serial 1; the target B has
// TEST 3, TEST SHA(abc) and serial 7.
func vectorParties(t testing.TB) (a, b Party) {
	return partyFrom(t, seedTest1, seedTest2, 1), partyFrom(t, seedTest3, seedTestSHA, 7)
}

func vectorTerms(t testing.TB, notAfter time.Time) Terms {
	a, b := vectorParties(t)
	return Terms{Protocol: vectorProtocol, Initiator: a, Target: b, CreatedAt: vectorCreated, NotAfter: notAfter}
}

// The pairing identifier of the first and second vector, from the independent
// implementation: every stage of the construction is compared, so a mistake is
// located and not only detected.
func TestTranscriptVectors(t *testing.T) {
	a, b := vectorParties(t)
	if !bytes.Equal(a.Node().Bytes(), v(t, "A.node")) || !bytes.Equal(b.Node().Bytes(), v(t, "B.node")) {
		t.Fatal("node identifiers differ from the independent derivation")
	}
	tests := []struct {
		name     string
		prefix   string
		notAfter time.Time
	}{
		{"vector 1: no not_after", "v1", time.Time{}},
		{"vector 2: with not_after", "v2", vectorNotAfter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			terms := vectorTerms(t, tt.notAfter)
			nA, nB := nonceOf(0x00), nonceOf(0x20)

			if got := framedFields(terms.fields()); !bytes.Equal(got, v(t, tt.prefix+".params")) {
				t.Fatalf("params = %x", got)
			}
			c, err := terms.Commit(nA)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(c[:], v(t, tt.prefix+".c")) {
				t.Fatalf("commitment = %x, want %s", c, vec[tt.prefix+".c"])
			}
			tr, err := Open(terms, c, nB, nA)
			if err != nil {
				t.Fatal(err)
			}
			id := tr.ID()
			if !bytes.Equal(id[:], v(t, tt.prefix+".pid")) {
				t.Fatalf("pairing id = %x, want %s", id, vec[tt.prefix+".pid"])
			}
			body := append(terms.fields(), c[:], nB[:], nA[:])
			if !bytes.Equal(framedFields(body), v(t, tt.prefix+".body")) {
				t.Fatalf("body differs")
			}
			if want := vecInts[tt.prefix+".cands"]; len(want) != 8 {
				t.Fatalf("vector has %d candidates", len(want))
			}
			if got, want := tr.Code().String(), vec[tt.prefix+".code"][:3]+" "+vec[tt.prefix+".code"][3:]; got != want {
				t.Fatalf("code = %q, want %q", got, want)
			}
			nonce := tr.Credential().Nonce
			if !bytes.Equal(nonce[:], v(t, tt.prefix+".nonce")) {
				t.Fatalf("credential nonce = %x, want %s", nonce, vec[tt.prefix+".nonce"])
			}
			payload, err := tr.Credential().Payload()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(payload, v(t, tt.prefix+".payload")) {
				t.Fatalf("credential payload = %x, want %s", payload, vec[tt.prefix+".payload"])
			}
			digest, err := tr.Credential().Digest()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(digest[:], v(t, tt.prefix+".digest")) {
				t.Fatalf("credential digest = %x", digest)
			}
		})
	}
}

// framedFields concatenates fields as the construction does, each behind its
// uvarint length, written here without package frame so the test does not
// borrow the rule it checks.
func framedFields(fields [][]byte) []byte {
	var out []byte
	for _, f := range fields {
		out = append(out, uvarintBytes(len(f))...)
		out = append(out, f...)
	}
	return out
}

func uvarintBytes(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

// The credential records of the vectors: the initiator signs first, the target
// completes, and the bytes equal what the independent implementation signed.
func TestCredentialRecordVectors(t *testing.T) {
	for _, prefix := range []string{"v1", "v2"} {
		notAfter := time.Time{}
		if prefix == "v2" {
			notAfter = vectorNotAfter
		}
		terms := vectorTerms(t, notAfter)
		nA, nB := nonceOf(0x00), nonceOf(0x20)
		c, _ := terms.Commit(nA)
		tr, err := Open(terms, c, nB, nA)
		if err != nil {
			t.Fatal(err)
		}
		cred := tr.Credential()
		first, err := record.Seal(credential.Descriptor, cred, signerFromSeed(t, seedTest1))
		if err != nil {
			t.Fatal(err)
		}
		enc, _ := first.Encode()
		if !bytes.Equal(enc, v(t, prefix+".partial")) {
			t.Fatalf("%s: partial record = %x", prefix, enc)
		}
		done, err := record.CoSign(credential.Descriptor, first, signerFromSeed(t, seedTest3))
		if err != nil {
			t.Fatal(err)
		}
		enc, _ = done.Encode()
		if !bytes.Equal(enc, v(t, prefix+".complete")) {
			t.Fatalf("%s: complete record = %x", prefix, enc)
		}
		if _, err := record.Verify(credential.Descriptor, done); err != nil {
			t.Fatal(err)
		}
	}
}

// The first candidate of this nB is at or above the limit and is rejected; the
// second is used. nB_i = SHA-256("sidera-pairing-reject-vector" || u32be(i)),
// i being the smallest index for which this happens with the vector terms.
func TestRejectionVectorEndToEnd(t *testing.T) {
	if vecInts["reject.i"] != nil || vec["reject.i"] != "1725" {
		t.Fatalf("rejection vector index = %s, the test below assumes 1725", vec["reject.i"])
	}
	terms := vectorTerms(t, time.Time{})
	nA := nonceOf(0x00)
	var nB Nonce
	copy(nB[:], v(t, "reject.nB"))
	// The nonce really is the hash of the documented input.
	sum := sha256.Sum256(append([]byte("sidera-pairing-reject-vector"), 0, 0, 0x06, 0xbd)) // 1725 = 0x06bd
	if sum != nB {
		t.Fatalf("nB is not SHA-256(label || u32be(1725))")
	}
	c, _ := terms.Commit(nA)
	tr, err := Open(terms, c, nB, nA)
	if err != nil {
		t.Fatal(err)
	}
	id := tr.ID()
	if !bytes.Equal(id[:], v(t, "reject.pid")) {
		t.Fatalf("pairing id = %x", id)
	}
	if got, want := tr.Code().String(), "490 876"; got != want {
		t.Fatalf("code = %q, want %q (second candidate)", got, want)
	}
	if vecInts["reject.cands"][0] < 4294000000 {
		t.Fatal("vector is not a rejection vector")
	}
}

func TestSampleCodeUnitVectors(t *testing.T) {
	be := func(vals ...uint32) []byte {
		out := make([]byte, 32)
		for i, x := range vals {
			out[4*i], out[4*i+1], out[4*i+2], out[4*i+3] = byte(x>>24), byte(x>>16), byte(x>>8), byte(x)
		}
		return out
	}
	tests := []struct {
		name   string
		okm    []byte
		code   Code
		accept bool
	}{
		{"4293999999 is accepted and gives 999999", be(4293999999), 999999, true},
		{"4294000000 is rejected, the next candidate is used", be(4294000000, 7), 7, true},
		{"all 0xFF: all eight candidates rejected", bytes.Repeat([]byte{0xff}, 32), 0, false},
		{"five rejections then the sixth candidate is used", be(4294000000, 4294000000, 4294000000, 4294000000, 4294000000, 1234567), 234567, true},
		{"only the last candidate is acceptable", be(4294000000, 4294000000, 4294000000, 4294000000, 4294000000, 4294000000, 4294000000, 5), 5, true},
		{"the first acceptable candidate wins over later ones", be(1000000, 2000000), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, ok := sampleCode(tt.okm)
			if ok != tt.accept || code != tt.code {
				t.Fatalf("sampleCode = (%d, %v), want (%d, %v)", code, ok, tt.code, tt.accept)
			}
		})
	}
	// The same unit vectors as the independent implementation printed them.
	if got := vecInts["unit.4293999999"]; got[0] != 999999 || got[1] != 0 {
		t.Fatalf("generator disagrees: %v", got)
	}
	if got := vecInts["unit.allff"]; got[0] != -1 {
		t.Fatalf("generator disagrees: %v", got)
	}
}

// An independently written sampler: rejection by comparison with the largest
// multiple of 10^6 below 2^32, written in terms of big-endian arithmetic.
func TestSampleCodeAgainstIndependentSampler(t *testing.T) {
	ref := func(okm []byte) (int64, bool) {
		for i := 0; i < 8; i++ {
			x := int64(okm[4*i])<<24 | int64(okm[4*i+1])<<16 | int64(okm[4*i+2])<<8 | int64(okm[4*i+3])
			if x < (int64(1)<<32)/1000000*1000000 {
				return x % 1000000, true
			}
		}
		return 0, false
	}
	seed := sha256.Sum256([]byte("sampler"))
	for n := 0; n < 20000; n++ {
		seed = sha256.Sum256(seed[:])
		got, gotOK := sampleCode(seed[:])
		want, wantOK := ref(seed[:])
		if gotOK != wantOK || int64(got) != want {
			t.Fatalf("input %x: got (%d,%v) want (%d,%v)", seed, got, gotOK, want, wantOK)
		}
	}
}

// Frame is the one rule behind every concatenation in the construction; its
// length prefix changes shape at 128 bytes (one varint byte to two).
func TestLengthPrefixBoundary(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 129, 16383, 16384} {
		field := bytes.Repeat([]byte{'a'}, n)
		framed := frame.Frame("sidera/test/lp", field)
		prefix := v(t, "lp."+itoa(n)+".prefix")
		domainLen := 1 + len("sidera/test/lp")
		if !bytes.Equal(framed[domainLen:domainLen+len(prefix)], prefix) {
			t.Fatalf("%d: prefix = %x, want %x", n, framed[domainLen:domainLen+len(prefix)], prefix)
		}
		sum := sha256.Sum256(framed)
		if !bytes.Equal(sum[:], v(t, "lp."+itoa(n)+".sha256")) {
			t.Fatalf("%d: digest differs from the independent framing", n)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

func TestNegativeVectors(t *testing.T) {
	a, b := vectorParties(t)
	nA, nB := nonceOf(0x00), nonceOf(0x20)
	open := func(terms Terms) Transcript {
		t.Helper()
		c, err := terms.Commit(nA)
		if err != nil {
			t.Fatal(err)
		}
		tr, err := Open(terms, c, nB, nA)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	base := vectorTerms(t, time.Time{})
	baseID := open(base).ID()

	t.Run("swapped quadruples give another pairing id", func(t *testing.T) {
		swapped := base
		swapped.Initiator, swapped.Target = b, a
		id := open(swapped).ID()
		if id == baseID || !bytes.Equal(id[:], v(t, "swapped.pid")) {
			t.Fatalf("swapped id = %x", id)
		}
		c, _ := swapped.Commit(nA)
		if !bytes.Equal(c[:], v(t, "swapped.c")) {
			t.Fatalf("swapped commitment = %x", c)
		}
	})
	t.Run("created_at changed by one second", func(t *testing.T) {
		m := base
		m.CreatedAt = base.CreatedAt.Add(time.Second)
		id := open(m).ID()
		if !bytes.Equal(id[:], v(t, "created_plus_one.pid")) {
			t.Fatalf("id = %x", id)
		}
	})
	t.Run("other protocol identifier", func(t *testing.T) {
		m := base
		m.Protocol = "/sidera/pairing/1.0.1"
		id := open(m).ID()
		if !bytes.Equal(id[:], v(t, "other_protocol.pid")) {
			t.Fatalf("id = %x", id)
		}
	})
	t.Run("serial of the target changed", func(t *testing.T) {
		m := base
		m.Target = partyFrom(t, seedTest3, seedTestSHA, 8)
		id := open(m).ID()
		if !bytes.Equal(id[:], v(t, "serial_plus_one.pid")) {
			t.Fatalf("id = %x", id)
		}
	})
	t.Run("identity and network key of the initiator exchanged", func(t *testing.T) {
		m := base
		m.Initiator = partyFrom(t, seedTest2, seedTest1, 1)
		id := open(m).ID()
		if !bytes.Equal(id[:], v(t, "keyroles_swapped.pid")) {
			t.Fatalf("id = %x", id)
		}
	})
}

// Every input of the transcript changes the pairing identifier, the code and
// the commitment check: a changed byte of either nonce, of the commitment or
// of any field of the terms.
func TestEveryInputByteChangesThePairingID(t *testing.T) {
	terms := vectorTerms(t, vectorNotAfter)
	nA, nB := nonceOf(0x00), nonceOf(0x20)
	c, _ := terms.Commit(nA)
	base, err := Open(terms, c, nB, nA)
	if err != nil {
		t.Fatal(err)
	}
	for i := range nB {
		m := nB
		m[i] ^= 1
		tr, err := Open(terms, c, m, nA)
		if err != nil {
			continue
		}
		if tr.ID() == base.ID() {
			t.Fatalf("nB byte %d does not change the id", i)
		}
	}
	for i := range nA {
		m := nA
		m[i] ^= 1
		if _, err := Open(terms, c, nB, m); !errors.Is(err, ErrCommitment) {
			t.Fatalf("nA byte %d: err = %v, want the commitment to fail", i, err)
		}
	}
	for i := range c {
		m := c
		m[i] ^= 1
		if _, err := Open(terms, m, nB, nA); !errors.Is(err, ErrCommitment) {
			t.Fatalf("commitment byte %d: err = %v", i, err)
		}
	}
	// Each field of the terms, one byte at a time, through the commitment and
	// the identifier (the field list is what both are made of).
	fields := terms.fields()
	seen := map[ID]bool{base.ID(): true}
	for fi := range fields {
		for bi := range fields[fi] {
			mutated := make([][]byte, len(fields))
			for k := range fields {
				mutated[k] = append([]byte(nil), fields[k]...)
			}
			mutated[fi][bi] ^= 1
			body := append(mutated, c[:], nB[:], nA[:])
			id := ID(sha256.Sum256(frame.Frame(transcriptDomain, body...)))
			if seen[id] {
				t.Fatalf("field %d byte %d: identifier repeats", fi, bi)
			}
			seen[id] = true
		}
	}
}

func TestOpenRefusals(t *testing.T) {
	terms := vectorTerms(t, time.Time{})
	nA, nB := nonceOf(0x00), nonceOf(0x20)
	c, _ := terms.Commit(nA)
	tests := []struct {
		name string
		do   func() error
		want error
	}{
		{"zero nA", func() error { _, e := Open(terms, c, nB, Nonce{}); return e }, ErrNonce},
		{"zero nB", func() error { _, e := Open(terms, c, Nonce{}, nA); return e }, ErrNonce},
		{"zero commitment", func() error { _, e := Open(terms, Commitment{}, nB, nA); return e }, ErrCommitment},
		{"no protocol identifier", func() error { m := terms; m.Protocol = ""; _, e := Open(m, c, nB, nA); return e }, ErrTerms},
		{"protocol identifier too long", func() error {
			m := terms
			m.Protocol = strings.Repeat("p", MaxProtocolIDLen+1)
			_, e := Open(m, c, nB, nA)
			return e
		}, ErrTerms},
		{"pairing with itself", func() error { m := terms; m.Target = m.Initiator; _, e := Open(m, c, nB, nA); return e }, ErrTerms},
		{"unset target", func() error { m := terms; m.Target = Party{}; _, e := Open(m, c, nB, nA); return e }, ErrTerms},
		{"sub-second created_at", func() error {
			m := terms
			m.CreatedAt = m.CreatedAt.Add(time.Millisecond)
			_, e := Open(m, c, nB, nA)
			return e
		}, ErrTerms},
		{"not_after not after created_at", func() error {
			m := terms
			m.NotAfter = m.CreatedAt
			_, e := Open(m, c, nB, nA)
			return e
		}, ErrTerms},
		{"created_at unset", func() error { m := terms; m.CreatedAt = time.Time{}; _, e := Open(m, c, nB, nA); return e }, ErrTerms},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.do(); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// A target that sees the keys differently from the initiator (another network
// key, another serial) recomputes another commitment and aborts.
func TestCommitmentBindsTheTargetsOwnView(t *testing.T) {
	terms := vectorTerms(t, time.Time{})
	nA, nB := nonceOf(0x00), nonceOf(0x20)
	c, _ := terms.Commit(nA)
	for name, mutate := range map[string]func(*Terms){
		"initiator network key": func(m *Terms) { m.Initiator = partyFrom(t, seedTest1, seedTest3, 1) },
		"initiator certificate": func(m *Terms) { m.Initiator = partyFrom(t, seedTest1, seedTest2, 2) },
		"target network key":    func(m *Terms) { m.Target = partyFrom(t, seedTest3, seedTest2, 7) },
		"negotiated protocol":   func(m *Terms) { m.Protocol += "x" },
		"not_after present":     func(m *Terms) { m.NotAfter = vectorNotAfter },
		"created_at":            func(m *Terms) { m.CreatedAt = m.CreatedAt.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			m := terms
			mutate(&m)
			if _, err := Open(m, c, nB, nA); !errors.Is(err, ErrCommitment) {
				t.Fatalf("err = %v, want ErrCommitment", err)
			}
		})
	}
}

func TestNewPartyRefusals(t *testing.T) {
	idSigner, netSigner := signerFromSeed(t, seedTest1), signerFromSeed(t, seedTest2)
	node, _ := ident.NodeIDFromKey(idSigner.Public())
	issue := func(netKey ident.PublicKey, serial uint64) record.Verified[record.NetworkKeyCertificate] {
		t.Helper()
		cert := record.IssueNetworkKeyCertificate(node, netKey, serial, testNow.Add(-time.Hour), testNow.Add(time.Hour))
		rec, err := record.Seal(record.NetworkKeyCertificateDescriptor, cert, idSigner)
		if err != nil {
			return record.Verified[record.NetworkKeyCertificate]{}
		}
		ver, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
		if err != nil {
			t.Fatal(err)
		}
		return ver
	}
	good := issue(netSigner.Public(), 1)
	tests := []struct {
		name string
		do   func() error
	}{
		{"identity key equal to the network key", func() error {
			_, e := NewParty(issue(idSigner.Public(), 1), record.KeyRevocationClear, 1, idSigner.Public(), testNow)
			return e
		}},
		{"connection authenticated another key", func() error {
			_, e := NewParty(good, record.KeyRevocationClear, 1, signerFromSeed(t, seedTest3).Public(), testNow)
			return e
		}},
		{"revocation never looked up", func() error {
			_, e := NewParty(good, record.KeyRevocationUnchecked, 1, netSigner.Public(), testNow)
			return e
		}},
		{"certificate revoked", func() error {
			_, e := NewParty(good, record.KeyRevocationRevoked, 1, netSigner.Public(), testNow)
			return e
		}},
		{"certificate expired", func() error {
			_, e := NewParty(good, record.KeyRevocationClear, 1, netSigner.Public(), testNow.Add(2*time.Hour))
			return e
		}},
		{"certificate not yet valid", func() error {
			_, e := NewParty(good, record.KeyRevocationClear, 1, netSigner.Public(), testNow.Add(-3*time.Hour))
			return e
		}},
		{"a higher serial was seen: the certificate is stale", func() error {
			_, e := NewParty(good, record.KeyRevocationClear, 2, netSigner.Public(), testNow)
			return e
		}},
		{"no highest serial given", func() error {
			_, e := NewParty(good, record.KeyRevocationClear, 0, netSigner.Public(), testNow)
			return e
		}},
		{"no certificate", func() error {
			_, e := NewParty(record.Verified[record.NetworkKeyCertificate]{}, record.KeyRevocationClear, 1, netSigner.Public(), testNow)
			return e
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.do(); !errors.Is(err, ErrParty) {
				t.Fatalf("err = %v, want ErrParty", err)
			}
		})
	}
	// The identity-equals-network case is refused already when the certificate
	// is sealed; the party constructor refuses the same pair for records read
	// back from storage.
	if _, err := partyOf(idSigner.Public(), idSigner.Public(), 1); !errors.Is(err, ErrParty) {
		t.Fatalf("partyOf with equal keys: %v", err)
	}
	if _, err := partyOf(idSigner.Public(), netSigner.Public(), 0); !errors.Is(err, ErrParty) {
		t.Fatalf("partyOf with serial 0: %v", err)
	}
}

func TestCodeText(t *testing.T) {
	tests := []struct {
		code Code
		text string
	}{
		{0, "000 000"}, {7, "000 007"}, {482019, "482 019"}, {999999, "999 999"}, {100000, "100 000"},
	}
	for _, tt := range tests {
		if got := tt.code.String(); got != tt.text {
			t.Fatalf("Code(%d).String() = %q, want %q", tt.code, got, tt.text)
		}
		for _, in := range []string{tt.text, strings.ReplaceAll(tt.text, " ", "")} {
			got, err := ParseCode(in)
			if err != nil || got != tt.code {
				t.Fatalf("ParseCode(%q) = %d, %v", in, got, err)
			}
		}
	}
	for _, bad := range []string{"", "12345", "1234567", "12 3456", "abc def", "123  456", "123-456", "12345 ", " 123456", "+12345", "123 4567"} {
		if _, err := ParseCode(bad); !errors.Is(err, ErrCode) {
			t.Fatalf("ParseCode(%q) = %v, want ErrCode", bad, err)
		}
	}
}

func TestNewNonce(t *testing.T) {
	if _, err := NewNonce(bytes.NewReader(make([]byte, NonceLen))); !errors.Is(err, ErrNonce) {
		t.Fatalf("zero reader: %v", err)
	}
	if _, err := NewNonce(bytes.NewReader([]byte{1, 2, 3})); !errors.Is(err, ErrNonce) {
		t.Fatalf("short reader: %v", err)
	}
	n, err := NewNonce(bytes.NewReader(bytes.Repeat([]byte{9}, NonceLen)))
	if err != nil || n.IsZero() {
		t.Fatalf("NewNonce = %v, %v", n, err)
	}
}

// The rule that a credential ends after it starts is the credential package's;
// the terms and the commitment message ask it and carry its error.
func TestTheWindowRuleIsTheCredentialPackagesOwn(t *testing.T) {
	terms := vectorTerms(t, vectorNotAfter)
	terms.NotAfter = terms.CreatedAt
	if err := terms.validate(); !errors.Is(err, ErrTerms) || !errors.Is(err, credential.ErrWindow) {
		t.Fatalf("terms: %v, want ErrTerms wrapping credential.ErrWindow", err)
	}
	c := Commitment{1}
	w := commitWire{Commit: c[:], CreatedAt: 100, NotAfter: ptr(int64(100))}
	b, err := record.MarshalCanonical(wrap(TypeCommit, w))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCommit(b); !errors.Is(err, ErrMessage) || !errors.Is(err, credential.ErrWindow) {
		t.Fatalf("M1: %v, want ErrMessage wrapping credential.ErrWindow", err)
	}
}
