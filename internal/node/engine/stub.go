package engine

// Stub of the engine: the shapes the named tests are written against. Every
// operation fails until the implementation replaces this file.

import (
	"context"
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var errStub = errors.New("engine: not implemented")

// Phase is where the engine is in its life.
type Phase uint8

const (
	PhaseOpened Phase = iota + 1
	PhaseRecovering
	PhaseRunning
	PhaseStopping
	PhaseStopped
	PhaseFailed
)

// ScanReport says what the scan entry did.
type ScanReport struct {
	Passes     int
	Rules      map[string]int
	Endpoints  int
	Unfinished int
}

// Engine is the single writer.
type Engine struct{}

// Open is the scan entry.
func Open(ctx context.Context, d Deps, p Params) (*Engine, ScanReport, error) {
	return nil, ScanReport{}, errStub
}

func (e *Engine) Start()                                    {}
func (e *Engine) Serve()                                    {}
func (e *Engine) BeginShutdown()                            {}
func (e *Engine) Stop(ctx context.Context) error            { return errStub }
func (e *Engine) Done() <-chan struct{}                     { return nil }
func (e *Engine) Err() error                                { return errStub }
func (e *Engine) Phase() Phase                              { return 0 }
func (e *Engine) RecoverDelivery(ctx context.Context) error { return errStub }
func (e *Engine) SystemAddress() ident.Address              { return ident.Address{} }

// TaskInput is what a periodic task is told.
type TaskInput struct {
	Now            time.Time
	HighWater      time.Time
	RollbackPaused bool
}

// Task is a periodic task.
type Task func(in TaskInput) error

func (e *Engine) AddPeriodic(name string, every time.Duration, t Task) error { return errStub }

// Expect and Report are the shapes of Quiesce.
type Expect struct{}
type Report struct {
	InboxLen   int
	DueEntries int
	Workers    int
	Satisfied  bool
}

func (e *Engine) Quiesce(ctx context.Context, x Expect) (Report, error) { return Report{}, errStub }

// MaxAcceptFrom bounds the senders an endpoint can list.
const MaxAcceptFrom = 256

// DeclarationPatch is a change of a declaration.
type DeclarationPatch struct {
	Paused               *bool
	RefuseMultiRecipient *bool
	RestrictSenders      *bool
	HiddenFromDirectory  *bool
	AcceptFrom           *[]ident.Address
}

func (p DeclarationPatch) IsEmpty() bool { return false }

func (e *Engine) Declare(ctx context.Context, c Caller, p DeclarationPatch) (authz.Declaration, error) {
	return authz.Declaration{}, errStub
}

func (e *Engine) AdapterAdmitted(ctx context.Context, instance string, a adapterreg.Admission) error {
	return errStub
}

func (e *Engine) RegisterDirect(ctx context.Context, instance string, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	return adapterproto.RegisterResult{}, errStub
}

func (e *Engine) UnregisterDirect(ctx context.Context, instance string, p adapterproto.UnregisterParams) error {
	return errStub
}

// EndpointView is one endpoint as the owner sees it.
type EndpointView struct {
	ID          ident.EndpointID
	Address     ident.Address
	Kind        adapterreg.EndpointKind
	Instance    string
	Status      store.EndpointStatus
	DisplayName string
	Description string
	Declaration authz.Declaration
	Draining    bool
}

func (e *Engine) Endpoints(ctx context.Context) ([]EndpointView, error) { return nil, errStub }

// CloseOutcome says what a close did.
type CloseOutcome uint8

const (
	CloseOutcomeClosed CloseOutcome = iota + 1
	CloseOutcomeClosing
	CloseOutcomeAlreadyClosed
)

func (o CloseOutcome) String() string { return "" }

// CloseRequest asks to close an endpoint at the owner's request.
type CloseRequest struct {
	Endpoint ident.EndpointID
	Result   *store.AdminRecord
}

// CloseResult is the answer.
type CloseResult struct{ Outcome CloseOutcome }

func (e *Engine) CloseEndpoint(ctx context.Context, r CloseRequest) (CloseResult, error) {
	return CloseResult{}, errStub
}

// Counts are the numbers the owner's status shows.
type Counts struct {
	Endpoints  int
	Unfinished int
	Alerts     int
}

func (e *Engine) Counts(ctx context.Context) (Counts, error) { return Counts{}, errStub }

func (e *Engine) Alerts(ctx context.Context) ([]store.AlertRecord, error) { return nil, errStub }

func (e *Engine) RaiseAlert(ctx context.Context, kind, subject, detail string) (store.AlertID, error) {
	return store.AlertID{}, errStub
}

// CapacityReason says why the capacity check refused.
type CapacityReason uint8

const (
	CapacityOK CapacityReason = iota
	CapacityClockRollback
	CapacityNodeQueueFull
	CapacityStorageFull
)

func (r CapacityReason) String() string { return "" }

// Reason is the engine reason of a capacity refusal.
func (r CapacityReason) Reason() Reason { return 0 }

// CapacityInput is everything the capacity check reads.
type CapacityInput struct {
	RollbackPaused bool
	Unfinished     int
	Recipients     int
	NodeQueueLimit int
	Available      uint64
	DiskReserve    int64
}

// CapacityVerdict is the answer of the capacity check.
type CapacityVerdict struct {
	Admit  bool
	Reason CapacityReason
}

// CheckCapacity is the one capacity check.
func CheckCapacity(in CapacityInput) CapacityVerdict { return CapacityVerdict{} }

// RecoveryPlan is the work of the recovery rules of the scan entry.
type RecoveryPlan struct{}

func (p RecoveryPlan) Empty() bool     { return false }
func (p RecoveryPlan) Items() []string { return nil }

// PlanRecovery is the pure part of the scan entry.
func PlanRecovery(sc *store.Scan, now time.Time, enabled func(instance string) bool) RecoveryPlan {
	return RecoveryPlan{}
}

// PendingRecoveryError is the refusal of the scan entry to open over work of a
// rule that no stream has wired yet.
type PendingRecoveryError struct {
	Rule  string
	Items int
}

func (e *PendingRecoveryError) Error() string { return "engine: " + e.Rule + ": not wired" }

// The unexported pieces the internal tests are written against.

type endpointMeta struct {
	DisplayName string
	Description string
	Declaration authz.Declaration
}

func encodeMeta(m endpointMeta) ([]byte, error) { return nil, errStub }
func decodeMeta(b []byte) (endpointMeta, error) { return endpointMeta{}, errStub }

type tokenIndex struct{}

func newTokenIndex() *tokenIndex                            { return &tokenIndex{} }
func (x *tokenIndex) add(id ident.EndpointID, token []byte) {}
func (x *tokenIndex) remove(id ident.EndpointID)            {}
func (x *tokenIndex) lookup(t adapterproto.Token) (ident.EndpointID, bool) {
	return ident.EndpointID{}, false
}

type endpointState struct {
	rec  store.EndpointRecord
	meta endpointMeta
}

type view struct{}

func buildView(node ident.NodeID, sc *store.Scan) (*view, error) { return &view{}, errStub }
func (v *view) resolve(c Caller) (*endpointState, error)         { return nil, errStub }
func (v *view) Unfinished() int                                  { return -1 }
func (v *view) BodyRefs(name string) int                         { return -1 }
func (v *view) OpenEndpoints() int                               { return -1 }
func (v *view) Draining(id ident.EndpointID) bool                { return false }
