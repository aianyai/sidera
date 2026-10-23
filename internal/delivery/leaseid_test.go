package delivery

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/iotest"
)

func TestNewLeaseID(t *testing.T) {
	rows := []struct {
		rule    string
		src     func() *bytes.Reader
		wantErr error
		text    string
	}{
		{"16 random bytes become the ID", func() *bytes.Reader {
			return bytes.NewReader([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 255})
		}, nil, "000102030405060708090a0b0c0d0eff"},
		{"all-zero bytes are rejected", func() *bytes.Reader { return bytes.NewReader(make([]byte, LeaseIDLen)) }, ErrLeaseIDZero, ""},
		{"a source that runs dry is an error", func() *bytes.Reader { return bytes.NewReader(make([]byte, LeaseIDLen-1)) }, errAny, ""},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			id, err := NewLeaseID(r.src())
			switch {
			case r.wantErr == nil && err != nil:
				t.Fatal(err)
			case r.wantErr == errAny && err == nil, r.wantErr != nil && r.wantErr != errAny && !errors.Is(err, r.wantErr):
				t.Fatalf("err %v, want %v", err, r.wantErr)
			}
			if err == nil && id.String() != r.text {
				t.Errorf("text %q, want %q", id.String(), r.text)
			}
			if err != nil && !id.IsZero() {
				t.Errorf("a failed ID must be the zero value, got %v", id)
			}
		})
	}
	t.Run("a failing source is an error", func(t *testing.T) {
		if _, err := NewLeaseID(iotest.ErrReader(errors.New("boom"))); err == nil {
			t.Error("want an error")
		}
	})
}

var errAny = errors.New("any error")

func TestLeaseIDText(t *testing.T) {
	id, err := NewLeaseID(testRand)
	if err != nil {
		t.Fatal(err)
	}
	s := id.String()
	if len(s) != LeaseIDTextLen || s != strings.ToLower(s) {
		t.Errorf("text %q is not %d lowercase hex characters", s, LeaseIDTextLen)
	}
	if LeaseIDTextLen != 32 {
		t.Errorf("LeaseIDTextLen = %d, want 32", LeaseIDTextLen)
	}
	if (LeaseID{}).String() != strings.Repeat("0", LeaseIDTextLen) || !(LeaseID{}).IsZero() {
		t.Error("the zero value is unset and prints as zeros")
	}
}

func TestParseLeaseID(t *testing.T) {
	good := "000102030405060708090a0b0c0d0eff"
	rows := []struct {
		rule string
		in   string
		err  error
	}{
		{"the canonical form parses", good, nil},
		{"upper case is not canonical", strings.ToUpper(good), ErrLeaseIDText},
		{"a short text is rejected", good[:30], ErrLeaseIDText},
		{"a long text is rejected", good + "00", ErrLeaseIDText},
		{"a non-hex character is rejected", good[:31] + "g", ErrLeaseIDText},
		{"an empty text is rejected", "", ErrLeaseIDText},
		{"all zeros is rejected as unset", strings.Repeat("0", LeaseIDTextLen), ErrLeaseIDZero},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			id, err := ParseLeaseID(r.in)
			if !errors.Is(err, r.err) {
				t.Fatalf("err %v, want %v", err, r.err)
			}
			if err == nil && id.String() != r.in {
				t.Errorf("round trip gave %q", id.String())
			}
		})
	}
}
