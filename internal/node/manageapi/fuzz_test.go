package manageapi

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
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
		// the owner's methods: identifiers, digests, nodes, addresses and patches
		`{"operation_id":"00000000000000000000000000000001","params":{"alert":"00000000000000000000000000000001"}}`,
		`{"operation_id":"00000000000000000000000000000001","params":{"digest":"` + strings.Repeat("ab", 32) + `"}}`,
		`{"operation_id":"00000000000000000000000000000001","params":{"node":"` + ownNode.String() + `"}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":2,"params":{"settings":{"node_queue_limit":20000,"endpoint_defaults":{"max_queue_s":60,"backlog_limit":9}}}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":2,"params":{"endpoint":"` + ownNode.String() + `/` + strings.Repeat("a", 26) + `","settings":{"backlog_limit":3}}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":2,"params":{"settings":{"disk_reserve_bytes":9223372036854775807,"reply_permission":true}}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":2,"params":{"settings":{"a":null,"b":[1,{"c":1e2}],"d":"\u00e9"}}}`,
		`{"params":{"include_acknowledged":true}}`,
		// adapters and presets: a package path, an adapter with and without its instance
		// and configuration, a preset document
		`{"operation_id":"00000000000000000000000000000001","params":{"source":"/srv/adapters/relay"}}`,
		`{"operation_id":"00000000000000000000000000000001","params":{"source":"relative/path"}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":2,"params":{"adapter":"relay"}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":2,"params":{"adapter":"relay","instance":"second","config":{"b":[1,2],"a":{"c":"x"}}}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":2,"params":{"instance":"relay"}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":2,"params":{"name":"tight","preset":{"local_sources":"deny","config":{"relay":{"k":1}}}}}`,
		`{"operation_id":"00000000000000000000000000000001","expected_version":2,"params":{"name":"tight"}}`,
	} {
		for _, m := range methodNames(f) {
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

// methodNames are every method of the owner the server has, so that the fuzzer feeds
// each, and one that does not exist. A test row is answered by its own path in the
// server and is not read by decode.
func methodNames(tb testing.TB) []string {
	tb.Helper()
	ms, err := defaultMethods()
	if err != nil {
		tb.Fatal(err)
	}
	names := []string{"nothing"}
	for name, m := range ms {
		if m.test == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
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
