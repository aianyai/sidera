package store_test

import (
	"path"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/internal/node/store"
)

// The files of out/ are named after their sender, so that a file whose first
// line cannot be read still says whose message it held: <sender>.<hash>.rec,
// where the hash is the one of every other directory.

// hashOfName is the 64 hex digits before ".rec" in the last dot-separated part of
// the name of a file of out/.
func hashOfName(t testing.TB, name string) string {
	t.Helper()
	h, ok := strings.CutSuffix(name, ".rec")
	if !ok {
		t.Fatalf("%q is not a record file name", name)
	}
	return h[strings.LastIndexByte(h, '.')+1:]
}

// zeroOutName is the name of a file of out/ of the sender e that has no record:
// the hash is not one of any key.
func zeroOutName(e ident.EndpointID) string {
	return e.String() + "." + strings.Repeat("0", 64) + ".rec"
}

func TestOutFileNamesCarryTheSenderAndTheHashOfKindAndKey(t *testing.T) {
	for _, c := range []struct {
		name string
		rec  store.OutRecord
		kind store.Kind
	}{
		{"a message", srcOut(1, false), store.KindOut},
		{"a control signal", outCtlRecord(2), store.KindOutCtl},
		{"a lost record", lostOutAt(srcOut(3, false).Key, base), store.KindOut},
	} {
		t.Run(c.name, func(t *testing.T) {
			key, err := c.rec.Key.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			hash := strings.TrimSuffix(recfile.FileName(string(c.kind), key), ".rec")
			want := ep(1).String() + "." + hash + ".rec"
			if got := c.rec.Name(); got != want {
				t.Errorf("Name() = %q, want %q", got, want)
			}
			if got := c.rec.Key.FileName(); got != want {
				t.Errorf("OutKey.FileName() = %q, want %q", got, want)
			}
			if got := store.RecordFileName(c.kind, key); got != want {
				t.Errorf("RecordFileName() = %q, want %q", got, want)
			}
			if len(want) > 120 {
				t.Errorf("the name is %d characters long: every platform must take it", len(want))
			}
		})
	}
	t.Run("the other directories keep the bare hash", func(t *testing.T) {
		for _, name := range []string{tgtIn(1).Name(), sessionCtl(1, 5).Name(), store.SeqRecord{Sender: ep(1)}.Name(), directEndpoint(7, "chat").Name()} {
			if strings.Contains(strings.TrimSuffix(name, ".rec"), ".") || len(name) != 64+len(".rec") {
				t.Errorf("%q is not a bare hash name", name)
			}
		}
	})
}

func TestAnOutRecordIsReadUnderItsOwnNameOnly(t *testing.T) {
	rec := srcOut(1, false)
	data := mustEncode(t, rec)
	good := rec.Name()
	hash := hashOfName(t, good)
	other := ep(2).String() + "." + hash + ".rec"
	wrongHash := ep(1).String() + "." + strings.Repeat("a", 64) + ".rec"
	for _, c := range []struct {
		rule string
		name string
		ok   bool
	}{
		{"the name of the record", good, true},
		{"a prefix that is another sender", other, false},
		{"a bare hash, which is the name of no file of out/", hash + ".rec", false},
		{"a hash that is not the one of kind and key", wrongHash, false},
		{"a prefix in upper case", strings.ToUpper(ep(1).String()) + "." + hash + ".rec", false},
		{"an empty prefix", "." + hash + ".rec", false},
		{"a prefix twice", ep(1).String() + "." + good, false},
	} {
		t.Run(c.rule, func(t *testing.T) {
			got, dmg := store.DecodeRecord(store.OutDir, c.name, data)
			switch {
			case c.ok && (dmg != nil || got.Name() != good):
				t.Errorf("refused: %+v", dmg)
			case !c.ok && (dmg == nil || dmg.Class != store.HeaderUnreadable || dmg.Reason != store.ReasonNameMismatch):
				t.Errorf("damage %+v, want an unreadable first line, name mismatch", dmg)
			}
		})
	}
	t.Run("the kind is part of the hash, so a control signal is not read under the name of a message", func(t *testing.T) {
		ctl := rec
		ctl.Key.Control = true
		ctl.Outbound.Kind = outCtlRecord(1).Outbound.Kind
		if _, dmg := store.DecodeRecord(store.OutDir, good, mustEncode(t, outCtlRecord(1))); dmg == nil {
			t.Error("a control signal accepted under the name of a message")
		}
	})
	t.Run("a prefixed name is no name of another directory", func(t *testing.T) {
		in := inRev(1, 1)
		if _, dmg := store.DecodeRecord(store.InDir, ep(3).String()+"."+in.Name(), mustEncode(t, in)); dmg == nil {
			t.Error("a target record accepted under a prefixed name")
		}
	})
}

// The scan tells a record file of out/ from a stranger by its shape alone: a
// sender, a dot, 64 hex digits and ".rec". A file of that shape whose prefix is
// no endpoint is damaged, and settled like any other.
func TestScanClassifiesTheNamesOfOut(t *testing.T) {
	r := newRig(t)
	good := srcOut(1, false)
	seed(t, r.mem, good)
	hash := strings.Repeat("0", 64)
	damaged := []string{
		zeroOutName(ep(1)),
		"notanendpoint." + hash + ".rec",
		ep(2).String() + "." + strings.Repeat("1", 64) + ".rec",
	}
	for _, n := range damaged {
		seedRaw(t, r.mem, store.OutDir, n, make([]byte, 64))
	}
	unknown := []string{
		hash + ".rec", // a bare hash is the name of no file of out/
		ep(1).String() + "." + hash[:63] + ".rec",
		ep(1).String() + "." + strings.ToUpper(strings.Repeat("a", 64)) + ".rec",
		ep(1).String() + "." + hash,
		"stray.txt",
	}
	for _, n := range unknown {
		seedRaw(t, r.mem, store.OutDir, n, []byte("x"))
	}
	seedRaw(t, r.mem, store.InDir, ep(1).String()+"."+hash+".rec", []byte("x")) // not a name of in/
	sc := r.scan()
	if len(sc.Out) != 1 || sc.Out[0].Name() != good.Name() {
		t.Errorf("records: %v", sc.Out)
	}
	var got []string
	for _, d := range sc.Damaged {
		if d.Dir != store.OutDir || d.Key != nil || d.Damage.Class != store.HeaderUnreadable {
			t.Errorf("damaged: %+v", d)
		}
		got = append(got, d.Name)
	}
	if strings.Join(sortedStrings(got), ",") != strings.Join(sortedStrings(damaged), ",") {
		t.Errorf("damaged names %v, want %v", got, damaged)
	}
	var wantUnknown []string
	for _, n := range unknown {
		wantUnknown = append(wantUnknown, path.Join(store.OutDir, n))
	}
	wantUnknown = append(wantUnknown, path.Join(store.InDir, ep(1).String()+"."+hash+".rec"))
	if strings.Join(sortedStrings(sc.Unknown), ",") != strings.Join(sortedStrings(wantUnknown), ",") {
		t.Errorf("unknown %v, want %v", sc.Unknown, wantUnknown)
	}
}
