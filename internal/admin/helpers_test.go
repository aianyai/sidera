package admin

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func person(t *testing.T, n byte) ident.PersonID {
	t.Helper()
	b := make([]byte, ident.PersonIDLen)
	b[0] = n
	id, err := ident.PersonIDFromBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func node(t *testing.T, n byte) ident.NodeID {
	t.Helper()
	b := make([]byte, ident.NodeIDLen)
	b[0] = n
	id, err := ident.NodeIDFromBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustList(t *testing.T, entries ...Entry) List {
	t.Helper()
	l, problems := NewList(entries)
	if len(problems) > 0 {
		t.Fatalf("unexpected list problems: %v", problems)
	}
	return l
}

// validCert returns a certificate valid around t0 with the given cap and a
// clear revocation lookup.
func validCert(max Level) DeviceCert {
	return DeviceCert{
		MaxLevel:   max,
		NotBefore:  t0.Add(-24 * time.Hour),
		NotAfter:   t0.Add(24 * time.Hour),
		Revocation: RevocationClear,
	}
}
