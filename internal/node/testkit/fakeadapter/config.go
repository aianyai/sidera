package fakeadapter

import (
	"errors"
	"log/slog"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// RuntimeLauncher starts or finds the native sessions the adapter drives. A
// fakeruntime.Runtime is one in the test process; a fakeruntime.ProcessLauncher
// starts a program per session.
type RuntimeLauncher = fakeruntime.Launcher

// Config configures Serve.
type Config struct {
	// InstanceID names the adapter instance in its ledger entries.
	InstanceID string
	// Now is the shared test clock. The adapter never reads another.
	Now func() time.Time
	// Ledger is the writer of the adapter's own entries ("adapter/<instance>").
	Ledger *ledger.Writer
	// Runtime is the native runtime the adapter drives.
	Runtime RuntimeLauncher
	// Manifest says what the adapter declares.
	Manifest ManifestOptions
	// Knobs are the test's handles on the adapter; nil gives knobs that change
	// nothing and that nobody holds.
	Knobs *Knobs
	// Peer sets the limits of the protocol connection; the zero value takes the
	// peer's defaults.
	Peer peer.Params
	// Logger receives protocol violations and the end of the connection; it never
	// receives message bodies. Nil discards everything.
	Logger *slog.Logger
}

func (c Config) validate() error {
	switch {
	case c.InstanceID == "":
		return errors.New("fakeadapter: no instance id")
	case c.Now == nil:
		return errors.New("fakeadapter: the adapter needs the test clock")
	case c.Ledger == nil:
		return errors.New("fakeadapter: the adapter needs a ledger writer")
	case c.Runtime == nil && c.Manifest.Runtime:
		return errors.New("fakeadapter: an adapter that declares a runtime needs a native runtime to drive")
	}
	return c.Manifest.Validate()
}
