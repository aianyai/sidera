package ledger_test

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// FuzzDecode: Decode never panics, and a line it accepts is the canonical line
// of the entry it decoded to, byte for byte. The canonical form is what lets a
// reader trust that two equal entries are one line.
func FuzzDecode(f *testing.F) {
	for _, g := range goldenEntries {
		f.Add([]byte(g.line))
	}
	f.Add([]byte(`{"v":1}`))
	f.Add([]byte(`{"v":1,"writer":"fixture","pid":0,"seq":1,"at_ms":1,"step":"","kind":"fixture.step","attrs":{"name":"é"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		e, err := ledger.Decode(data)
		if err != nil {
			return
		}
		again, err := ledger.Encode(e)
		if err != nil {
			t.Fatalf("an entry Decode accepted cannot be encoded: %v\nline %q", err, data)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("decode then encode changed the line:\n in  %q\n out %q", data, again)
		}
	})
}

// FuzzParseStream: reading a whole file never panics, and every line of a file
// that parses is canonical.
func FuzzParseStream(f *testing.F) {
	for _, g := range goldenEntries {
		f.Add([]byte(g.line+"\n"), false)
	}
	f.Add([]byte(`{"v":1,"writer":"fixture","pid":0,"seq":1,"at_ms":1,"step":"","kind":"fixture.step","attrs":{"name":"a"}}`), true)
	f.Add([]byte("\n\n"), false)
	f.Fuzz(func(t *testing.T, data []byte, killed bool) {
		entries, err := ledger.ParseStream("x-0.jsonl", data, killed)
		if err != nil {
			return
		}
		for _, e := range entries {
			line, err := ledger.Encode(e)
			if err != nil {
				t.Fatalf("parsed entry cannot be encoded: %v", err)
			}
			if !bytes.Contains(data, line) {
				t.Fatalf("a parsed entry is not a line of the input: %s", line)
			}
		}
	})
}
