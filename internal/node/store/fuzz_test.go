package store_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/internal/node/store"
)

var recordDirs = []string{
	store.OutDir, store.InDir, store.CtlDir, store.EndpointsDir, store.AttemptsDir,
	store.CheckpointsDir, store.AdminDir, store.AlertsDir, store.SeqDir,
}

var allKinds = []store.Kind{
	store.KindOut, store.KindOutCtl, store.KindIn, store.KindCtl, store.KindEndpoint,
	store.KindAttempts, store.KindCheckpoint, store.KindAdmin, store.KindAlert, store.KindSeq, store.KindOutLost,
}

// Whatever DecodeRecord accepts re-encodes to the same bytes, and is the
// record its file name says. The name is derived from the first line, so that
// the fuzzer reaches the checks of the body and not only the name check.
func FuzzDecodeRecord(f *testing.F) {
	for _, g := range goldenRecords {
		for i, d := range recordDirs {
			if d == g.dir {
				f.Add(uint8(i), []byte(g.file))
			}
		}
	}
	f.Fuzz(func(t *testing.T, dirIdx uint8, data []byte) {
		dir := recordDirs[int(dirIdx)%len(recordDirs)]
		name := "x.rec"
		if fr, dmg := recfile.DecodeFrame(data); dmg == nil {
			name = nameOf(fr.Kind, string(fr.Key))
		}
		rec, dmg := store.DecodeRecord(dir, name, data)
		if dmg != nil {
			return
		}
		if rec.Name() != name {
			t.Fatalf("a record decoded under %q has the name %q", name, rec.Name())
		}
		again, err := store.EncodeRecord(rec)
		if err != nil {
			t.Fatalf("a record that decoded cannot be encoded: %v", err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("decode then encode changed the bytes:\n in  %q\n out %q", data, again)
		}
	})
}

// Whatever ParseKey accepts is written back as the same key text.
func FuzzParseKey(f *testing.F) {
	f.Add(uint8(0), []byte(e1+" msg-0001 "+n2+"/"+e2))
	f.Add(uint8(2), []byte(n2+" "+e3+" msg-0001 "+e2))
	f.Add(uint8(3), []byte(n2+" "+e3+" msg-0001 "+e2))
	f.Add(uint8(4), []byte(e1))
	f.Add(uint8(6), []byte("attempt 0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6"))
	f.Add(uint8(7), []byte("local "+op))
	f.Add(uint8(8), []byte(aid))
	f.Add(uint8(10), []byte(e1+" "+strings.Repeat("0123456789abcdef", 4)))
	f.Fuzz(func(t *testing.T, kindIdx uint8, key []byte) {
		kind := allKinds[int(kindIdx)%len(allKinds)]
		k, err := store.ParseKey(kind, key)
		if err != nil {
			return
		}
		var back []byte
		switch v := k.(type) {
		case store.OutKey:
			back, err = v.Bytes()
			if v.Control != (kind == store.KindOutCtl) {
				t.Fatalf("kind %s parsed to a key with Control = %v", kind, v.Control)
			}
		case store.TargetKey:
			back, err = v.Bytes()
		case store.CheckpointKey:
			back, err = store.CheckpointKeyBytes(v)
		default:
			back = reencodeSimpleKey(t, kind, k)
		}
		if err != nil || !bytes.Equal(back, key) {
			t.Fatalf("key %q of kind %s came back as %q (%v)", key, kind, back, err)
		}
	})
}

// Whatever ParseArchive is given, it returns without panicking and never makes
// up more records than there are newlines.
func FuzzParseArchive(f *testing.F) {
	for _, c := range archiveSeeds() {
		f.Add(c)
	}
	f.Add([]byte("\n\n{\"crc\":\"00000000\",\"rec\":{}}\n\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got := store.ParseArchive(data)
		if n := len(got.Summaries) + got.Dropped; n > bytes.Count(data, []byte("\n"))+1 {
			t.Fatalf("%d records and drops out of %d lines", n, bytes.Count(data, []byte("\n"))+1)
		}
	})
}

// Whatever body is put in a frame with a right checksum under a valid key of a
// kind, DecodeRecord either refuses it as a damaged body or returns a record that
// encodes to the same bytes: the fuzzer reaches the checks of every body, which a
// mutated whole file seldom does.
func FuzzDecodeRecordBody(f *testing.F) {
	keys := map[store.Kind]string{}
	for _, g := range goldenRecords {
		_, key, _ := goldenParts(g.file)
		kind := store.Kind(strings.Fields(g.file)[2])
		if _, ok := keys[kind]; !ok { // the first vector of a kind: its key is the one the fuzzer frames bodies under
			keys[kind] = key
		}
	}
	kindsInOrder := []store.Kind{store.KindOut, store.KindIn, store.KindCtl, store.KindEndpoint, store.KindAttempts, store.KindCheckpoint, store.KindAdmin, store.KindAlert, store.KindSeq, store.KindOutLost}
	for i, k := range kindsInOrder {
		for _, g := range goldenRecords {
			if strings.Fields(g.file)[2] == string(k) {
				_, _, body := goldenParts(g.file)
				f.Add(uint8(i), []byte(body))
			}
		}
	}
	f.Fuzz(func(t *testing.T, kindIdx uint8, body []byte) {
		kind := kindsInOrder[int(kindIdx)%len(kindsInOrder)]
		key := keys[kind]
		dir := kind.Dir()
		file, err := recfile.EncodeFrame(string(kind), []byte(key), body)
		if err != nil {
			return
		}
		rec, dmg := store.DecodeRecord(dir, nameOf(string(kind), key), file)
		if dmg != nil {
			if dmg.Class != store.BodyDamaged {
				t.Fatalf("a frame with a valid key and checksum is damaged in its first line: %+v", dmg)
			}
			return
		}
		again, err := store.EncodeRecord(rec)
		if err != nil || !bytes.Equal(again, file) {
			t.Fatalf("decode then encode changed the file (%v):\n in  %q\n out %q", err, file, again)
		}
	})
}

// goldenParts splits a golden file into its kind's directory (unused), its key and
// its body.
func goldenParts(file string) (string, string, string) {
	nl := strings.IndexByte(file, '\n')
	fields := strings.Split(file[:nl], " ")
	raw, _ := base64.RawURLEncoding.DecodeString(fields[5])
	return "", string(raw), file[nl+1:]
}
