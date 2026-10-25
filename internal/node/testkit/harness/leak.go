package harness

import (
	"bytes"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
)

// ownFrames mark the goroutines the fixture is responsible for: the engine's,
// the transaction layer's, the store's and its own.
var ownFrames = []string{
	"internal/node/engine.",
	"internal/node/txn.",
	"internal/node/store.",
	"internal/node/testkit/harness.",
}

// leakWait is how long a goroutine that is on its way out is given.
const leakWait = 5 * time.Second

// NoLeaks fails the test, when it and its cleanups are over, if a goroutine of
// the engine, the transaction layer, the store or the fixture that was not
// running when NoLeaks was called is still running. Register it first in a test,
// so that it is the last cleanup to run.
func NoLeaks(t testing.TB) {
	t.Helper()
	checkNoLeaks(t, ownFrames, leakWait)
}

// checkNoLeaks is NoLeaks for the goroutines whose stacks hold one of the
// markers. Goroutines running now are not the test's and are never reported.
func checkNoLeaks(t testing.TB, markers []string, wait time.Duration) {
	t.Helper()
	before := map[int]bool{}
	for id := range marked(markers) {
		before[id] = true
	}
	t.Cleanup(func() {
		sys := clock.System()
		deadline := sys.Now().Add(wait)
		for {
			var left []string
			for id, stack := range marked(markers) {
				if !before[id] {
					left = append(left, stack)
				}
			}
			if len(left) == 0 {
				return
			}
			if sys.Now().After(deadline) {
				t.Errorf("%d goroutines of the fixture's packages still running:\n%s", len(left), strings.Join(left, "\n\n"))
				return
			}
			timer := sys.NewTimer(10 * time.Millisecond)
			<-timer.C()
		}
	})
}

// marked returns the stacks of the live goroutines with a marker in them, by
// goroutine number.
func marked(markers []string) map[int]string {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	out := map[int]string{}
	for _, block := range strings.Split(buf.String(), "\n\n") {
		header, _, _ := strings.Cut(block, "\n")
		fields := strings.Fields(header)
		if len(fields) < 2 || fields[0] != "goroutine" {
			continue
		}
		id, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		for _, m := range markers {
			if strings.Contains(block, m) {
				out[id] = block
				break
			}
		}
	}
	return out
}
