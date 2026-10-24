package store_test

import (
	"path"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// Unfinished quarantines: the file was renamed, and the process stopped before
// the replacement, the floors, the closed session and the alert. The rule that
// finishes them is the one the crash walk reaches only through a crash during
// another workflow, so these scenarios start from the state a crash leaves and
// make every crash point of the resumption a point of a walk of its own.

// quarantinedDamaged seeds the file of rec, damaged in its body, under the
// quarantined name an interrupted quarantine with the alert of its name gave it.
func quarantinedDamaged(t testing.TB, mem *fsxtest.Mem, dir string, rec store.Record) {
	t.Helper()
	data := append([]byte(nil), mustEncode(t, rec)...)
	data[len(data)-2] ^= 0x01
	seedRaw(t, mem, dir, corruptName(rec, alertFor(dir, rec.Name())), data)
}

func resumedQuarantines() []scenario {
	pair := addr(2, 2)
	return []scenario{
		{
			name: "resumed quarantine of a source record whose first line is trusted",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, srcOut(1, false), srcOut(2, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 2}})
				quarantinedDamaged(t, mem, store.OutDir, srcOut(3, false))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			// The floor is raised once more by each resumption: its range is
			// checked, not its bytes.
			ignore: isIn(store.SeqDir),
			extra: func(t *testing.T, where string, crashed, fin map[string]string) {
				name := store.SeqRecord{Sender: ep(1)}.Name()
				rec, dmg := store.DecodeRecord(store.SeqDir, name, []byte(fin[path.Join(store.SeqDir, name)]))
				if dmg != nil {
					t.Errorf("%s: floor file: %+v", where, dmg)
					return
				}
				if got := rec.(store.SeqRecord).Floors[pair]; got < 3 || got > 7 {
					t.Errorf("%s: floor %d, want 3 (floor 2, one lost record) up to a few more for resumed ones", where, got)
				}
			},
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := srcOut(3, false).Name()
				id := alertFor(store.OutDir, name)
				if out, _, _ := find(t, sc, name); out == nil || out.Lost == nil {
					t.Errorf("the resumed quarantine did not put the replacement: %#v", out)
				}
				if !hasAlert(sc, id, "corrupt_record") || len(sc.Corrupt) != 1 {
					t.Errorf("the alert is missing or the file was quarantined twice: %v %v", sc.Alerts, sc.Corrupt)
				}
				if got := floorOf(t, sc, 1, pair); got != 3 {
					t.Errorf("floor = %d, want 3: the highest of the floor 2 and the live numbers 1 and 2, plus one lost record", got)
				}
			},
		},
		{
			name: "resumed quarantine of a control record whose first line is trusted",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				c := sessionCtl(1, 5)
				derived := derivedIn(1, 5, 1)
				seed(t, mem, session(5, store.EndpointCreating, ctlName(1)), derived)
				seedBodies(t, mem, bodyBytes(1))
				quarantinedDamaged(t, mem, store.CtlDir, c)
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := sessionCtl(1, 5).Name()
				id := alertFor(store.CtlDir, name)
				if _, _, c := find(t, sc, name); c == nil || !c.Lost {
					t.Errorf("the resumed quarantine did not put the replacement: %#v", c)
				}
				if !hasAlert(sc, id, "control_unknown") {
					t.Errorf("the alert must say a native session may remain: %v", sc.Alerts)
				}
				if e := endpointOf(sc, ep(5)); e == nil || e.Status != store.EndpointClosed {
					t.Errorf("the session endpoint still being created must be closed: %#v", e)
				}
				if _, d, _ := find(t, sc, derivedIn(1, 5, 1).Name()); d == nil || d.Record.State != delivery.StateRejected {
					t.Errorf("the delivery derived from the control record must be rejected as the endpoint closed: %#v", d)
				}
			},
		},
		{
			// The first line parses, names a source record of the pair of the
			// floor, and does not hash to the name the file had. It says nothing
			// about the file: no replacement is built, and no floor is raised for
			// the pair it names, neither by its own resumption nor by the
			// quarantine of a damaged source record of the same pair.
			name: "resumed quarantine of a file whose first line belongs to another name",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, srcOut(1, false), srcOut(5, false))
				name, data := misnamed(t, srcOut(3, false), srcOut(2, false), alertFor(store.OutDir, srcOut(2, false).Name()))
				seedRaw(t, mem, store.OutDir, name, data)
				damageBody(t, mem, path.Join(store.OutDir, srcOut(5, false).Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			ignore: isIn(store.SeqDir),
			extra: func(t *testing.T, where string, crashed, fin map[string]string) {
				name := store.SeqRecord{Sender: ep(1)}.Name()
				rec, dmg := store.DecodeRecord(store.SeqDir, name, []byte(fin[path.Join(store.SeqDir, name)]))
				if dmg != nil {
					t.Errorf("%s: floor file: %+v", where, dmg)
					return
				}
				// Only the damaged record of the pair counts, once per resumption
				// of its own quarantine: the file of another name is never one.
				if got := rec.(store.SeqRecord).Floors[pair]; got < 2 || got > 4 {
					t.Errorf("%s: floor %d, want 2 (live number 1, one lost record) up to a few more for resumed ones", where, got)
				}
			},
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				for _, n := range []string{srcOut(2, false).Name(), srcOut(3, false).Name()} {
					if out, _, _ := find(t, sc, n); out != nil {
						t.Errorf("a replacement was built for %s from a first line that does not belong to its name", n)
					}
				}
				id := alertFor(store.OutDir, srcOut(2, false).Name())
				if !hasAlert(sc, id, "corrupt_record") {
					t.Errorf("the quarantine of the file of another name was not finished: %v", sc.Alerts)
				}
				if got := floorOf(t, sc, 1, pair); got != 2 {
					t.Errorf("floor = %d, want 2: the live number 1 and the one lost record; the file of another name claims a record of the pair and must not count", got)
				}
			},
		},
	}
}
