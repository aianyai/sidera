package engine

import (
	"testing"
	"time"
)

// A step between two readings of the clock counts for no more than a little over the
// period in which the loop wakes: the period of the clock state's task and a half
// more; without that task the longest period of the others; with none, every step
// counts whole.
func TestTheStepCapOfTheAwakeTimeFollowsThePeriodicTasks(t *testing.T) {
	task := func(TaskInput) error { return nil }
	for _, c := range []struct {
		name  string
		build func(e *Engine)
		want  time.Duration
	}{
		{"a clock task", func(e *Engine) {
			_ = e.AddPeriodic("slow", time.Hour, task)
			_ = e.AddClockState("clock", 40*time.Second, task)
		}, time.Minute},
		{"no clock task", func(e *Engine) {
			_ = e.AddPeriodic("fast", time.Minute, task)
			_ = e.AddPeriodic("slow", time.Hour, task)
		}, time.Hour},
		{"no periodic task", func(*Engine) {}, 0},
	} {
		e := &Engine{periodic: map[string]*periodicTask{}}
		e.setPhase(PhaseOpened)
		c.build(e)
		if got := e.awakeStepCap(); got != c.want {
			t.Errorf("%s: the cap is %v, want %v", c.name, got, c.want)
		}
	}

	var a awakeTime
	start := time.Unix(0, 0)
	a.begin(start, 0)
	a.advance(start.Add(8 * time.Hour))
	if a.total != 8*time.Hour {
		t.Errorf("without a cap a step of 8 hours counts %v, want all of it", a.total)
	}
}
