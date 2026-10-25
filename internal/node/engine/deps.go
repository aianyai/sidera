package engine

import (
	"context"
	"io"
	"log/slog"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Store is what the engine asks of the durable records of state/: the scan that
// builds its view, the writes of the transactions it runs, and the writes of the
// recovery rules. *store.Store is the one implementation; the interface is the
// seam that keeps the engine from reaching the store's other methods before a
// stream needs them, and the list grows with the streams.
type Store interface {
	Scan() (*store.Scan, error)
	Available() (uint64, error)

	EndpointUpdate(ep store.EndpointRecord) error
	EndpointClose(ep store.EndpointRecord, result *store.AdminRecord) error
	EndpointDrain(b store.DrainBatch) error
	AlertRaise(a store.AlertRecord) error

	CtlEffectReplay(eps []store.EndpointRecord) error
	InstanceDrainReplay(closed []store.EndpointRecord, b store.DrainBatch) error
	OrphanGCEndpoint(eps []store.EndpointRecord) error
	EndpointDrainReplay(b store.DrainBatch) error
	OrphanGC(bodies []string) error
}

var _ Store = (*store.Store)(nil)

// Config is what the engine reads of the owner's configuration and the part of
// it that a transaction of the engine writes. *config.Config is the one
// implementation.
type Config interface {
	Node() config.Node
	Instances() config.Instances
	EndpointSettings(instance string, id ident.EndpointID) config.EndpointSettings
	DirectID(instance, key string, rnd io.Reader) (config.IDChange, error)
	PutEndpointIDs(tx *txn.Tx, ch config.IDChange) (config.AfterCommit, error)
}

var _ Config = (*config.Config)(nil)

// Beginner opens the transactions that write the files of more than one owner.
// *txn.Writer is the one implementation. A transaction of one owner is run by
// that owner's own method.
type Beginner interface {
	Begin(d *txn.Decl) *txn.Tx
}

var _ Beginner = (*txn.Writer)(nil)

// Adapters is what the engine asks of the adapter host. Every call runs outside
// the loop, in a goroutine the engine owns, and its result comes back as a
// command. A call to an adapter that is not connected fails at once with the
// host's not-connected error.
type Adapters interface {
	// EnsureRunning starts the instance if it is enabled and not running;
	// Stop stops it. Both are idempotent.
	EnsureRunning(instance string)
	Stop(instance string)

	CreateSession(ctx context.Context, instance string, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error)
	DeliverPrompt(ctx context.Context, instance string, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error)
	Reconcile(ctx context.Context, instance string, p adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error)
	Interrupt(ctx context.Context, instance string, p adapterproto.InterruptParams) (adapterproto.InterruptResult, error)
	Close(ctx context.Context, instance string, p adapterproto.CloseParams) (adapterproto.CloseResult, error)
	// NotifyPending tells a direct endpoint's adapter that messages wait; it is
	// best effort and does not block.
	NotifyPending(instance string, p adapterproto.PendingParams)
}

// Deps is what the composition root hands the engine. Every field except
// Adapters, Log and RollbackPaused is required.
type Deps struct {
	// Node is this node's identifier: the node part of the address of every
	// endpoint of the node, the system endpoint included.
	Node ident.NodeID
	// Clock is the injected time source. The engine reads the process clock
	// through clock.System() only for the waits that are about real time.
	Clock clock.Clock
	// HighWater is the high water mark of the wall clock, built from the
	// persisted value. The engine takes the value over: the loop observes it once
	// per command and is its only user.
	HighWater clock.HighWater
	// RollbackPaused is true when the persisted state says the wall clock was
	// set back and the owner has not cleared it.
	RollbackPaused bool
	Writer         Beginner
	Store          Store
	Config         Config
	// Rand is the randomness of tokens and alert identifiers; the composition
	// root passes crypto/rand's reader, a test a seeded one. Only the loop reads
	// it.
	Rand io.Reader
	// Adapters is the adapter host; nil before there is one.
	Adapters Adapters
	// Log receives diagnostics, never a message body, a token or a key; nil
	// discards them.
	Log *slog.Logger
}

var _ = adapterreg.KindDirect
