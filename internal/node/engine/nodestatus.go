package engine

import (
	"context"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	nodekeys "github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
)

// ReminderKind is what the owner is reminded of on every look at the status until
// it is done. A reminder is a fact derived from where its authority lives: it has
// no record of its own, so nothing can leave one standing after the fact has
// changed, and nothing can be acknowledged away. The zero value is none.
type ReminderKind uint8

const (
	// ReminderRevocationBackupUnconfirmed: the owner has not confirmed that a copy
	// of the self-revocation is kept outside this device, for the statement this
	// node holds. A confirmation of another statement does not count, so a node made
	// again over the same data directory reminds again.
	ReminderRevocationBackupUnconfirmed ReminderKind = iota + 1

	reminderEnd
)

var reminderNames = [reminderEnd]string{ReminderRevocationBackupUnconfirmed: "revocation_backup_unconfirmed"}

// String returns the name of the reminder.
func (k ReminderKind) String() string {
	if k > 0 && k < reminderEnd {
		return reminderNames[k]
	}
	return "invalid"
}

// ClockStatus is what the owner is shown of the wall clock.
type ClockStatus struct {
	// RollbackPaused: the clock was set back beyond the tolerance and the owner has
	// not cleared it.
	RollbackPaused bool
	// PausedSince is when the pause began, when that is known.
	PausedSince time.Time
	// HighWater is the highest reading of the clock the node trusts; zero before
	// the first.
	HighWater time.Time
}

// CertificateStatus is what the owner is shown of the network key's certificate.
// Both are zero for a node that holds no keys.
type CertificateStatus struct {
	NotAfter time.Time
	// Remaining is the validity left at the time of the status; negative once the
	// certificate has expired.
	Remaining time.Duration
}

// CapacityStatus is what the owner is shown of the room the node has.
type CapacityStatus struct {
	// OpenDeliveries is how many deliveries the node holds that still have work
	// behind them: the number the queue limit bounds.
	OpenDeliveries int
	// NodeQueueLimit is the limit.
	NodeQueueLimit int
	// OutboundBacklog is how many deliveries of messages this node sent no receipt
	// has answered yet.
	OutboundBacklog int
	// DiskFreeBytes is the free space of the volume, and DiskReserveBytes the free
	// space below which new work is refused.
	DiskFreeBytes    uint64
	DiskReserveBytes int64
}

// EndpointStatus is one endpoint in the status.
type EndpointStatus struct {
	Address  ident.Address
	Kind     adapterreg.EndpointKind
	Instance string
	Status   store.EndpointStatus
}

// NodeStatus is what the owner is shown of the node: derived from the loop's
// state at one command, so its parts agree with one another. The figures that need
// a feature the node does not have yet (the endpoints that are blocked, the turns
// started by prompts) are the features' to add.
type NodeStatus struct {
	Node ident.NodeID
	// Phase is where the engine is: recovering while the node starts, running when
	// it serves.
	Phase Phase
	// StartedAt is when the loop was started, on the injected clock.
	StartedAt time.Time
	// Reminders come first for the owner and stay until done.
	Reminders []ReminderKind
	// AlertsOpen is how many alerts the owner has not acknowledged.
	AlertsOpen  int
	Clock       ClockStatus
	Certificate CertificateStatus
	Capacity    CapacityStatus
	// Deliveries counts the deliveries the node holds as a target, the control
	// signals among them, by the state each is in. A state with none is absent.
	Deliveries map[delivery.State]int
	// Endpoints are the node's endpoints, closed ones included, in the order of
	// their identifiers.
	Endpoints []EndpointStatus
}

type cmdNodeStatus struct{ call[NodeStatus] }

func (c *cmdNodeStatus) gate() gate { return gateRead }
func (c *cmdNodeStatus) run(e *Engine, now time.Time) {
	st, err := e.nodeStatus(now)
	c.done(st, e.classify(err))
}

// Status is what the owner is shown of the node. It reads the loop's state and
// the free space of the volume, and writes nothing.
func (e *Engine) Status(ctx context.Context) (NodeStatus, error) {
	c := &cmdNodeStatus{call: newCall[NodeStatus]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

func (e *Engine) nodeStatus(now time.Time) (NodeStatus, error) {
	st := NodeStatus{
		Node: e.d.Node, Phase: e.Phase(), StartedAt: e.startedAt,
		Clock:      ClockStatus{RollbackPaused: e.paused, PausedSince: e.pausedSince, HighWater: e.hw.Value()},
		Deliveries: map[delivery.State]int{},
	}
	if k := e.d.Keys; k != nil {
		notAfter := k.Certificate().Payload().NotAfter
		st.Certificate = CertificateStatus{NotAfter: notAfter, Remaining: notAfter.Sub(now)}
		if !e.backupConfirmed(k) {
			st.Reminders = append(st.Reminders, ReminderRevocationBackupUnconfirmed)
		}
	}
	for _, a := range e.view.alerts {
		if a.Acked == nil {
			st.AlertsOpen++
		}
	}
	free, err := e.d.Store.Available()
	if err != nil {
		return NodeStatus{}, err
	}
	settings := e.d.Config.Node().Settings
	st.Capacity = CapacityStatus{
		OpenDeliveries: e.view.Unfinished(), NodeQueueLimit: settings.NodeQueueLimit,
		DiskFreeBytes: free, DiskReserveBytes: settings.DiskReserveBytes,
	}
	for _, o := range e.view.out {
		st.Capacity.OutboundBacklog += outUnfinished(o)
	}
	for _, in := range e.view.in {
		st.Deliveries[in.Record.State]++
	}
	for _, c := range e.view.ctl {
		st.Deliveries[c.Record.State]++
	}
	for _, ep := range e.endpointViews() {
		st.Endpoints = append(st.Endpoints, EndpointStatus{Address: ep.Address, Kind: ep.Kind, Instance: ep.Instance, Status: ep.Status})
	}
	return st, nil
}

type cmdNodeSettings struct{ call[config.Node] }

func (c *cmdNodeSettings) gate() gate                 { return gateRead }
func (c *cmdNodeSettings) run(e *Engine, _ time.Time) { c.done(e.d.Config.Node(), nil) }

// NodeSettings is node.json as the loop sees it: the settings, the version a
// change of them has to name, and the operation that made the version. A read of
// the loop, so it is the same configuration the next command uses.
func (e *Engine) NodeSettings(ctx context.Context) (config.Node, error) {
	c := &cmdNodeSettings{call: newCall[config.Node]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

// backupConfirmed says whether the owner has confirmed the backup of the
// statement this node holds.
func (e *Engine) backupConfirmed(k Keys) bool {
	b, ok := e.d.Config.Backup()
	return ok && b.Confirms(k.SelfRevocation().Digest())
}

// RevocationExport is the self-revocation of the node in the form the owner keeps
// it in: the armored text and the digest that names it.
type RevocationExport struct {
	Armor  []byte
	Digest record.Digest
}

// ExportRevocation is the self-revocation the keys verified as this node's, in
// the one form of export the keys package makes. It is the one thing of the
// management interface that is neither a read of the loop's state nor an operation
// that is stored: it reads the statement, which does not change while the node
// runs, and it is not stored, because the statement can retire the node and its
// result would be a copy of it under state/. It does not run in the loop, so the
// owner can have it while the loop is busy, and it needs only that the node holds
// keys.
func (e *Engine) ExportRevocation(ctx context.Context) (RevocationExport, error) {
	if err := ctx.Err(); err != nil {
		return RevocationExport{}, err
	}
	if e.d.Keys == nil {
		return RevocationExport{}, errNoKeys
	}
	armor, digest := nodekeys.FormatRevocationExport(e.d.Keys.SelfRevocation())
	return RevocationExport{Armor: armor, Digest: digest}, nil
}
