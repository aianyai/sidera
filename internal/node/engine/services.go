package engine

import (
	"context"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/node/store"
)

// AgentService is what the Agent interface asks of the engine: one method for
// each thing an agent can do, each taking the caller it is done for, which is
// resolved in the command that does it. The interface packages depend on this
// and not on Engine. The methods are added as the engine learns to do them.
type AgentService interface {
	Declare(ctx context.Context, c Caller, p DeclarationPatch) (authz.Declaration, error)
}

// ManageService is what the management interface asks of the engine. The methods
// are added as the engine learns to do them.
//
// Everything that changes state comes through Execute, the one door of the
// administrative rules (manage.go); the others only read.
type ManageService interface {
	Endpoints(ctx context.Context) ([]EndpointView, error)
	Counts(ctx context.Context) (Counts, error)
	Alerts(ctx context.Context) ([]store.AlertRecord, error)
	Execute(ctx context.Context, c ManagementCommand) (StoredResult, error)
}

var (
	_ AgentService  = (*Engine)(nil)
	_ ManageService = (*Engine)(nil)
)
