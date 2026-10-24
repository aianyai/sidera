package store_test

import (
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
)

// A scan returns every decoded record of every directory, the names of the
// bodies and of the archive files, and nothing it did not find.
func TestScanReturnsEveryRecordOfEveryKind(t *testing.T) {
	r := newRig(t)
	seed(t, r.mem,
		srcOut(1, false), outCtlRecord(2), inRev(3, 1), sessionCtl(4, 5), directEndpoint(7, "chat"),
		store.AttemptsRecord{Endpoint: ep(2)}, store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{}`)},
		adminResult(1), store.AlertRecord{ID: store.AlertID{1}, Kind: "k", At: base, Detail: "d"},
		store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{addr(2, 2): 1}})
	seedBodies(t, r.mem, bodyBytes(1), bodyBytes(2))
	seedRaw(t, r.mem, store.ArchiveDir, "2026-10-24.jsonl", []byte("\n"))
	sc := r.scan()
	if len(sc.Out) != 2 || len(sc.In) != 1 || len(sc.Ctl) != 1 || len(sc.Endpoints) != 1 || len(sc.Attempts) != 1 ||
		len(sc.Checkpoints) != 1 || len(sc.Admin) != 1 || len(sc.Alerts) != 1 || len(sc.Seq) != 1 {
		t.Errorf("records: out %d in %d ctl %d endpoint %d attempts %d checkpoint %d admin %d alert %d seq %d",
			len(sc.Out), len(sc.In), len(sc.Ctl), len(sc.Endpoints), len(sc.Attempts), len(sc.Checkpoints), len(sc.Admin), len(sc.Alerts), len(sc.Seq))
	}
	if len(sc.Bodies) != 2 || len(sc.Archive) != 1 || sc.Archive[0] != "2026-10-24.jsonl" {
		t.Errorf("bodies %v archive %v", sc.Bodies, sc.Archive)
	}
	if len(sc.Damaged)+len(sc.Corrupt)+len(sc.Unknown) != 0 {
		t.Errorf("damaged %v corrupt %v unknown %v", sc.Damaged, sc.Corrupt, sc.Unknown)
	}
	kinds := map[store.Kind]bool{}
	for _, o := range sc.Out {
		if o.Key.Control {
			kinds[store.KindOutCtl] = true
		} else {
			kinds[store.KindOut] = true
		}
	}
	if !kinds[store.KindOut] || !kinds[store.KindOutCtl] {
		t.Errorf("the two kinds of out/ were not told apart: %v", kinds)
	}
	for _, o := range sc.Out {
		if !equalRecords(o, srcOut(1, false)) && !equalRecords(o, outCtlRecord(2)) {
			t.Errorf("an out record came back changed: %#v", o)
		}
	}
}

// Names are classified: a record, a body, a quarantined file with its alert,
// the debris of an interrupted write, and the rest.
func TestScanClassifiesFileNames(t *testing.T) {
	r := newRig(t)
	good := srcOut(1, false)
	seed(t, r.mem, good)
	id := store.AlertID{0xab}
	seedRaw(t, r.mem, store.OutDir, good.Name()+"."+id.String()+".corrupt", []byte("junk"))
	seedRaw(t, r.mem, store.OutDir, "stray.txt", []byte("x"))
	seedRaw(t, r.mem, store.OutDir, strings.Repeat("0", 64)+".rec", []byte("not a record"))
	seedRaw(t, r.mem, store.OutDir, ".tmp-123", []byte("debris"))
	seedRaw(t, r.mem, store.BodiesDir, bodyRef(1).Name(), bodyBytes(1))
	seedRaw(t, r.mem, store.BodiesDir, bodyRef(2).Name()+"."+id.String()+".corrupt", []byte("junk"))
	seedRaw(t, r.mem, store.BodiesDir, "oddly-named", []byte("x"))
	sc := r.scan()
	if len(sc.Out) != 1 {
		t.Errorf("records: %d", len(sc.Out))
	}
	if len(sc.Corrupt) != 2 {
		t.Fatalf("quarantined files: %v", sc.Corrupt)
	}
	for _, c := range sc.Corrupt {
		if c.Alert != id {
			t.Errorf("quarantined file %s has alert %v", c.Name, c.Alert)
		}
		if c.Dir == store.OutDir && c.Original != good.Name() {
			t.Errorf("original name %q", c.Original)
		}
		if c.Dir == store.BodiesDir && c.Original != bodyRef(2).Name() {
			t.Errorf("original name of a body %q", c.Original)
		}
	}
	if len(sc.Damaged) != 1 || sc.Damaged[0].Damage.Class != store.HeaderUnreadable || sc.Damaged[0].Key != nil {
		t.Errorf("damaged: %+v", sc.Damaged)
	}
	if len(sc.Bodies) != 1 {
		t.Errorf("bodies: %v", sc.Bodies)
	}
	var unknown []string
	for _, u := range sc.Unknown {
		unknown = append(unknown, u)
	}
	if strings.Join(unknown, ",") != "state/bodies/oddly-named,state/out/stray.txt" {
		t.Errorf("unknown: %v", unknown)
	}
}

// A damaged file is reported with the key of its first line when that can be
// trusted, and without when it cannot.
func TestScanReportsTheKeyOfADamagedFileOnlyWhenItCanBeTrusted(t *testing.T) {
	r := newRig(t)
	seed(t, r.mem, srcOut(1, false), inRev(2, 1), sessionCtl(3, 5))
	damageBody(t, r.mem, path.Join(store.OutDir, srcOut(1, false).Name()))
	damageBody(t, r.mem, path.Join(store.InDir, inRev(2, 1).Name()))
	damageHeader(t, r.mem, path.Join(store.CtlDir, sessionCtl(3, 5).Name()))
	sc := r.scan()
	if len(sc.Damaged) != 3 || len(sc.Out)+len(sc.In)+len(sc.Ctl) != 0 {
		t.Fatalf("damaged: %v, records %d", sc.Damaged, len(sc.Out)+len(sc.In)+len(sc.Ctl))
	}
	for _, d := range sc.Damaged {
		switch d.Dir {
		case store.OutDir:
			if k, ok := d.Key.(store.OutKey); !ok || k.Message != msgID(1) || d.Kind != store.KindOut || d.Damage.Class != store.BodyDamaged {
				t.Errorf("out: %+v", d)
			}
		case store.InDir:
			if k, ok := d.Key.(store.TargetKey); !ok || k != tgtKey(2) || d.Kind != store.KindIn || d.Damage.Class != store.BodyDamaged {
				t.Errorf("in: %+v", d)
			}
		case store.CtlDir:
			if d.Key != nil || d.Damage.Class != store.HeaderUnreadable {
				t.Errorf("ctl with a damaged first line: %+v", d)
			}
		}
	}
}

// The first line of a quarantined file is read, so that its key is known for
// the sequence floor of a record that cannot be replaced.
func TestScanReadsTheKeyOfQuarantinedFiles(t *testing.T) {
	r := newRig(t)
	o := srcOut(1, false)
	data := mustEncode(t, o)
	id := store.AlertID{1}
	seedRaw(t, r.mem, store.OutDir, o.Name()+"."+id.String()+".corrupt", data[:len(data)-3]) // cut short: the first line is whole
	seedRaw(t, r.mem, store.OutDir, zeroRec+"."+store.AlertID{2}.String()+".corrupt", []byte("no first line"))
	sc := r.scan()
	if len(sc.Corrupt) != 2 {
		t.Fatalf("%v", sc.Corrupt)
	}
	for _, c := range sc.Corrupt {
		switch c.Alert {
		case id:
			if k, ok := c.Key.(store.OutKey); !ok || k.Message != msgID(1) || c.Kind != store.KindOut {
				t.Errorf("a quarantined file with a readable first line: %+v", c)
			}
		default:
			if c.Key != nil || c.Kind != "" {
				t.Errorf("a quarantined file without one: %+v", c)
			}
		}
	}
}

// The first line of a quarantined file is trusted by the rule of a live file,
// against the name the file had: a first line that parses but belongs to another
// name, or to a kind the directory does not hold, says nothing about the file.
func TestScanTrustsTheKeyOfAQuarantinedFileOnlyWhenItsFirstLineBelongsToItsName(t *testing.T) {
	id := store.AlertID{1}
	named, ownData := misnamed(t, srcOut(1, false), srcOut(1, false), id)
	other, otherData := misnamed(t, srcOut(1, false), srcOut(2, false), id)
	endpointFile := mustEncode(t, directEndpoint(7, "chat"))
	for _, c := range []struct {
		rule    string
		name    string
		data    []byte
		trusted bool
	}{
		{"a first line that belongs to the name", named, ownData, true},
		{"a first line that parses but hashes to another name", other, otherData, false},
		{"a record of a kind the directory does not hold", corruptName(srcOut(3, false), id), endpointFile, false},
	} {
		t.Run(c.rule, func(t *testing.T) {
			r := newRig(t)
			seedRaw(t, r.mem, store.OutDir, c.name, c.data)
			sc := r.scan()
			if len(sc.Corrupt) != 1 {
				t.Fatalf("quarantined files: %v", sc.Corrupt)
			}
			got := sc.Corrupt[0]
			if c.trusted {
				if k, ok := got.Key.(store.OutKey); !ok || k.Message != msgID(1) || got.Kind != store.KindOut {
					t.Errorf("a first line of the file's own name is not trusted: %+v", got)
				}
			} else if got.Key != nil || got.Kind != "" {
				t.Errorf("a first line that does not belong to the name is trusted: %+v", got)
			}
		})
	}
}

// A missing directory is not an error: a node whose state/ was deleted has
// nothing, and the layout rule makes the directories again.
func TestScanOfMissingDirectoriesIsEmpty(t *testing.T) {
	r := newRig(t)
	mustDo(t, r.mem.RemoveAll(setupTag, "state"))
	sc, err := r.st.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(sc.Out)+len(sc.In)+len(sc.Bodies)+len(sc.Corrupt)+len(sc.Damaged)+len(sc.Archive) != 0 {
		t.Errorf("a scan of nothing found something: %+v", sc)
	}
	if _, err := r.mem.Stat("state"); err == nil {
		t.Errorf("Scan made a directory: it only reads")
	}
}

func TestAvailableReportsTheVolumeOfState(t *testing.T) {
	r := newRig(t)
	r.mem.SetAvailable(123456789)
	got, err := r.st.Available()
	if err != nil || got != 123456789 {
		t.Errorf("Available = %d, %v, want 123456789", got, err)
	}
}

func TestReadBodyChecksTheNameAndTheContent(t *testing.T) {
	r := newRig(t)
	seedBodies(t, r.mem, bodyBytes(1))
	got, err := r.st.ReadBody(*bodyRef(1))
	if err != nil || string(got) != string(bodyBytes(1)) {
		t.Fatalf("ReadBody = %q, %v", got, err)
	}
	if _, err := r.st.ReadBody(*bodyRef(2)); !errors.Is(err, store.ErrBodyMissing) {
		t.Errorf("a missing body: %v, want ErrBodyMissing", err)
	}
	seedRaw(t, r.mem, store.BodiesDir, bodyRef(3).Name(), []byte("tampered"))
	if _, err := r.st.ReadBody(*bodyRef(3)); !errors.Is(err, store.ErrBodyMismatch) {
		t.Errorf("a tampered body: %v, want ErrBodyMismatch", err)
	}
	if _, err := r.st.ReadBody(store.BodyRef{Digest: bodyRef(1).Digest, Len: 3}); !errors.Is(err, store.ErrBodyMismatch) {
		t.Errorf("a body of another length than the reference says: %v, want ErrBodyMismatch", err)
	}
}

var _ = fsx.StepWrite
