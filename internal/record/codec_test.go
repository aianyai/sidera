package record

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
)

func TestTimeConversion(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	t.Run("required time", func(t *testing.T) {
		rows := []struct {
			rule string
			sec  int64
			ok   bool
		}{
			{"one second after the epoch is accepted", 1, true},
			{"a current time is accepted", 1700000000, true},
			{"the largest wire time is accepted", MaxUnixSeconds, true},
			{"zero is refused", 0, false},
			{"negative is refused", -1, false},
			{"beyond the largest wire time is refused", MaxUnixSeconds + 1, false},
			{"the largest int64 is refused", math.MaxInt64, false},
			{"the smallest int64 is refused", math.MinInt64, false},
		}
		for _, r := range rows {
			t.Run(r.rule, func(t *testing.T) {
				tm, err := TimeFromWire(r.sec)
				if (err == nil) != r.ok || (err != nil && !errors.Is(err, ErrTime)) {
					t.Fatalf("TimeFromWire error = %v", err)
				}
				if r.ok {
					if tm.Unix() != r.sec || tm.Location() != time.UTC {
						t.Fatalf("time = %v", tm)
					}
					back, err := TimeToWire(tm)
					if err != nil || back != r.sec {
						t.Fatalf("TimeToWire = %d, %v", back, err)
					}
				}
			})
		}
		t.Run("the zero time is refused", func(t *testing.T) {
			if _, err := TimeToWire(time.Time{}); !errors.Is(err, ErrTime) {
				t.Fatalf("error = %v", err)
			}
		})
		t.Run("a time before 1970 is refused", func(t *testing.T) {
			if _, err := TimeToWire(time.Unix(-10, 0)); !errors.Is(err, ErrTime) {
				t.Fatalf("error = %v", err)
			}
		})
		t.Run("sub-second precision is dropped", func(t *testing.T) {
			sec, err := TimeToWire(time.Unix(100, 999_999_999))
			if err != nil || sec != 100 {
				t.Fatalf("TimeToWire = %d, %v", sec, err)
			}
		})
		t.Run("a non-UTC time converts by instant", func(t *testing.T) {
			zone := time.FixedZone("x", 5*3600)
			sec, err := TimeToWire(time.Unix(1700000000, 0).In(zone))
			if err != nil || sec != 1700000000 {
				t.Fatalf("TimeToWire = %d, %v", sec, err)
			}
		})
	})
	t.Run("optional time", func(t *testing.T) {
		if p, err := OptionalTimeToWire(time.Time{}); p != nil || err != nil {
			t.Fatalf("zero time = %v, %v; want nil", p, err)
		}
		if p, err := OptionalTimeToWire(time.Unix(5, 0)); err != nil || p == nil || *p != 5 {
			t.Fatalf("time = %v, %v", p, err)
		}
		if _, err := OptionalTimeToWire(time.Unix(-5, 0)); !errors.Is(err, ErrTime) {
			t.Fatalf("negative time: %v", err)
		}
		if tm, err := OptionalTimeFromWire(nil); !tm.IsZero() || err != nil {
			t.Fatalf("absent = %v, %v; want zero time", tm, err)
		}
		if _, err := OptionalTimeFromWire(i64(0)); !errors.Is(err, ErrTime) {
			t.Fatalf("explicit zero: %v", err)
		}
		if _, err := OptionalTimeFromWire(i64(-1)); !errors.Is(err, ErrTime) {
			t.Fatalf("negative: %v", err)
		}
		if tm, err := OptionalTimeFromWire(i64(7)); err != nil || tm.Unix() != 7 {
			t.Fatalf("value = %v, %v", tm, err)
		}
	})
	t.Run("counters start at one", func(t *testing.T) {
		if err := RequireCounter(0); !errors.Is(err, ErrCounter) {
			t.Fatalf("0: %v", err)
		}
		for _, v := range []uint64{1, 2, math.MaxUint64} {
			if err := RequireCounter(v); err != nil {
				t.Fatalf("%d: %v", v, err)
			}
		}
	})
}

func TestLevelTable(t *testing.T) {
	rows := []struct {
		rule  string
		level admin.Level
		wire  uint64
	}{
		{"view is 1", admin.LevelView, 1},
		{"manage is 2", admin.LevelManage, 2},
		{"full control is 3", admin.LevelFullControl, 3},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			w, err := LevelToWire(r.level)
			if err != nil || w != r.wire {
				t.Fatalf("LevelToWire = %d, %v", w, err)
			}
			l, err := LevelFromWire(r.wire)
			if err != nil || l != r.level {
				t.Fatalf("LevelFromWire = %v, %v", l, err)
			}
		})
	}
	for _, v := range []uint64{0, 4, 255, math.MaxUint64} {
		t.Run("wire value outside the table is refused", func(t *testing.T) {
			if l, err := LevelFromWire(v); !errors.Is(err, ErrLevel) || l != admin.LevelNone {
				t.Fatalf("LevelFromWire(%d) = %v, %v", v, l, err)
			}
		})
	}
	for _, l := range []admin.Level{admin.LevelNone, -1, 4} {
		t.Run("level outside the table is refused on write", func(t *testing.T) {
			if _, err := LevelToWire(l); !errors.Is(err, ErrLevel) {
				t.Fatalf("LevelToWire(%d): %v", l, err)
			}
		})
	}
}

func TestStrictUnmarshal(t *testing.T) {
	type small struct {
		A uint64 `cbor:"1,keyasint"`
		B string `cbor:"2,keyasint,omitempty"`
	}
	rows := []struct {
		rule string
		in   []byte
		want error
	}{
		{"canonical input is accepted", cborw.mapOf(cborw.uint(1), cborw.uint(7)), nil},
		{"optional field present is accepted", cborw.mapOf(cborw.uint(1), cborw.uint(7), cborw.uint(2), cborw.tstr("x")), nil},
		{"optional field written as an empty value is refused", cborw.mapOf(cborw.uint(1), cborw.uint(7), cborw.uint(2), cborw.tstr("")), ErrNotCanonical},
		{"duplicate key", cborw.mapOf(cborw.uint(1), cborw.uint(7), cborw.uint(1), cborw.uint(8)), ErrMalformed},
		{"unknown key", cborw.mapOf(cborw.uint(1), cborw.uint(7), cborw.uint(9), cborw.uint(8)), ErrMalformed},
		{"negative key", cborw.mapOf([]byte{0x20}, cborw.uint(7)), ErrMalformed},
		{"text key", cborw.mapOf(cborw.tstr("A"), cborw.uint(7)), ErrMalformed},
		{"non-shortest value", cborw.mapOf(cborw.uint(1), []byte{0x18, 0x07}), ErrNotCanonical},
		{"indefinite-length text", cborw.mapOf(cborw.uint(1), cborw.uint(7), cborw.uint(2), []byte{0x7f, 0x61, 'x', 0xff}), ErrMalformed},
		{"tag", cborw.mapOf(cborw.uint(1), append([]byte{0xc1}, cborw.uint(7)...)), ErrMalformed},
		{"floating point where an integer is expected", cborw.mapOf(cborw.uint(1), []byte{0xf9, 0x3c, 0x00}), ErrMalformed},
		{"NaN where an integer is expected", cborw.mapOf(cborw.uint(1), []byte{0xf9, 0x7e, 0x00}), ErrMalformed},
		{"negative number for an unsigned field", cborw.mapOf(cborw.uint(1), []byte{0x20}), ErrMalformed},
		{"null for the required field", cborw.mapOf(cborw.uint(1), []byte{0xf6}), ErrNotCanonical},
		{"keys out of order", cborw.mapOf(cborw.uint(2), cborw.tstr("x"), cborw.uint(1), cborw.uint(7)), ErrNotCanonical},
		{"trailing bytes", append(cborw.mapOf(cborw.uint(1), cborw.uint(7)), 0), ErrMalformed},
		{"truncated input", cborw.mapOf(cborw.uint(1), cborw.uint(7))[:2], ErrMalformed},
		{"not a map", cborw.uint(1), ErrMalformed},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			var v small
			err := UnmarshalStrict(r.in, &v, DefaultMaxInput)
			if !errors.Is(err, r.want) && !(r.want == nil && err == nil) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
		})
	}
}

func note(t *testing.T, payload []byte) error {
	t.Helper()
	rec, err := Sign(noteType, payload, seedSigner(t, 7))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify(noteDescriptor, rec)
	return err
}

// A record type defined entirely outside the package's own types, with an
// optional field: this is where the optional-field rules apply.
func TestOptionalFieldsInAnExternalRecordType(t *testing.T) {
	text := cborw.tstr("hello")
	rows := []struct {
		rule    string
		payload []byte
		want    error
	}{
		{"optional field left out", cborw.mapOf(cborw.uint(1), text), nil},
		{"optional field present", cborw.mapOf(cborw.uint(1), text, cborw.uint(2), cborw.uint(1700000000)), nil},
		{"optional field written as an explicit 0", cborw.mapOf(cborw.uint(1), text, cborw.uint(2), cborw.uint(0)), ErrTime},
		{"optional field written as a negative time", cborw.mapOf(cborw.uint(1), text, cborw.uint(2), []byte{0x20}), ErrTime},
		{"optional field written as null", cborw.mapOf(cborw.uint(1), text, cborw.uint(2), []byte{0xf6}), ErrNotCanonical},
		{"optional field beyond the wire range", cborw.mapOf(cborw.uint(1), text, cborw.uint(2), cborw.uint(uint64(MaxUnixSeconds)+1)), ErrTime},
		{"required field left out", cborw.mapOf(cborw.uint(2), cborw.uint(5)), ErrNotCanonical},
		{"required field empty", cborw.mapOf(cborw.uint(1), cborw.tstr("")), ErrPayload},
		{"unknown field in the payload", cborw.mapOf(cborw.uint(1), text, cborw.uint(3), cborw.uint(1)), ErrMalformed},
		{"duplicate key in the payload", cborw.mapOf(cborw.uint(1), text, cborw.uint(1), text), ErrMalformed},
		{"indefinite length in the payload", append(append([]byte{0xbf}, cborw.uint(1)...), append(text, 0xff)...), ErrMalformed},
		{"tag in the payload", cborw.mapOf(cborw.uint(1), append([]byte{0xc0}, text...)), ErrMalformed},
		{"trailing bytes after the payload", append(cborw.mapOf(cborw.uint(1), text), 0x00), ErrMalformed},
		{"non-shortest integer in the payload", cborw.mapOf(cborw.uint(1), text, cborw.uint(2), []byte{0x1b, 0, 0, 0, 0, 0x65, 0x53, 0xf1, 0x00}), ErrNotCanonical},
		{"payload keys out of order", cborw.mapOf(cborw.uint(2), cborw.uint(1700000000), cborw.uint(1), text), ErrNotCanonical},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			err := note(t, r.payload)
			if !errors.Is(err, r.want) && !(r.want == nil && err == nil) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
		})
	}
	t.Run("an unset optional time is written by leaving the key out", func(t *testing.T) {
		b, err := noteDescriptor.encode(noteRecord{Text: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		if want := cborw.mapOf(cborw.uint(1), text); string(b) != string(want) {
			t.Fatalf("payload = %x, want %x", b, want)
		}
		b, err = noteDescriptor.encode(noteRecord{Text: "hello", NotAfter: time.Unix(1700000000, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if want := cborw.mapOf(cborw.uint(1), text, cborw.uint(2), cborw.uint(1700000000)); string(b) != string(want) {
			t.Fatalf("payload = %x, want %x", b, want)
		}
	})
}
