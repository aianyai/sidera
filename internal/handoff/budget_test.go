package handoff

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEstimateTokens(t *testing.T) {
	rows := []struct {
		rule string
		in   string
		want int
	}{
		{"empty text costs nothing", "", 0},
		{"every ASCII byte counts one, however dense", "a1!{}\n", 6},
		{"a CJK rune counts its three bytes, at least one token", "中文", 6},
		{"an emoji counts its four bytes", "😀", 4},
		{"a two-byte letter counts two", "é", 2},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := EstimateTokens(r.in); got != r.want {
				t.Fatalf("EstimateTokens(%q) = %d, want %d", r.in, got, r.want)
			}
			if got := (Budget{UnitTokens, 1}).Measure(r.in); got != r.want {
				t.Fatalf("Measure = %d, want %d", got, r.want)
			}
		})
	}
}

func TestUTF16Len(t *testing.T) {
	rows := []struct {
		rule string
		in   string
		want int
	}{
		{"ASCII is one unit per character", "abc", 3},
		{"a basic-plane CJK character is one unit", "中文", 2},
		{"an emoji outside the basic plane is two units", "😀", 2},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := UTF16Len(r.in); got != r.want {
				t.Fatalf("UTF16Len(%q) = %d, want %d", r.in, got, r.want)
			}
			if got := (Budget{UnitChars, 1}).Measure(r.in); got != r.want {
				t.Fatalf("Measure = %d, want %d", got, r.want)
			}
		})
	}
}

// randomText mixes ASCII, punctuation, CJK, accented and emoji runes.
func randomText(rng *rand.Rand, runes int) string {
	pools := []string{
		"abcdefghijklmnopqrstuvwxyz ABCXYZ0123456789",
		"{}[]()<>&\"\\'\n\t!@#$%^*-_=+;:,./?",
		"你好世界这是一条消息中文日本語",
		"éüñßø",
		"😀🚀🎉",
	}
	var b strings.Builder
	for i := 0; i < runes; i++ {
		pool := []rune(pools[rng.Intn(len(pools))])
		b.WriteRune(pool[rng.Intn(len(pool))])
	}
	return b.String()
}

// The estimate is the common currency between character and token limits, so
// it must never fall below either count.
func TestEstimateTokensNeverUndercounts(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 500; i++ {
		s := randomText(rng, rng.Intn(200))
		est := EstimateTokens(s)
		if est < UTF16Len(s) || est < utf8.RuneCountInString(s) {
			t.Fatalf("estimate %d is below the %d UTF-16 units / %d runes of %q", est, UTF16Len(s), utf8.RuneCountInString(s), s)
		}
	}
}

// The visible limits the default budget is derived from, as measured on two
// runtimes. They are test data: the package declares none, an adapter's
// manifest does. The unit is the one the runtime path counts in.
var visibleLimits = []struct {
	name   string
	budget Budget
}{
	{"bash success, inline", Budget{UnitChars, 30_000}},
	{"bash failure, head and tail", Budget{UnitChars, 10_000}},
	{"tool server result", Budget{UnitTokens, 25_000}},
	{"tool output cut in the middle", Budget{UnitTokens, 10_000}},
}

func TestDefaultBudget(t *testing.T) {
	got := DefaultBudget()
	if want := (Budget{UnitTokens, 9_000}); got != want {
		t.Fatalf("DefaultBudget() = %+v, want the smallest limit (10,000) less a tenth: %+v", got, want)
	}
	if err := got.validate(); err != nil {
		t.Fatal(err)
	}
	// The constants it is derived from agree with the visible limits.
	smallest := visibleLimits[0].budget.Limit
	for _, vl := range visibleLimits {
		smallest = min(smallest, vl.budget.Limit)
	}
	if SmallestVisibleLimit != smallest {
		t.Fatalf("SmallestVisibleLimit = %d, want %d", SmallestVisibleLimit, smallest)
	}
	if DefaultBudgetLimit != smallest-smallest/10 {
		t.Fatalf("DefaultBudgetLimit = %d, want %d", DefaultBudgetLimit, smallest-smallest/10)
	}
}

// A text that fits the default budget fits every path in that path's own unit.
func TestDefaultBudgetFitsEveryPath(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	def := DefaultBudget()
	for i := 0; i < 200; i++ {
		// No rune takes more than four bytes, so the estimate is within the limit.
		s := randomText(rng, 1+rng.Intn(def.Limit/4))
		if def.Measure(s) > def.Limit {
			t.Fatal("test text is over the default budget")
		}
		for _, vl := range visibleLimits {
			if size := vl.budget.Measure(s); size > vl.budget.Limit {
				t.Fatalf("text within the default budget measures %d on %s, limit %d", size, vl.name, vl.budget.Limit)
			}
		}
	}
}

func TestBudgetValidate(t *testing.T) {
	rows := []struct {
		rule string
		b    Budget
		ok   bool
	}{
		{"known unit and positive limit", Budget{UnitChars, 1}, true},
		{"zero unit is rejected", Budget{0, 10}, false},
		{"unknown unit is rejected", Budget{99, 10}, false},
		{"zero limit is rejected", Budget{UnitTokens, 0}, false},
		{"negative limit is rejected", Budget{UnitTokens, -5}, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			err := r.b.validate()
			if (err == nil) != r.ok {
				t.Fatalf("validate() = %v, want ok=%v", err, r.ok)
			}
			if _, perr := Pack(nil, testLease, r.b); (perr == nil) != r.ok {
				t.Fatalf("Pack error = %v, want ok=%v", perr, r.ok)
			}
		})
	}
}
