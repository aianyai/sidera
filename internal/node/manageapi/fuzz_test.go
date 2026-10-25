package manageapi

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
)

// FuzzDecodeRequest feeds untrusted bytes to the one entry that reads a request of
// the management interface. Decoding never panics, and what decodes is
// canonical: the params re-encoded decode again to the same canonical bytes and the
// same fingerprint, because the fingerprint is how a resend is told from the reuse
// of an identifier.
func FuzzDecodeRequest(f *testing.F) {
	for _, s := range []string{
		`{}`,
		`{"params":{}}`,
		`{"params":{"instance":"chat"}}`,
		`{"operation_id":"00000000000000000000000000000001","params":{"endpoint":"x"}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":3,"params":{}}`,
		`{"operation_id":"00000000000000000000000000000001"}`,
		`{"params":null}`, `{"params":{"a":1,"a":2}}`, `[]`, `null`, ``, `{`, `{"operation_id":""}`,
		`{"params":{"endpoint":"` + "\xff" + `"}}`,
	} {
		for _, m := range []string{"endpoints.list", "endpoints.close", "daemon.stop", "nothing"} {
			f.Add(m, []byte(s))
		}
	}
	svc := &fakeService{}
	s, err := New(Config{Service: svc, Node: ownNode, Clock: clocktest.NewFake(t0)})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, name string, body []byte) {
		d, err := s.decode(name, body)
		if err != nil {
			return
		}
		if d.plan == nil || len(d.plan.canonical) == 0 {
			t.Fatalf("a request of %s decoded with no plan", name)
		}
		if d.m.op.SideEffecting() != (d.plan.operation != nil) {
			t.Fatalf("%s: side effects %v, operation %v", name, d.m.op.SideEffecting(), d.plan.operation != nil)
		}
		// the canonical form of an accepted request is accepted, and is its own
		// canonical form
		again, err := s.decode(name, canonicalRequest(d))
		if err != nil {
			t.Fatalf("the canonical form of an accepted request is refused: %v\n%s", err, canonicalRequest(d))
		}
		if !bytes.Equal(again.plan.canonical, d.plan.canonical) || again.fingerprint != d.fingerprint ||
			again.id != d.id || again.expected != d.expected {
			t.Fatalf("canonical params %s then %s; fingerprints %x then %x", d.plan.canonical, again.plan.canonical, d.fingerprint, again.fingerprint)
		}
	})
}

// canonicalRequest writes an accepted request again in its canonical form.
func canonicalRequest(d *decoded) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	if d.m.op.SideEffecting() {
		fmt.Fprintf(&b, `"operation_id":"%x",`, d.id[:])
		if d.expected != 0 {
			fmt.Fprintf(&b, `"expected_version":%d,`, d.expected)
		}
	}
	fmt.Fprintf(&b, `"params":%s}`, d.plan.canonical)
	return b.Bytes()
}
