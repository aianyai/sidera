package adapterhost

import (
	"context"
	"log/slog"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Config configures New.
type Config struct {
	Launcher Launcher
	Params   Params
	Clock    clock.Clock
	Log      *slog.Logger
}

// Host supervises the adapter instances of the node. Nothing of it is written yet:
// every method fails, so that the tests that describe it fail first.
type Host struct{}

func New(Config) (*Host, error) { panic("adapterhost: not implemented") }

func LockPath(home, instance string) string { panic("adapterhost: not implemented") }

func (*Host) Start(context.Context, Env) error { panic("adapterhost: not implemented") }
func (*Host) Shutdown(context.Context) error   { panic("adapterhost: not implemented") }
func (*Host) Abort()                           { panic("adapterhost: not implemented") }
func (*Host) EnsureRunning(string)             { panic("adapterhost: not implemented") }
func (*Host) Stop(string)                      { panic("adapterhost: not implemented") }
func (*Host) Counts(string) ConnCounts         { panic("adapterhost: not implemented") }
func (*Host) NotifyPending(string, adapterproto.PendingParams) {
	panic("adapterhost: not implemented")
}
func (*Host) CreateSession(context.Context, string, adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	panic("adapterhost: not implemented")
}
func (*Host) DeliverPrompt(context.Context, string, adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	panic("adapterhost: not implemented")
}
func (*Host) Reconcile(context.Context, string, adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	panic("adapterhost: not implemented")
}
func (*Host) Interrupt(context.Context, string, adapterproto.InterruptParams) (adapterproto.InterruptResult, error) {
	panic("adapterhost: not implemented")
}
func (*Host) Close(context.Context, string, adapterproto.CloseParams) (adapterproto.CloseResult, error) {
	panic("adapterhost: not implemented")
}

// ExecLauncher starts an adapter as a child process of the daemon.
type ExecLauncher struct{ Env []string }

func (ExecLauncher) Launch(context.Context, Instance) (Process, error) {
	panic("adapterhost: not implemented")
}

func (p Params) withDefaults() Params { panic("adapterhost: not implemented") }
func (p Params) validate() error      { panic("adapterhost: not implemented") }
