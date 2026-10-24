package pairing

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/record"
)

func vectorMessages(t *testing.T) (c Commitment, id ID, partial, complete record.Record) {
	t.Helper()
	copy(c[:], v(t, "v1.c"))
	copy(id[:], v(t, "v1.pid"))
	var err error
	if partial, err = record.Decode(v(t, "v1.partial"), MaxMessage); err != nil {
		t.Fatal(err)
	}
	if complete, err = record.Decode(v(t, "v1.complete"), MaxMessage); err != nil {
		t.Fatal(err)
	}
	return c, id, partial, complete
}

// Each message is a canonical CBOR map whose key 0 is its type. The expected
// bytes come from the independent implementation; the diagnostic notation is
// given for the short ones.
func TestMessageGoldenVectors(t *testing.T) {
	c, id, partial, complete := vectorMessages(t)
	nB, nA := nonceOf(0x20), nonceOf(0x00)
	created := time.Unix(1798761600, 0).UTC()
	tests := []struct {
		name string
		key  string
		enc  func() ([]byte, error)
		dec  func([]byte) (any, error)
		want any
	}{
		{"M1 without not_after: {0: type, 1: C, 2: created_at}", "msg.m1",
			CommitMsg{Commitment: c, CreatedAt: created}.Encode,
			func(b []byte) (any, error) { return DecodeCommit(b) }, CommitMsg{Commitment: c, CreatedAt: created}},
		{"M1 with not_after", "msg.m1_not_after",
			CommitMsg{Commitment: c, CreatedAt: created, NotAfter: vectorNotAfter}.Encode,
			func(b []byte) (any, error) { return DecodeCommit(b) }, CommitMsg{Commitment: c, CreatedAt: created, NotAfter: vectorNotAfter}},
		{"M2: {0: type, 1: nB}", "msg.m2", RevealMsg{NB: nB}.Encode,
			func(b []byte) (any, error) { return DecodeReveal(b) }, RevealMsg{NB: nB}},
		{"M3: {0: type, 1: nA}", "msg.m3", OpenMsg{NA: nA}.Encode,
			func(b []byte) (any, error) { return DecodeOpen(b) }, OpenMsg{NA: nA}},
		{"M4: {0: type, 1: pairing_id, 2: expires_at}", "msg.m4", AcceptedMsg{ID: id, ExpiresAt: created.Add(time.Hour)}.Encode,
			func(b []byte) (any, error) { return DecodeAccepted(b) }, AcceptedMsg{ID: id, ExpiresAt: created.Add(time.Hour)}},
		{"refusal paused with retry_after 90", "msg.refused_paused", RefusalMsg{Reason: RefusalPaused, RetryAfter: 90 * time.Second}.Encode,
			func(b []byte) (any, error) { return DecodeRefusal(b) }, RefusalMsg{Reason: RefusalPaused, RetryAfter: 90 * time.Second}},
		{"refusal not_accepted without retry_after", "msg.refused_not_accepted", RefusalMsg{Reason: RefusalNotAccepted}.Encode,
			func(b []byte) (any, error) { return DecodeRefusal(b) }, RefusalMsg{Reason: RefusalNotAccepted}},
		{"F1 poll", "msg.f1_poll", FetchMsg{ID: id}.Encode,
			func(b []byte) (any, error) { return DecodeFetch(b) }, FetchMsg{ID: id}},
		{"F1 withdraw mismatch", "msg.f1_withdraw_mismatch", FetchMsg{ID: id, Withdraw: WithdrawMismatch}.Encode,
			func(b []byte) (any, error) { return DecodeFetch(b) }, FetchMsg{ID: id, Withdraw: WithdrawMismatch}},
		{"F1 with the initiator's partial credential", "msg.f1_signed", FetchMsg{ID: id, Signed: &partial}.Encode,
			func(b []byte) (any, error) { return DecodeFetch(b) }, FetchMsg{ID: id, Signed: &partial}},
		{"F2 awaiting_target with retry_after 5", "msg.f2_awaiting_target", ResultMsg{ID: id, Status: StatusAwaitingTarget, RetryAfter: 5 * time.Second}.Encode,
			func(b []byte) (any, error) { return DecodeResult(b) }, ResultMsg{ID: id, Status: StatusAwaitingTarget, RetryAfter: 5 * time.Second}},
		{"F2 complete with the credential", "msg.f2_complete", ResultMsg{ID: id, Status: StatusComplete, Credential: &complete}.Encode,
			func(b []byte) (any, error) { return DecodeResult(b) }, ResultMsg{ID: id, Status: StatusComplete, Credential: &complete}},
		{"F2 rejected with mismatch", "msg.f2_rejected_mismatch", ResultMsg{ID: id, Status: StatusRejected, Rejection: WithdrawMismatch}.Encode,
			func(b []byte) (any, error) { return DecodeResult(b) }, ResultMsg{ID: id, Status: StatusRejected, Rejection: WithdrawMismatch}},
		{"F2 unknown", "msg.f2_unknown", ResultMsg{ID: id, Status: StatusUnknown}.Encode,
			func(b []byte) (any, error) { return DecodeResult(b) }, ResultMsg{ID: id, Status: StatusUnknown}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := v(t, tt.key)
			got, err := tt.enc()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("encoded = %x\nwant      %x", got, want)
			}
			msg, err := tt.dec(want)
			if err != nil {
				t.Fatal(err)
			}
			if !sameMessage(t, msg, tt.want) {
				t.Fatalf("decoded = %+v, want %+v", msg, tt.want)
			}
		})
	}
}

func sameMessage(t *testing.T, a, b any) bool {
	t.Helper()
	// Messages compare by their encoding; the records inside are pointers.
	ea, eb := mustEncodeAny(t, a), mustEncodeAny(t, b)
	return bytes.Equal(ea, eb)
}

func mustEncodeAny(t *testing.T, m any) []byte {
	t.Helper()
	type encoder interface{ Encode() ([]byte, error) }
	b, err := m.(encoder).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The diagnostic notation of the shortest messages, written out by hand, so a
// reader sees the shape without trusting the hex.
func TestMessageShapeByHand(t *testing.T) {
	// M2 = {0: "sidera/pairing-reveal/1", 1: h'2021...3f'}
	want := append([]byte{0xa2, 0x00, 0x77}, []byte("sidera/pairing-reveal/1")...)
	want = append(want, 0x01, 0x58, 0x20)
	n := nonceOf(0x20)
	want = append(want, n[:]...)
	got, err := RevealMsg{NB: nonceOf(0x20)}.Encode()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("M2 = %x, %v\nwant %x", got, err, want)
	}
}

func TestDecodersRefuse(t *testing.T) {
	c, id, partial, complete := vectorMessages(t)
	created := time.Unix(1798761600, 0).UTC()
	raw := func(m interface{ Encode() ([]byte, error) }) []byte {
		b, err := m.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	m2 := raw(RevealMsg{NB: nonceOf(0x20)})
	m3 := raw(OpenMsg{NA: nonceOf(0x00)})

	t.Run("a message of another type", func(t *testing.T) {
		if _, err := DecodeReveal(m3); !errors.Is(err, ErrMessage) {
			t.Fatalf("M3 read as M2: %v", err)
		}
		if _, err := DecodeOpen(m2); !errors.Is(err, ErrMessage) {
			t.Fatalf("M2 read as M3: %v", err)
		}
	})
	t.Run("truncated, extended and empty frames", func(t *testing.T) {
		for _, b := range [][]byte{nil, {}, m2[:len(m2)-1], append(append([]byte{}, m2...), 0), {0xa0}, {0xff}} {
			if _, err := DecodeReveal(b); err == nil {
				t.Fatalf("%x decoded", b)
			}
		}
	})
	t.Run("frame over the size bound", func(t *testing.T) {
		if _, err := DecodeReveal(make([]byte, MaxMessage+1)); !errors.Is(err, ErrMessage) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nonce of the wrong length or all zero", func(t *testing.T) {
		if _, err := record.MarshalCanonical(revealWire{Type: TypeReveal, NB: make([]byte, 31)}); err != nil {
			t.Fatal(err)
		}
		short, _ := record.MarshalCanonical(revealWire{Type: TypeReveal, NB: bytes.Repeat([]byte{1}, 31)})
		zero, _ := record.MarshalCanonical(revealWire{Type: TypeReveal, NB: make([]byte, 32)})
		for name, b := range map[string][]byte{"short": short, "zero": zero} {
			if _, err := DecodeReveal(b); !errors.Is(err, ErrMessage) {
				t.Fatalf("%s: %v", name, err)
			}
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		b, _ := record.MarshalCanonical(map[int]any{0: TypeReveal, 1: bytes.Repeat([]byte{7}, 32), 9: 1})
		if _, err := DecodeReveal(b); !errors.Is(err, ErrMessage) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("M1 times", func(t *testing.T) {
		for name, w := range map[string]commitWire{
			"created_at zero":        {Type: TypeCommit, Commit: c[:], CreatedAt: 0},
			"created_at negative":    {Type: TypeCommit, Commit: c[:], CreatedAt: -5},
			"not_after zero":         {Type: TypeCommit, Commit: c[:], CreatedAt: 100, NotAfter: ptr(int64(0))},
			"not_after = created_at": {Type: TypeCommit, Commit: c[:], CreatedAt: 100, NotAfter: ptr(int64(100))},
			"not_after before":       {Type: TypeCommit, Commit: c[:], CreatedAt: 100, NotAfter: ptr(int64(99))},
			"zero commitment":        {Type: TypeCommit, Commit: make([]byte, 32), CreatedAt: 100},
			"short commitment":       {Type: TypeCommit, Commit: c[:31], CreatedAt: 100},
		} {
			b, err := record.MarshalCanonical(w)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeCommit(b); !errors.Is(err, ErrMessage) {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if _, err := DecodeCommit(raw(CommitMsg{Commitment: c, CreatedAt: created})); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("explicit zero retry_after is refused", func(t *testing.T) {
		zero := int64(0)
		b, _ := record.MarshalCanonical(refusalWire{Type: TypeRefused, Reason: uint64(RefusalPaused), RetryAfter: &zero})
		if _, err := DecodeRefusal(b); !errors.Is(err, ErrMessage) {
			t.Fatalf("refusal: %v", err)
		}
		b, _ = record.MarshalCanonical(resultWire{Type: TypeResult, ID: id[:], Status: uint64(StatusAwaitingTarget), RetryAfter: &zero})
		if _, err := DecodeResult(b); !errors.Is(err, ErrMessage) {
			t.Fatalf("result: %v", err)
		}
		if _, err := (RefusalMsg{Reason: RefusalPaused, RetryAfter: time.Millisecond}).Encode(); !errors.Is(err, ErrMessage) {
			t.Fatalf("sub-second hint encoded: %v", err)
		}
	})
	t.Run("retry_after beyond the bound", func(t *testing.T) {
		big := int64(MaxRetryAfter/time.Second) + 1
		b, _ := record.MarshalCanonical(refusalWire{Type: TypeRefused, Reason: uint64(RefusalBusy), RetryAfter: &big})
		if _, err := DecodeRefusal(b); !errors.Is(err, ErrMessage) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("refusal reason out of range", func(t *testing.T) {
		for _, r := range []uint64{0, 6, 99} {
			b, _ := record.MarshalCanonical(refusalWire{Type: TypeRefused, Reason: r})
			if _, err := DecodeRefusal(b); !errors.Is(err, ErrMessage) {
				t.Fatalf("reason %d: %v", r, err)
			}
		}
	})
	t.Run("fetch cannot sign and withdraw", func(t *testing.T) {
		b, _ := record.MarshalCanonical(fetchWire{Type: TypeFetch, ID: id[:], Signed: v(t, "v1.partial"), Withdraw: uint64(WithdrawDeclined)})
		if _, err := DecodeFetch(b); !errors.Is(err, ErrMessage) {
			t.Fatalf("err = %v", err)
		}
		if _, err := (FetchMsg{ID: id, Signed: &partial, Withdraw: WithdrawDeclined}).Encode(); !errors.Is(err, ErrMessage) {
			t.Fatalf("encoded: %v", err)
		}
	})
	t.Run("fetch withdraw reason out of range", func(t *testing.T) {
		b, _ := record.MarshalCanonical(fetchWire{Type: TypeFetch, ID: id[:], Withdraw: 3})
		if _, err := DecodeFetch(b); !errors.Is(err, ErrMessage) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("result field rules", func(t *testing.T) {
		cred := v(t, "v1.complete")
		rows := map[string]resultWire{
			"credential without complete": {Type: TypeResult, ID: id[:], Status: uint64(StatusAwaitingTarget), Credential: cred},
			"complete without credential": {Type: TypeResult, ID: id[:], Status: uint64(StatusComplete)},
			"rejected without reason":     {Type: TypeResult, ID: id[:], Status: uint64(StatusRejected)},
			"reason without rejected":     {Type: TypeResult, ID: id[:], Status: uint64(StatusExpired), Rejection: uint64(WithdrawDeclined)},
			"rejected with bad reason":    {Type: TypeResult, ID: id[:], Status: uint64(StatusRejected), Rejection: 7},
			"status zero":                 {Type: TypeResult, ID: id[:], Status: 0},
			"status out of range":         {Type: TypeResult, ID: id[:], Status: 8},
			"zero id":                     {Type: TypeResult, ID: make([]byte, 32), Status: uint64(StatusUnknown)},
		}
		five := int64(5)
		rows["retry_after on unknown"] = resultWire{Type: TypeResult, ID: id[:], Status: uint64(StatusUnknown), RetryAfter: &five}
		for name, w := range rows {
			b, err := record.MarshalCanonical(w)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeResult(b); !errors.Is(err, ErrMessage) {
				t.Fatalf("%s: %v", name, err)
			}
		}
	})
	t.Run("an embedded record that is not a record", func(t *testing.T) {
		b, _ := record.MarshalCanonical(fetchWire{Type: TypeFetch, ID: id[:], Signed: []byte{1, 2, 3}})
		if _, err := DecodeFetch(b); !errors.Is(err, ErrMessage) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("encoders refuse what no decoder accepts", func(t *testing.T) {
		if _, err := (ResultMsg{ID: id, Status: StatusComplete}).Encode(); !errors.Is(err, ErrMessage) {
			t.Fatalf("complete without credential: %v", err)
		}
		if _, err := (ResultMsg{ID: id, Status: StatusRejected, Credential: &complete}).Encode(); !errors.Is(err, ErrMessage) {
			t.Fatalf("rejected with credential: %v", err)
		}
		if _, err := (CommitMsg{Commitment: c}).Encode(); !errors.Is(err, ErrMessage) {
			t.Fatalf("M1 without created_at: %v", err)
		}
	})
}

func ptr[T any](v T) *T { return &v }

func TestDecodeOpening(t *testing.T) {
	c, id, _, _ := vectorMessages(t)
	m1, _ := CommitMsg{Commitment: c, CreatedAt: time.Unix(1798761600, 0).UTC()}.Encode()
	f1, _ := FetchMsg{ID: id}.Encode()
	m2, _ := RevealMsg{NB: nonceOf(1)}.Encode()
	if o, err := DecodeOpening(m1); err != nil || o.Commit == nil || o.Fetch != nil {
		t.Fatalf("M1: %+v %v", o, err)
	}
	if o, err := DecodeOpening(f1); err != nil || o.Fetch == nil || o.Commit != nil {
		t.Fatalf("F1: %+v %v", o, err)
	}
	if _, err := DecodeOpening(m2); !errors.Is(err, ErrMessage) {
		t.Fatalf("M2 as an opening: %v", err)
	}
}

// Fuzz tests: any frame that decodes encodes back to the identical bytes, and
// no frame panics a decoder.
func seedFuzz(f *testing.F, keys ...string) {
	for _, k := range keys {
		b, err := hexDecode(vec[k])
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte{})
	f.Add([]byte{0xa0})
}

func fuzzRoundTrip[M interface{ Encode() ([]byte, error) }](f *testing.F, decode func([]byte) (M, error), keys ...string) {
	seedFuzz(f, keys...)
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := decode(data)
		if err != nil {
			return
		}
		again, err := m.Encode()
		if err != nil {
			t.Fatalf("decoded message does not encode: %v", err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("round trip differs:\n in  %x\n out %x", data, again)
		}
	})
}

func FuzzDecodeCommit(f *testing.F)   { fuzzRoundTrip(f, DecodeCommit, "msg.m1", "msg.m1_not_after") }
func FuzzDecodeReveal(f *testing.F)   { fuzzRoundTrip(f, DecodeReveal, "msg.m2") }
func FuzzDecodeOpen(f *testing.F)     { fuzzRoundTrip(f, DecodeOpen, "msg.m3") }
func FuzzDecodeAccepted(f *testing.F) { fuzzRoundTrip(f, DecodeAccepted, "msg.m4") }
func FuzzDecodeRefusal(f *testing.F) {
	fuzzRoundTrip(f, DecodeRefusal, "msg.refused_paused", "msg.refused_not_accepted")
}
func FuzzDecodeFetch(f *testing.F) {
	fuzzRoundTrip(f, DecodeFetch, "msg.f1_poll", "msg.f1_withdraw_mismatch", "msg.f1_signed")
}
func FuzzDecodeResult(f *testing.F) {
	fuzzRoundTrip(f, DecodeResult, "msg.f2_awaiting_target", "msg.f2_complete", "msg.f2_rejected_mismatch", "msg.f2_unknown")
}

func FuzzDecodeOpening(f *testing.F) {
	seedFuzz(f, "msg.m1", "msg.f1_poll", "msg.f1_signed", "msg.m2")
	f.Fuzz(func(t *testing.T, data []byte) {
		o, err := DecodeOpening(data)
		if err != nil {
			return
		}
		if (o.Commit == nil) == (o.Fetch == nil) {
			t.Fatal("an opening is exactly a commitment or a fetch")
		}
		var again []byte
		if o.Commit != nil {
			again, err = o.Commit.Encode()
		} else {
			again, err = o.Fetch.Encode()
		}
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("round trip: %x %v", again, err)
		}
	})
}
