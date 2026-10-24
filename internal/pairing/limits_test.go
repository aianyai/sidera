package pairing

import (
	"errors"
	"math/rand"
	"testing"
	"time"
)

func at(min int) time.Time { return testNow.Add(time.Duration(min) * time.Minute) }

func TestParamsValidate(t *testing.T) {
	if err := DefaultParams().Validate(); err != nil {
		t.Fatalf("default params: %v", err)
	}
	if err := (Params{}).Validate(); !errors.Is(err, ErrParams) {
		t.Fatalf("zero params: %v", err)
	}
	for name, mutate := range map[string]func(*Params){
		"zero online timeout":   func(p *Params) { p.OnlineTimeout = 0 },
		"sub-second duration":   func(p *Params) { p.PendingTTL = 1500 * time.Millisecond },
		"negative retention":    func(p *Params) { p.ResultRetention = -time.Second },
		"zero pending cap":      func(p *Params) { p.PendingCap = 0 },
		"zero reveal budget":    func(p *Params) { p.RevealBudget = 0 },
		"zero failure limit":    func(p *Params) { p.FailureLimit = 0 },
		"backoff max below min": func(p *Params) { p.FetchBackoffMax = p.FetchBackoffMin - time.Second },
		"zero expectation cap":  func(p *Params) { p.ExpectationCap = 0 },
		"zero failure window":   func(p *Params) { p.FailureWindow = 0 },
		"zero reveal window":    func(p *Params) { p.RevealWindow = 0 },
	} {
		p := DefaultParams()
		mutate(&p)
		if err := p.Validate(); !errors.Is(err, ErrParams) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRevealBudget(t *testing.T) {
	p := DefaultParams()
	var l Limits
	var err error
	for i := 0; i < 5; i++ {
		if ok, _ := l.RevealAllowed(p, at(i)); !ok {
			t.Fatalf("reveal %d refused", i+1)
		}
		if l, err = l.SpendReveal(p, at(i)); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("the sixth reveal within the hour is refused and says when", func(t *testing.T) {
		ok, retry := l.RevealAllowed(p, at(10))
		if ok || retry != 50*time.Minute {
			t.Fatalf("allowed=%v retry=%v, want refused with 50m (the first reveal at minute 0 frees at minute 60)", ok, retry)
		}
		if _, err := l.SpendReveal(p, at(10)); !errors.Is(err, ErrBudget) {
			t.Fatalf("spend: %v", err)
		}
	})
	t.Run("an entry stops counting at exactly the window", func(t *testing.T) {
		if ok, _ := l.RevealAllowed(p, at(60).Add(-time.Second)); ok {
			t.Fatal("allowed one second before the first reveal slides out")
		}
		if ok, _ := l.RevealAllowed(p, at(60)); !ok {
			t.Fatal("refused at the instant the first reveal slides out")
		}
	})
	t.Run("the hint is positive whole seconds", func(t *testing.T) {
		_, retry := l.RevealAllowed(p, at(60).Add(-time.Millisecond))
		if retry != time.Second {
			t.Fatalf("retry = %v, want 1s", retry)
		}
	})
	t.Run("old entries are dropped when a reveal is spent", func(t *testing.T) {
		l2, err := l.SpendReveal(p, at(61))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(l2.Reveals()); n != 4 {
			t.Fatalf("kept %d reveals, want the 3 still in the window (minutes 2 to 4) plus the new one", n)
		}
	})
	t.Run("failures do not use the reveal budget", func(t *testing.T) {
		l2 := l.RecordFailure(p, at(1))
		if ok, _ := l2.RevealAllowed(p, at(1)); ok {
			t.Fatal("budget changed by a failure")
		}
	})
}

func TestVerificationFailurePause(t *testing.T) {
	p := DefaultParams()
	var l Limits
	l = l.RecordFailure(p, at(0))
	l = l.RecordFailure(p, at(5))
	if paused, _ := l.Paused(p, at(6)); paused {
		t.Fatal("paused after two failures")
	}
	l = l.RecordFailure(p, at(10))
	t.Run("the third failure pauses", func(t *testing.T) {
		paused, retry := l.Paused(p, at(10))
		if !paused || retry != 50*time.Minute {
			t.Fatalf("paused=%v retry=%v, want paused until the first failure slides out", paused, retry)
		}
	})
	t.Run("the pause lifts by itself when the earliest failure slides out", func(t *testing.T) {
		if paused, _ := l.Paused(p, at(60).Add(-time.Second)); !paused {
			t.Fatal("lifted early")
		}
		if paused, _ := l.Paused(p, at(60)); paused {
			t.Fatal("still paused when the first failure left the window")
		}
	})
	t.Run("a fourth failure during the pause extends it only as far as the window allows", func(t *testing.T) {
		l4 := l.RecordFailure(p, at(20))
		paused, retry := l4.Paused(p, at(20))
		// Kept failures are the last three: minutes 5, 10, 20. The pause ends
		// when minute 5 slides out, never later than a full window from now.
		if !paused || retry != 45*time.Minute {
			t.Fatalf("paused=%v retry=%v, want 45m", paused, retry)
		}
		if n := len(l4.Failures()); n != p.FailureLimit {
			t.Fatalf("kept %d failures, want at most %d", n, p.FailureLimit)
		}
	})
	t.Run("failures spread over more than the window never pause", func(t *testing.T) {
		var s Limits
		for i := 0; i < 10; i++ {
			s = s.RecordFailure(p, at(i*31))
			if paused, _ := s.Paused(p, at(i*31)); paused {
				t.Fatalf("paused at failure %d", i+1)
			}
		}
	})
}

// A model of both counters that keeps every timestamp and counts by looking at
// the whole history, compared with Limits over random histories.
func TestLimitsAgainstAModel(t *testing.T) {
	p := DefaultParams()
	rng := rand.New(rand.NewSource(7))
	var (
		l                Limits
		reveals, fails   []time.Time
		now              = testNow
		spent, refused   int
		pausedN, openedN int
	)
	count := func(ts []time.Time, window time.Duration) (n int, oldest time.Time) {
		for _, x := range ts {
			if now.Sub(x) < window {
				if n == 0 || x.Before(oldest) {
					oldest = x
				}
				n++
			}
		}
		return
	}
	for step := 0; step < 20000; step++ {
		now = now.Add(time.Duration(rng.Intn(900)) * time.Second) // minutes-scale steps within the hour windows
		switch []int{0, 0, 1, 2, 2, 2, 0, 2}[rng.Intn(8)] {
		case 0:
			n, _ := count(reveals, p.RevealWindow)
			ok, _ := l.RevealAllowed(p, now)
			if ok != (n < p.RevealBudget) {
				t.Fatalf("step %d: allowed=%v with %d in the window", step, ok, n)
			}
			next, err := l.SpendReveal(p, now)
			if ok {
				if err != nil {
					t.Fatal(err)
				}
				l = next
				reveals = append(reveals, now)
				spent++
			} else {
				if !errors.Is(err, ErrBudget) {
					t.Fatalf("spend over budget: %v", err)
				}
				refused++
			}
		case 1:
			l = l.RecordFailure(p, now)
			fails = append(fails, now)
		case 2:
			n, _ := count(fails, p.FailureWindow)
			paused, retry := l.Paused(p, now)
			if paused != (n >= p.FailureLimit) {
				t.Fatalf("step %d: paused=%v with %d failures in the window", step, paused, n)
			}
			if paused {
				pausedN++
				if retry < time.Second || retry > p.FailureWindow {
					t.Fatalf("step %d: retry hint %v out of range", step, retry)
				}
				// At the hinted instant the pause has lifted.
				lifted := now.Add(retry)
				live := 0
				for _, x := range fails {
					if lifted.Sub(x) < p.FailureWindow {
						live++
					}
				}
				if live >= p.FailureLimit {
					t.Fatalf("step %d: still %d failures at the hinted instant", step, live)
				}
			} else {
				openedN++
			}
		}
	}
	if spent < 500 || refused < 500 || pausedN < 500 || openedN < 500 {
		t.Fatalf("coverage: spent=%d refused=%d paused=%d open=%d", spent, refused, pausedN, openedN)
	}
	t.Logf("reveals spent %d, refused %d; pause checks paused %d, open %d", spent, refused, pausedN, openedN)
}

func TestNewLimits(t *testing.T) {
	if _, err := NewLimits([]time.Time{at(2), at(1)}, nil); err == nil {
		t.Fatal("descending reveals accepted")
	}
	if _, err := NewLimits(nil, []time.Time{testNow.Add(time.Millisecond)}); err == nil {
		t.Fatal("sub-second failure accepted")
	}
	if _, err := NewLimits([]time.Time{{}}, nil); err == nil {
		t.Fatal("zero time accepted")
	}
	l, err := NewLimits([]time.Time{at(1), at(2)}, []time.Time{at(3)})
	if err != nil || len(l.Reveals()) != 2 || len(l.Failures()) != 1 {
		t.Fatalf("NewLimits: %v %v", l, err)
	}
}
