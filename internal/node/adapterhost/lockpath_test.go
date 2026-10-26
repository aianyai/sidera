package adapterhost_test

import (
	"context"
	"os"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// What the engine will ask of the host is what the host is: the engine defines the
// interface, and a drift of a name or a signature would be found only when the
// daemon wires them. The engine package cannot be imported here, so the interface
// is written out.
var _ interface {
	EnsureRunning(instance string)
	Stop(instance string)
	Stopping(instance string) bool
	CreateSession(ctx context.Context, instance string, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error)
	DeliverPrompt(ctx context.Context, instance string, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error)
	Reconcile(ctx context.Context, instance string, p adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error)
	Interrupt(ctx context.Context, instance string, p adapterproto.InterruptParams) (adapterproto.InterruptResult, error)
	Close(ctx context.Context, instance string, p adapterproto.CloseParams) (adapterproto.CloseResult, error)
	NotifyPending(instance string, p adapterproto.PendingParams)
	Counts(instance string) adapterhost.ConnCounts
} = (*adapterhost.Host)(nil)

// The lock file of an instance is adapter-<instance>.lock in the data directory.
func TestLockPath(t *testing.T) {
	sep := string(os.PathSeparator)
	for _, tc := range []struct {
		name, home, instance, want string
	}{
		{"an instance in a data directory", sep + "data" + sep + "sidera", "chat", sep + "data" + sep + "sidera" + sep + "adapter-chat.lock"},
		{"the separator at the end of the data directory is not doubled", sep + "data" + sep, "chat", sep + "data" + sep + "adapter-chat.lock"},
		{"an instance name with digits and hyphens", sep + "h", "chat-2", sep + "h" + sep + "adapter-chat-2.lock"},
		{"the lock is of the instance, not of the adapter", sep + "h", "fake", sep + "h" + sep + "adapter-fake.lock"},
	} {
		if got := adapterhost.LockPath(tc.home, tc.instance); got != tc.want {
			t.Errorf("%s: LockPath(%q, %q) = %q, want %q", tc.name, tc.home, tc.instance, got, tc.want)
		}
	}
}
