package clock_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/timing"
)

var mark = time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC)

// TestCheckWallJudgmentTable is the rollback judgment table: every reason, the
// boundary exactly at the clock tolerance on both sides, and the first start.
func TestCheckWallJudgmentTable(t *testing.T) {
	tests := []struct {
		name      string
		now       time.Time
		highWater time.Time
		verdict   clock.WallVerdict
		reason    clock.WallReason
	}{
		{"first start has no high water", mark, time.Time{}, clock.WallOK, clock.ReasonNoHighWater},
		{"first start accepts any reading, even the zero time", time.Time{}, time.Time{}, clock.WallOK, clock.ReasonNoHighWater},
		{"now equal to the high water is not behind", mark, mark, clock.WallOK, clock.ReasonNotBehind},
		{"now one nanosecond ahead is not behind", mark.Add(1), mark, clock.WallOK, clock.ReasonNotBehind},
		{"now far ahead is not behind", mark.Add(30 * 24 * time.Hour), mark, clock.WallOK, clock.ReasonNotBehind},
		{"one nanosecond behind is within the tolerance", mark.Add(-1), mark, clock.WallOK, clock.ReasonWithinTolerance},
		{"one minute behind is within the tolerance", mark.Add(-time.Minute), mark, clock.WallOK, clock.ReasonWithinTolerance},
		{"just inside the tolerance is within it", mark.Add(-timing.ClockTolerance + 1), mark, clock.WallOK, clock.ReasonWithinTolerance},
		{"exactly the tolerance behind is within it", mark.Add(-timing.ClockTolerance), mark, clock.WallOK, clock.ReasonWithinTolerance},
		{"one nanosecond past the tolerance is a rollback", mark.Add(-timing.ClockTolerance - 1), mark, clock.WallRolledBack, clock.ReasonBehindHighWater},
		{"one hour behind is a rollback", mark.Add(-time.Hour), mark, clock.WallRolledBack, clock.ReasonBehindHighWater},
		{"a year behind is a rollback", mark.AddDate(-1, 0, 0), mark, clock.WallRolledBack, clock.ReasonBehindHighWater},
		{"the zero time against a recorded high water is a rollback", time.Time{}, mark, clock.WallRolledBack, clock.ReasonBehindHighWater},
		{
			"the comparison is by instant, not by location",
			mark.Add(-timing.ClockTolerance).In(time.FixedZone("UTC+9", 9*60*60)), mark,
			clock.WallOK, clock.ReasonWithinTolerance,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, reason := clock.CheckWall(tt.now, tt.highWater)
			if verdict != tt.verdict || reason != tt.reason {
				t.Fatalf("CheckWall(%v, %v) = (%v, %v), want (%v, %v)",
					tt.now, tt.highWater, verdict, reason, tt.verdict, tt.reason)
			}
		})
	}
}

func TestWallVerdictAndReasonValuesAreDistinctAndNonZero(t *testing.T) {
	verdicts := []clock.WallVerdict{clock.WallOK, clock.WallRolledBack}
	reasons := []clock.WallReason{
		clock.ReasonNoHighWater, clock.ReasonNotBehind,
		clock.ReasonWithinTolerance, clock.ReasonBehindHighWater,
	}
	seenVerdict := map[clock.WallVerdict]bool{}
	for _, v := range verdicts {
		if v == 0 || seenVerdict[v] {
			t.Fatalf("verdict %d is zero or repeated: the zero value must mean unset", v)
		}
		seenVerdict[v] = true
	}
	seenReason := map[clock.WallReason]bool{}
	for _, r := range reasons {
		if r == 0 || seenReason[r] {
			t.Fatalf("reason %d is zero or repeated: the zero value must mean unset", r)
		}
		seenReason[r] = true
	}
}

func TestHighWaterFirstStartAcceptsAndRecordsTheReading(t *testing.T) {
	h := clock.NewHighWater(time.Time{})
	if !h.Value().IsZero() {
		t.Fatalf("Value() = %v before any reading, want zero", h.Value())
	}
	verdict, reason := h.Observe(mark)
	if verdict != clock.WallOK || reason != clock.ReasonNoHighWater {
		t.Fatalf("first Observe = (%v, %v), want (WallOK, ReasonNoHighWater)", verdict, reason)
	}
	if !h.Value().Equal(mark) {
		t.Fatalf("Value() = %v, want %v", h.Value(), mark)
	}
}

func TestHighWaterIsRaisedOnlyByReadingsAtOrAheadOfIt(t *testing.T) {
	h := clock.NewHighWater(mark)

	ahead := mark.Add(5 * time.Minute)
	if verdict, reason := h.Observe(ahead); verdict != clock.WallOK || reason != clock.ReasonNotBehind {
		t.Fatalf("Observe(ahead) = (%v, %v), want (WallOK, ReasonNotBehind)", verdict, reason)
	}
	if !h.Value().Equal(ahead) {
		t.Fatalf("Value() = %v after a reading ahead, want %v", h.Value(), ahead)
	}

	if verdict, reason := h.Observe(ahead); verdict != clock.WallOK || reason != clock.ReasonNotBehind {
		t.Fatalf("Observe(equal) = (%v, %v), want (WallOK, ReasonNotBehind)", verdict, reason)
	}
	if !h.Value().Equal(ahead) {
		t.Fatalf("Value() = %v after an equal reading, want %v", h.Value(), ahead)
	}
}

func TestHighWaterIsNotLoweredByAReadingWithinTheTolerance(t *testing.T) {
	h := clock.NewHighWater(mark)
	behind := mark.Add(-timing.ClockTolerance)
	if verdict, reason := h.Observe(behind); verdict != clock.WallOK || reason != clock.ReasonWithinTolerance {
		t.Fatalf("Observe(behind by the tolerance) = (%v, %v), want (WallOK, ReasonWithinTolerance)", verdict, reason)
	}
	if !h.Value().Equal(mark) {
		t.Fatalf("Value() = %v, want it unchanged at %v", h.Value(), mark)
	}
}

func TestHighWaterIsNeverLoweredByARolledBackReading(t *testing.T) {
	h := clock.NewHighWater(mark)
	for _, back := range []time.Duration{
		timing.ClockTolerance + 1, time.Hour, 24 * time.Hour, 365 * 24 * time.Hour,
	} {
		verdict, reason := h.Observe(mark.Add(-back))
		if verdict != clock.WallRolledBack || reason != clock.ReasonBehindHighWater {
			t.Fatalf("Observe(%v behind) = (%v, %v), want (WallRolledBack, ReasonBehindHighWater)", back, verdict, reason)
		}
		if !h.Value().Equal(mark) {
			t.Fatalf("Value() = %v after a reading %v behind, want it unchanged at %v", h.Value(), back, mark)
		}
	}
}

func TestHighWaterFromPersistedValueJudgesTheFirstReadingAfterStart(t *testing.T) {
	h := clock.NewHighWater(mark)
	verdict, reason := h.Observe(mark.Add(-time.Hour))
	if verdict != clock.WallRolledBack || reason != clock.ReasonBehindHighWater {
		t.Fatalf("Observe = (%v, %v), want (WallRolledBack, ReasonBehindHighWater)", verdict, reason)
	}
	if !h.Value().Equal(mark) {
		t.Fatalf("Value() = %v, want the persisted %v", h.Value(), mark)
	}
}

// A rollback is judged against the unchanged mark each time: observing does not
// remember that an earlier reading was rejected, so a reading that has come
// back within the tolerance is accepted again. Keeping the node paused until
// the owner clears it is the caller's state, not the mark's.
func TestHighWaterJudgesEachReadingAgainstTheUnchangedMark(t *testing.T) {
	h := clock.NewHighWater(mark)
	if verdict, _ := h.Observe(mark.Add(-time.Hour)); verdict != clock.WallRolledBack {
		t.Fatalf("setup: verdict = %v, want WallRolledBack", verdict)
	}
	if verdict, _ := h.Observe(mark.Add(-time.Hour)); verdict != clock.WallRolledBack {
		t.Fatalf("a repeated rolled-back reading: verdict = %v, want WallRolledBack", verdict)
	}
	verdict, reason := h.Observe(mark.Add(-time.Minute))
	if verdict != clock.WallOK || reason != clock.ReasonWithinTolerance {
		t.Fatalf("a reading back within tolerance = (%v, %v), want (WallOK, ReasonWithinTolerance)", verdict, reason)
	}
}

func TestHighWaterResetAcceptsNowAsTheNewMark(t *testing.T) {
	h := clock.NewHighWater(mark)
	back := mark.Add(-24 * time.Hour)
	if verdict, _ := h.Observe(back); verdict != clock.WallRolledBack {
		t.Fatalf("setup: verdict = %v, want WallRolledBack", verdict)
	}

	h.Reset(back)
	if !h.Value().Equal(back) {
		t.Fatalf("Value() = %v after Reset, want the lower %v", h.Value(), back)
	}
	if verdict, reason := h.Observe(back); verdict != clock.WallOK || reason != clock.ReasonNotBehind {
		t.Fatalf("Observe(reset time) = (%v, %v), want (WallOK, ReasonNotBehind)", verdict, reason)
	}
	if verdict, reason := h.Observe(back.Add(time.Minute)); verdict != clock.WallOK || reason != clock.ReasonNotBehind {
		t.Fatalf("Observe(after reset) = (%v, %v), want (WallOK, ReasonNotBehind)", verdict, reason)
	}
	// The new mark governs later judgments: the old one no longer does.
	h.Reset(back)
	if verdict, reason := h.Observe(back.Add(-time.Hour)); verdict != clock.WallRolledBack || reason != clock.ReasonBehindHighWater {
		t.Fatalf("Observe(behind the reset mark) = (%v, %v), want (WallRolledBack, ReasonBehindHighWater)", verdict, reason)
	}
}

// TestHighWaterMatchesAnIndependentModel replays a seeded sequence of readings,
// minutes apart as on a real node, through Observe and a separate model written
// with integer seconds, and checks that every reason was exercised.
func TestHighWaterMatchesAnIndependentModel(t *testing.T) {
	const (
		steps     = 4000
		stepSec   = 30 // readings land on both sides of every boundary
		spreadSec = 600
	)
	toleranceSec := int64(timing.ClockTolerance / time.Second)
	rng := rand.New(rand.NewPCG(1, 2))
	base := mark.Unix()

	h := clock.NewHighWater(time.Time{})
	var modelMark int64 // Unix seconds; 0 means nothing recorded
	counts := map[clock.WallReason]int{}
	// How often a reading sat exactly at, and just beyond, the tolerance behind
	// the mark: the two sides of the inclusive boundary.
	var atTolerance, beyondTolerance int

	for i := range steps {
		// Readings scatter around the model's mark, so all four outcomes recur.
		center := base
		if modelMark != 0 {
			center = modelMark
		}
		offset := int64(rng.IntN(2*spreadSec/stepSec+1)-spreadSec/stepSec) * stepSec
		reading := center + offset

		var wantVerdict clock.WallVerdict
		var wantReason clock.WallReason
		switch {
		case modelMark == 0:
			wantVerdict, wantReason = clock.WallOK, clock.ReasonNoHighWater
			modelMark = reading
		case reading >= modelMark:
			wantVerdict, wantReason = clock.WallOK, clock.ReasonNotBehind
			modelMark = reading
		case modelMark-reading <= toleranceSec:
			wantVerdict, wantReason = clock.WallOK, clock.ReasonWithinTolerance
		default:
			wantVerdict, wantReason = clock.WallRolledBack, clock.ReasonBehindHighWater
		}

		gotVerdict, gotReason := h.Observe(time.Unix(reading, 0).UTC())
		if gotVerdict != wantVerdict || gotReason != wantReason {
			t.Fatalf("step %d: Observe(%d) = (%v, %v), want (%v, %v), model mark %d",
				i, reading, gotVerdict, gotReason, wantVerdict, wantReason, modelMark)
		}
		if got := h.Value().Unix(); got != modelMark {
			t.Fatalf("step %d: Value() = %d, want %d", i, got, modelMark)
		}
		counts[gotReason]++
		switch modelMark - reading {
		case toleranceSec:
			atTolerance++
		case toleranceSec + stepSec:
			beyondTolerance++
		}
	}

	for _, reason := range []clock.WallReason{
		clock.ReasonNoHighWater, clock.ReasonNotBehind,
		clock.ReasonWithinTolerance, clock.ReasonBehindHighWater,
	} {
		if counts[reason] == 0 {
			t.Fatalf("reason %v never exercised in %d steps: %v", reason, steps, counts)
		}
	}
	if atTolerance == 0 || beyondTolerance == 0 {
		t.Fatalf("boundary offsets never exercised: exactly the tolerance %d times, tolerance+%ds %d times",
			atTolerance, stepSec, beyondTolerance)
	}
	if counts[clock.ReasonNoHighWater] != 1 {
		t.Fatalf("ReasonNoHighWater taken %d times, want exactly once (the first reading)", counts[clock.ReasonNoHighWater])
	}
	t.Logf("reasons taken over %d readings: %v", steps, counts)
}
