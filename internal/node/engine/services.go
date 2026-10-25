package engine

import (
	"context"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// AgentService is what the Agent interface asks of the engine: one method for
// each thing an agent can do, each taking the caller it is done for, which is
// resolved in the command that does it. The interface packages depend on this
// and not on Engine. The methods are added as the engine learns to do them.
type AgentService interface {
	Declare(ctx context.Context, c Caller, p DeclarationPatch) (authz.Declaration, error)
	Send(ctx context.Context, c Caller, r SendRequest) (SendResult, error)
	Reply(ctx context.Context, c Caller, r ReplyRequest) (SendResult, error)
	MessageStatus(ctx context.Context, c Caller, id adapterproto.MessageID) (adapterproto.MessageStatusResult, error)
}

// ManageService is what the management interface asks of the engine. The methods
// are added as the engine learns to do them.
//
// Everything that changes state comes through Execute, the one door of the
// administrative rules (manage.go); the others only read. ExportRevocation reads
// the statement that retires the node, and is the one read that is not judged as a
// read: the interface authorizes it as an operation of the owner's alone, and it
// stores nothing.
type ManageService interface {
	Endpoints(ctx context.Context) ([]EndpointView, error)
	Counts(ctx context.Context) (Counts, error)
	Alerts(ctx context.Context) ([]store.AlertRecord, error)
	Status(ctx context.Context) (NodeStatus, error)
	ExportRevocation(ctx context.Context) (RevocationExport, error)
	Execute(ctx context.Context, c ManagementCommand) (StoredResult, error)
	OperationResult(ctx context.Context, key admin.RequestKey) ([]byte, bool, error)
}

var (
	_ AgentService  = (*Engine)(nil)
	_ ManageService = (*Engine)(nil)
)
