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
	if len(sc.Damaged)+len(sc.Unknown) != 0 {
		t.Errorf("damaged %v unknown %v", sc.Damaged, sc.Unknown)
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

// Names are classified: a record, a body, the debris of an interrupted write,
// and the rest.
func TestScanClassifiesFileNames(t *testing.T) {
	r := newRig(t)
	good := srcOut(1, false)
	seed(t, r.mem, good)
	seedRaw(t, r.mem, store.OutDir, "stray.txt", []byte("x"))
	seedRaw(t, r.mem, store.OutDir, zeroOutName(ep(1)), []byte("not a record"))
	seedRaw(t, r.mem, store.OutDir, ".tmp-123", []byte("debris"))
	seedRaw(t, r.mem, store.BodiesDir, bodyRef(1).Name(), bodyBytes(1))
	seedRaw(t, r.mem, store.BodiesDir, "oddly-named", []byte("x"))
	sc := r.scan()
	if len(sc.Out) != 1 {
		t.Errorf("records: %d", len(sc.Out))
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

// The first line of a damaged file is trusted by the rule of a live file: a first
// line that parses but belongs to another name says nothing about the file, so no
// key is reported for it and nothing is built from the key it claims.
func TestScanTrustsTheKeyOfADamagedFileOnlyWhenItsFirstLineBelongsToItsName(t *testing.T) {
	own := mustEncode(t, srcOut(1, false))
	r := newRig(t)
	seedRaw(t, r.mem, store.OutDir, srcOut(1, false).Name(), own[:len(own)-3]) // cut short: the first line is whole and the file's own
	seedRaw(t, r.mem, store.OutDir, srcOut(2, false).Name(), own[:len(own)-3]) // the first line of another record
	seedRaw(t, r.mem, store.OutDir, srcOut(3, false).Name(), mustEncode(t, directEndpoint(7, "chat")))
	sc := r.scan()
	if len(sc.Damaged) != 3 {
		t.Fatalf("damaged: %v", sc.Damaged)
	}
	for _, d := range sc.Damaged {
		switch d.Name {
		case srcOut(1, false).Name():
			if k, ok := d.Key.(store.OutKey); !ok || k.Message != msgID(1) || d.Kind != store.KindOut || d.Damage.Class != store.BodyDamaged {
				t.Errorf("a first line of the file's own name is not trusted: %+v", d)
			}
		default:
			if d.Key != nil || d.Kind != "" || d.Damage.Class != store.HeaderUnreadable {
				t.Errorf("%s: a first line that does not belong to the name is trusted: %+v", d.Name, d)
			}
		}
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
	if len(sc.Out)+len(sc.In)+len(sc.Bodies)+len(sc.Damaged)+len(sc.Archive) != 0 {
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
