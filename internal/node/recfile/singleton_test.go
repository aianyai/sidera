package recfile_test

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/internal/node/recfile"
)

// Whatever DecodeFrame accepts is canonical: encoding what it returned gives
// the input back, so no two files mean the same record.
func FuzzDecodeFrame(f *testing.F) {
	for _, v := range frameVectors {
		f.Add([]byte(v.file))
	}
	f.Add([]byte("sidera-record 1 in 297bd0aa 2 aw\n{}"))
	f.Add([]byte("sidera-record 1 in 297bd0aa 2 aw\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		fr, dmg := recfile.DecodeFrame(data)
		if dmg != nil {
			if dmg.Class != recfile.HeaderUnreadable && dmg.Class != recfile.BodyDamaged {
				t.Fatalf("damage without a class: %+v", dmg)
			}
			return
		}
		again, err := recfile.EncodeFrame(fr.Kind, fr.Key, fr.Body)
		if err != nil {
			t.Fatalf("a frame that decoded cannot be encoded: %v", err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("decode then encode changed the bytes:\n in  %q\n out %q", data, again)
		}
	})
}

// A singleton is a record of which there is one: its key is its kind, and the
// owner names the file. The vector is the one of the clock, written by hand in
// the table of vectors.
func TestSingletonRecords(t *testing.T) {
	const file = "sidera-record 1 clock cff7d56a 7 Y2xvY2s\n{\"a\":1}"
	got, err := recfile.EncodeSingleton("clock", []byte(`{"a":1}`))
	if err != nil || string(got) != file {
		t.Fatalf("EncodeSingleton = %q, %v, want %q", got, err, file)
	}
	body, dmg := recfile.DecodeSingleton("clock", []byte(file))
	if dmg != nil || string(body) != `{"a":1}` {
		t.Fatalf("DecodeSingleton = %q, %v", body, dmg)
	}
	for _, c := range []struct {
		name   string
		kind   string
		data   string
		class  recfile.DamageClass
		reason recfile.DamageReason
	}{
		{"another kind than the one the owner reads", "init", file, recfile.HeaderUnreadable, recfile.ReasonKindNotAllowed},
		{"a key that is not the kind", "clock", "sidera-record 1 clock cff7d56a 7 YQ\n{\"a\":1}", recfile.HeaderUnreadable, recfile.ReasonNameMismatch},
		{"a first line that is not one", "clock", "garbage", recfile.HeaderUnreadable, recfile.ReasonHeaderNoNewline},
		{"a flipped bit in the body", "clock", file[:len(file)-2] + "0}", recfile.BodyDamaged, recfile.ReasonChecksumMismatch},
		{"a cut body", "clock", file[:len(file)-2], recfile.BodyDamaged, recfile.ReasonLengthMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, dmg := recfile.DecodeSingleton(c.kind, []byte(c.data))
			if dmg == nil || dmg.Class != c.class || dmg.Reason != c.reason {
				t.Errorf("damage = %+v, want class %d reason %d", dmg, c.class, c.reason)
			}
		})
	}
}
