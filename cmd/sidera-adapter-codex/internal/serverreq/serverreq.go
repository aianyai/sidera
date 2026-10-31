// Package serverreq decides how the adapter answers the requests the App Server
// sends it. Nobody is there to ask, so every answer comes from the preset and is
// given at once; a request the adapter does not know is refused. The decision is a
// pure function of the method and of whether the preset approves.
package serverreq

import (
	"encoding/json"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Method names of the requests the server sends.
const (
	MethodCommandApproval    = "item/commandExecution/requestApproval"
	MethodFileChangeApproval = "item/fileChange/requestApproval"
	MethodPermissionsRequest = "item/permissions/requestApproval"
	MethodElicitation        = "mcpServer/elicitation/request"
	MethodUserInput          = "item/tool/requestUserInput"
	MethodDynamicToolCall    = "item/tool/call"
	MethodAuthTokensRefresh  = "account/chatgptAuthTokens/refresh"
	MethodAttestation        = "attestation/generate"
	MethodApplyPatchLegacy   = "applyPatchApproval"
	MethodExecCommandLegacy  = "execCommandApproval"
)

// Known lists the methods the adapter answers; a method outside it is unknown.
// The schema guard test holds the list equal to the server's own.
func Known() []string {
	return []string{
		MethodCommandApproval, MethodFileChangeApproval, MethodPermissionsRequest, MethodElicitation,
		MethodUserInput, MethodDynamicToolCall, MethodAuthTokensRefresh, MethodAttestation,
		MethodApplyPatchLegacy, MethodExecCommandLegacy,
	}
}

// JSON-RPC codes of an error answer.
const (
	// CodeRefused answers a request the adapter has no one to put to.
	CodeRefused = -32000
	// CodeMethodNotFound answers a method the adapter does not know.
	CodeMethodNotFound = -32601
)

// Error is an error answer.
type Error struct {
	Code    int
	Message string
}

// Result is the answer to one request: either Reply, or Error, and how it is
// reported to the daemon.
type Result struct {
	Reply       any
	Error       *Error
	Disposition adapterproto.Disposition
}

// Handle decides the answer to a request of the server. approves is whether the
// preset says yes to approvals.
func Handle(method string, approves bool) Result {
	switch method {
	case MethodCommandApproval, MethodFileChangeApproval:
		if approves {
			return Result{Reply: map[string]any{"decision": "accept"}, Disposition: adapterproto.DispositionPreset}
		}
		return Result{Reply: map[string]any{"decision": "decline"}, Disposition: adapterproto.DispositionRejectedByPreset}
	case MethodApplyPatchLegacy, MethodExecCommandLegacy:
		if approves {
			return Result{Reply: map[string]any{"decision": "approved"}, Disposition: adapterproto.DispositionPreset}
		}
		return Result{
			Reply:       map[string]any{"decision": map[string]any{"denied": map[string]any{"rejection": "the preset does not approve"}}},
			Disposition: adapterproto.DispositionRejectedByPreset,
		}
	case MethodPermissionsRequest:
		// No permission beyond the sandbox is ever granted.
		return Result{Reply: map[string]any{"permissions": map[string]any{}}, Disposition: adapterproto.DispositionRejectedByPreset}
	case MethodElicitation:
		// The server asks for content nobody can make up.
		return Result{Reply: map[string]any{"action": "decline"}, Disposition: adapterproto.DispositionRejectedByPreset}
	case MethodUserInput, MethodDynamicToolCall, MethodAuthTokensRefresh, MethodAttestation:
		return Result{Error: &Error{Code: CodeRefused, Message: "the adapter cannot answer this request"}, Disposition: adapterproto.DispositionRejectedByPreset}
	}
	return Result{Error: &Error{Code: CodeMethodNotFound, Message: "method not found"}, Disposition: adapterproto.DispositionRejectedUnknown}
}

// ThreadID names the thread a request belongs to: the threadId of the current
// requests, the conversationId of the two older ones. It is empty for a request
// that belongs to none.
func ThreadID(params json.RawMessage) string {
	var p struct {
		ThreadID       string `json:"threadId"`
		ConversationID string `json:"conversationId"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	if p.ThreadID != "" {
		return p.ThreadID
	}
	return p.ConversationID
}
