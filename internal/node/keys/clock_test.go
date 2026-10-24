package keys_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
)

func TestReadClockOfAMissingFileIsTheFirstStart(t *testing.T) {
	r := newRig(t)
	got, err := keys.ReadClock(r.mem)
	if err != nil || got != (keys.ClockState{}) {
		t.Errorf("ReadClock with no keys directory = %+v, %v; want the zero state", got, err)
	}
	r.mustInit(1, t0)
	remove(t, r.mem, keys.ClockPath)
	got, err = keys.ReadClock(r.mem)
	if err != nil || got != (keys.ClockState{}) {
		t.Errorf("ReadClock with the file deleted = %+v, %v; want the zero state: the owner may delete it", got, err)
	}
}

// A clock record is refused, naming the file, unless it is exactly what the
// writer writes: every field of the first line, the CRC, the length, the key,
// and a body in the one canonical form.
func TestReadClockRefusesEverythingThatIsNotTheWrittenForm(t *testing.T) {
	body := `{"high_water":"2026-10-24T05:56:00Z"}`
	rows := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"no newline", "sidera-record 1 clock 766ed943 37 Y2xvY2s"},
		{"wrong magic", "sidera-recorc 1 clock 766ed943 37 Y2xvY2s\n" + body},
		{"future format version", "sidera-record 2 clock 766ed943 37 Y2xvY2s\n" + body},
		{"another kind", "sidera-record 1 init 766ed943 37 Y2xvY2s\n" + body},
		{"key is not the kind", "sidera-record 1 clock 766ed943 37 aW5pdA\n" + body},
		{"key with padding", "sidera-record 1 clock 766ed943 37 Y2xvY2s=\n" + body},
		{"crc of other bytes", "sidera-record 1 clock 766ed944 37 Y2xvY2s\n" + body},
		{"crc in capitals", "sidera-record 1 clock 766ED943 37 Y2xvY2s\n" + body},
		{"crc too short", "sidera-record 1 clock 66ed943 37 Y2xvY2s\n" + body},
		{"length too small", "sidera-record 1 clock 766ed943 36 Y2xvY2s\n" + body},
		{"length too large", "sidera-record 1 clock 766ed943 38 Y2xvY2s\n" + body},
		{"length with a leading zero", "sidera-record 1 clock 766ed943 037 Y2xvY2s\n" + body},
		{"length with a sign", "sidera-record 1 clock 766ed943 +37 Y2xvY2s\n" + body},
		{"double space", "sidera-record  1 clock 766ed943 37 Y2xvY2s\n" + body},
		{"seven fields", "sidera-record 1 clock 766ed943 37 Y2xvY2s x\n" + body},
		{"five fields", "sidera-record 1 clock 766ed943 37\n" + body},
		{"carriage return", "sidera-record 1 clock 766ed943 37 Y2xvY2s\r\n" + body},
		{"trailing newline after the body", "sidera-record 1 clock 766ed943 37 Y2xvY2s\n" + body + "\n"},
		{"unknown field", oracleHeader("clock", `{"high_water":"2026-10-24T05:56:00Z","x":1}`)},
		{"missing high water", oracleHeader("clock", `{}`)},
		{"null high water", oracleHeader("clock", `{"high_water":null}`)},
		{"empty pause", oracleHeader("clock", `{"high_water":"2026-10-24T05:56:00Z","rollback_paused_since":""}`)},
		{"duplicate key", oracleHeader("clock", `{"high_water":"2026-10-24T05:56:00Z","high_water":"2026-10-24T05:57:00Z"}`)},
		{"space in the body", oracleHeader("clock", `{"high_water": "2026-10-24T05:56:00Z"}`)},
		{"fields out of order", oracleHeader("clock", `{"rollback_paused_since":"2026-10-24T05:50:00Z","high_water":"2026-10-24T05:56:00Z"}`)},
		{"time with an offset", oracleHeader("clock", `{"high_water":"2026-10-24T07:56:00+02:00"}`)},
		{"time with fractions", oracleHeader("clock", `{"high_water":"2026-10-24T05:56:00.5Z"}`)},
		{"time that is not a time", oracleHeader("clock", `{"high_water":"yesterday"}`)},
		{"time before the epoch", oracleHeader("clock", `{"high_water":"1969-12-31T23:59:59Z"}`)},
		{"zero time", oracleHeader("clock", `{"high_water":"0001-01-01T00:00:00Z"}`)},
		{"not json", oracleHeader("clock", `high_water`)},
		{"an array", oracleHeader("clock", `[]`)},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			mem := goldenDisk(t)
			put(t, mem, keys.ClockPath, []byte(row.data))
			_, err := keys.ReadClock(mem)
			if !errors.Is(err, keys.ErrCorrupt) {
				t.Fatalf("ReadClock = %v, want ErrCorrupt", err)
			}
			asFileError(t, err, keys.ClockPath)
		})
	}
}

// oracleHeader is a clock record with a correct first line for the given body.
func oracleHeader(kind, body string) string { return string(oracleRecordFile(kind, []byte(body))) }

// The writer's output is the golden form, byte for byte, and a time is cut to
// whole seconds.
func TestSaveClockWritesTheGoldenForm(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()

	hw := time.Date(2026, 10, 24, 5, 56, 0, 999_000_000, time.UTC)
	if err := k.SaveClock(keys.ClockState{HighWater: hw}); err != nil {
		t.Fatal(err)
	}
	if got := string(r.read(keys.ClockPath)); got != goldenClockFile {
		t.Errorf("clock.rec = %q, want %q", got, goldenClockFile)
	}
	paused := keys.ClockState{HighWater: hw, RollbackPausedSince: time.Date(2026, 10, 24, 5, 50, 0, 0, time.UTC)}
	if err := k.SaveClock(paused); err != nil {
		t.Fatal(err)
	}
	if got := string(r.read(keys.ClockPath)); got != goldenClockPausedFile {
		t.Errorf("clock.rec = %q, want %q", got, goldenClockPausedFile)
	}
	got, err := keys.ReadClock(r.mem)
	if err != nil || got.HighWater != hw.Truncate(time.Second) || got.RollbackPausedSince != paused.RollbackPausedSince {
		t.Errorf("ReadClock = %+v, %v", got, err)
	}
}

// The persisted mark and the pause flag never go back through the periodic
// write: a lower mark, a cleared flag or another start of the pause is refused
// and writes nothing. Only the explicit clear resets them. A zero state is
// refused as well: it would erase the mark.
func TestSaveClockNeverGoesBack(t *testing.T) {
	hw := t0
	since := t0.Add(-10 * time.Minute)
	rows := []struct {
		name    string
		onDisk  keys.ClockState
		save    keys.ClockState
		refused bool
	}{
		{"raise the mark", keys.ClockState{HighWater: hw}, keys.ClockState{HighWater: hw.Add(time.Minute)}, false},
		{"same mark", keys.ClockState{HighWater: hw}, keys.ClockState{HighWater: hw}, false},
		{"same mark within the second", keys.ClockState{HighWater: hw}, keys.ClockState{HighWater: hw.Add(500 * time.Millisecond)}, false},
		{"lower the mark", keys.ClockState{HighWater: hw}, keys.ClockState{HighWater: hw.Add(-time.Second)}, true},
		{"zero state", keys.ClockState{HighWater: hw}, keys.ClockState{}, true},
		{"start the pause", keys.ClockState{HighWater: hw}, keys.ClockState{HighWater: hw, RollbackPausedSince: since}, false},
		{"start the pause with a raised mark", keys.ClockState{HighWater: hw}, keys.ClockState{HighWater: hw.Add(time.Minute), RollbackPausedSince: since}, false},
		{"keep the pause", keys.ClockState{HighWater: hw, RollbackPausedSince: since}, keys.ClockState{HighWater: hw.Add(time.Minute), RollbackPausedSince: since}, false},
		{"clear the pause", keys.ClockState{HighWater: hw, RollbackPausedSince: since}, keys.ClockState{HighWater: hw.Add(time.Minute)}, true},
		{"move the start of the pause", keys.ClockState{HighWater: hw, RollbackPausedSince: since}, keys.ClockState{HighWater: hw, RollbackPausedSince: since.Add(time.Minute)}, true},
		{"lower the mark of a paused clock", keys.ClockState{HighWater: hw, RollbackPausedSince: since}, keys.ClockState{HighWater: hw.Add(-time.Minute), RollbackPausedSince: since}, true},
		{"first write with no record", keys.ClockState{}, keys.ClockState{HighWater: hw}, false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := newRig(t)
			r.mustInit(1, t0)
			k := r.mustLoad()
			if row.onDisk == (keys.ClockState{}) {
				remove(t, r.mem, keys.ClockPath)
			} else if err := k.SaveClock(row.onDisk); err != nil {
				// the starting state is set through the same function; init wrote t0 which is not above hw
				t.Fatal(err)
			}
			before := snapshot(t, r.mem)
			r.obs.points = nil
			err := k.SaveClock(row.save)
			if row.refused {
				if !errors.Is(err, keys.ErrClockRegression) {
					t.Fatalf("SaveClock = %v, want ErrClockRegression", err)
				}
				asFileError(t, err, keys.ClockPath)
				if r.obs.count() != 0 || !reflect.DeepEqual(snapshot(t, r.mem), before) {
					t.Error("a refused save wrote")
				}
				return
			}
			if err != nil {
				t.Fatalf("SaveClock = %v", err)
			}
		})
	}
}

// The owner's clear resets the mark to now, which may be lower than the mark it
// replaces, and removes the pause flag in the same replacement: there is no
// state with the flag gone and the mark unchanged. The write is one replacement
// of one file.
func TestClearingTheRollbackResetsTheMarkAndTheFlagInOneReplacement(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	k := r.mustLoad()
	future := t0.Add(48 * time.Hour)
	if err := k.SaveClock(keys.ClockState{HighWater: future, RollbackPausedSince: future.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	now := t0.Add(time.Hour) // earlier than the mark: the clock really was set back
	r.obs.points = nil
	tx := r.w.Begin(clearDecl)
	defer tx.Close()
	if err := keys.PutClockCleared(tx, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Replace("result", "config/result-stand-in", []byte("{}"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, err := keys.ReadClock(r.mem)
	if err != nil || got != (keys.ClockState{HighWater: now}) {
		t.Errorf("after the clear ReadClock = %+v, %v; want the mark at now and no pause", got, err)
	}
	if string(r.read(keys.ClockPath)) != string(oracleRecordFile("clock", []byte(`{"high_water":"`+rfc3339(now)+`"}`))) {
		t.Error("the cleared record is not the canonical form")
	}
	var clockSteps int
	for _, n := range r.obs.names() {
		if strings.HasPrefix(n, "clock-clear/clock.") {
			clockSteps++
		}
	}
	if clockSteps != 5 {
		t.Errorf("the clear took %d steps of the clock role, want the 5 of one replacement: %v", clockSteps, r.obs.names())
	}
}
