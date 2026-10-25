package scenario

import (
	"context"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// Env is what a step may use of the system under test. Both fixtures implement
// it; what only one layer has (the engine layer's record query and file system,
// the binary layer's process control) is on that fixture and not here, so a
// script that uses only Env runs in both layers.
type Env interface {
	// Ctx is the context of the test: a step passes it to the calls it makes.
	Ctx() context.Context

	// Agent returns the fake agent of an endpoint set up under name, and Addr its
	// address. Direct sets up a direct endpoint under name; a script's Setup does
	// it before the first step.
	Agent(name string) fakeagent.Agent
	Addr(name string) string
	Direct(name string) (addr string, err error)

	// Advance moves the test clock forward by d and Set sets it, backwards too.
	// Both leave the system reacting; Quiesce waits for it to stop.
	Advance(d time.Duration)
	Set(t time.Time)
	// Quiesce returns when the system has stopped reacting at the time of the
	// clock now, and fails with a report of what had not stopped when its limit
	// ends. The driver calls it after every step; a step never sleeps.
	Quiesce(opts ...QuiesceOption) error

	// BeginStep announces the step that is about to run: the ledger entries
	// written from now on carry its name.
	BeginStep(name string) error

	// Adapter returns the knobs of the fake adapter of an instance and Runtime the
	// turns of the fake runtime. A layer that cannot provide one yet returns an
	// error that is ErrNotAvailable, never a stand-in.
	Adapter(instance string) (fakeadapter.Controller, error)
	Runtime() (fakeruntime.Controller, error)

	// Ledger reads what the fakes and the agents wrote, validated.
	Ledger() (*ledger.Log, error)
}

// ErrNotAvailable is what an Env method returns for a capability its layer does
// not have yet. It is the error the fake agent's backends carry for a method
// their layer lacks (fakeagent.ErrNotAvailable), so that one test works for both.
var ErrNotAvailable = fakeagent.ErrNotAvailable

// NotAvailable returns an error that is ErrNotAvailable and says what is missing.
func NotAvailable(what string) error {
	return fmt.Errorf("%w: %s", ErrNotAvailable, what)
}

// QuiesceDefaultTimeout is the longest Quiesce waits, on the real clock, when the
// caller gives no other limit.
const QuiesceDefaultTimeout = 60 * time.Second

// QuiesceOptions are what a caller of Env.Quiesce can say.
type QuiesceOptions struct {
	// Timeout is the real-time limit. Zero means QuiesceDefaultTimeout.
	Timeout time.Duration
}

// QuiesceOption sets one of the QuiesceOptions.
type QuiesceOption func(*QuiesceOptions)

// WithTimeout limits the wait to d on the real clock; zero keeps the default.
func WithTimeout(d time.Duration) QuiesceOption {
	return func(o *QuiesceOptions) { o.Timeout = d }
}

// ResolveQuiesce applies the options over the defaults. A fixture's Quiesce reads
// its options through it, so the two layers agree on what they mean.
func ResolveQuiesce(opts []QuiesceOption) QuiesceOptions {
	var o QuiesceOptions
	for _, f := range opts {
		f(&o)
	}
	if o.Timeout <= 0 {
		o.Timeout = QuiesceDefaultTimeout
	}
	return o
}
