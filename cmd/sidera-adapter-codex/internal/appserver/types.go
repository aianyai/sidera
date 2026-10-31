package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// The types below are the subset of the App Server protocol the adapter uses,
// written by hand: the full schema is large and moves with every release, and a
// generated copy would carry all of that. Decoding ignores members it does not
// know, so a release that adds one does not break the adapter; the members it
// depends on are checked by the schema guard test against the installed Codex.

// Method names the adapter calls.
const (
	MethodInitialize        = "initialize"
	MethodInitialized       = "initialized"
	MethodAccountRead       = "account/read"
	MethodThreadStart       = "thread/start"
	MethodThreadResume      = "thread/resume"
	MethodThreadSetName     = "thread/name/set"
	MethodThreadArchive     = "thread/archive"
	MethodThreadUnsubscribe = "thread/unsubscribe"
	MethodThreadTurnsList   = "thread/turns/list"
	MethodThreadShell       = "thread/shellCommand"
	MethodTurnStart         = "turn/start"
	MethodCommandExec       = "command/exec"
)

// Notification methods the adapter handles.
const (
	NotifyTurnStarted   = "turn/started"
	NotifyTurnCompleted = "turn/completed"
	NotifyItemCompleted = "item/completed"
	NotifyThreadStatus  = "thread/status/changed"
	NotifyMCPStatus     = "mcpServer/startupStatus/updated"
)

// Turn statuses.
const (
	TurnInProgress  = "inProgress"
	TurnCompleted   = "completed"
	TurnInterrupted = "interrupted"
	TurnFailed      = "failed"
)

// Thread status types.
const (
	ThreadNotLoaded = "notLoaded"
	ThreadIdle      = "idle"
	ThreadActive    = "active"
	ThreadSysError  = "systemError"
)

// MCP server startup states.
const (
	MCPStarting = "starting"
	MCPReady    = "ready"
	MCPFailed   = "failed"
)

// ItemUserMessage is the type of the item that records a prompt.
const ItemUserMessage = "userMessage"

// QuietNotifications are the notification methods the adapter has no use for and
// opts out of when it initializes: streamed output and reasoning are the bulk of
// the traffic and nothing here reads them.
var QuietNotifications = []string{
	"item/started",
	"item/agentMessage/delta",
	"item/plan/delta",
	"item/commandExecution/outputDelta",
	"item/commandExecution/terminalInteraction",
	"item/fileChange/outputDelta",
	"item/fileChange/patchUpdated",
	"item/mcpToolCall/progress",
	"item/reasoning/summaryTextDelta",
	"item/reasoning/summaryPartAdded",
	"item/reasoning/textDelta",
	"command/exec/outputDelta",
	"process/outputDelta",
	"turn/diff/updated",
	"turn/plan/updated",
	"thread/tokenUsage/updated",
	"mcpServer/event/stream/notification",
	"thread/realtime/started",
	"thread/realtime/itemAdded",
	"thread/realtime/item/started",
	"thread/realtime/item/transcript/delta",
	"thread/realtime/item/completed",
	"thread/realtime/transcript/delta",
	"thread/realtime/transcript/done",
	"thread/realtime/outputAudio/delta",
	"thread/realtime/sdp",
	"thread/realtime/error",
	"thread/realtime/closed",
}

// ClientInfo names the client to the server.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams is the first request of a connection.
type InitializeParams struct {
	ClientInfo   ClientInfo              `json:"clientInfo"`
	Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
}

// InitializeCapabilities is what the client asks of the connection.
type InitializeCapabilities struct {
	OptOutNotificationMethods []string `json:"optOutNotificationMethods,omitempty"`
}

// InitializeResult identifies the server.
type InitializeResult struct {
	UserAgent string `json:"userAgent"`
	CodexHome string `json:"codexHome"`
}

// Initialize opens the connection: the request, then the notification that
// completes it.
func (c *Client) Initialize(ctx context.Context, p InitializeParams) (InitializeResult, error) {
	var r InitializeResult
	if err := c.Call(ctx, MethodInitialize, p, &r); err != nil {
		return r, err
	}
	return r, c.Notify(ctx, MethodInitialized, nil)
}

// Account is the signed-in account; only its kind is read.
type Account struct {
	Type string `json:"type"`
}

// AccountResult is the answer of account/read.
type AccountResult struct {
	Account            *Account `json:"account"`
	RequiresOpenaiAuth bool     `json:"requiresOpenaiAuth"`
}

// SignedIn reports whether the server has an account, or needs none.
func (r AccountResult) SignedIn() bool { return r.Account != nil || !r.RequiresOpenaiAuth }

// AccountRead asks which account the server uses.
func (c *Client) AccountRead(ctx context.Context) (AccountResult, error) {
	var r AccountResult
	return r, c.Call(ctx, MethodAccountRead, struct{}{}, &r)
}

// ThreadStatus is the state of a thread in the server.
type ThreadStatus struct {
	Type string `json:"type"`
}

// Active reports whether a turn is running.
func (s ThreadStatus) Active() bool { return s.Type == ThreadActive }

// Thread is a conversation. Only what the adapter reads is decoded.
type Thread struct {
	ID     string       `json:"id"`
	Name   *string      `json:"name"`
	Status ThreadStatus `json:"status"`
}

// ThreadStartParams starts a thread. Config is the thread-level configuration:
// the server does not keep it, so every resume sends it again.
type ThreadStartParams struct {
	CWD            string         `json:"cwd,omitempty"`
	ApprovalPolicy string         `json:"approvalPolicy,omitempty"`
	Sandbox        string         `json:"sandbox,omitempty"`
	Model          string         `json:"model,omitempty"`
	Ephemeral      bool           `json:"ephemeral"`
	Config         map[string]any `json:"config,omitempty"`
}

// ThreadResult is the answer of thread/start and thread/resume.
type ThreadResult struct {
	Thread Thread `json:"thread"`
}

// ThreadStart starts a thread and subscribes this connection to it.
func (c *Client) ThreadStart(ctx context.Context, p ThreadStartParams) (Thread, error) {
	var r ThreadResult
	err := c.Call(ctx, MethodThreadStart, p, &r)
	return r.Thread, err
}

// ThreadResumeParams loads a thread that was stored and subscribes this
// connection to it. ExcludeTurns keeps the answer small.
type ThreadResumeParams struct {
	ThreadID       string         `json:"threadId"`
	CWD            string         `json:"cwd,omitempty"`
	ApprovalPolicy string         `json:"approvalPolicy,omitempty"`
	Sandbox        string         `json:"sandbox,omitempty"`
	Model          string         `json:"model,omitempty"`
	ExcludeTurns   bool           `json:"excludeTurns"`
	Config         map[string]any `json:"config,omitempty"`
}

// ThreadResume loads a thread and subscribes this connection to it.
func (c *Client) ThreadResume(ctx context.Context, p ThreadResumeParams) (Thread, error) {
	var r ThreadResult
	err := c.Call(ctx, MethodThreadResume, p, &r)
	return r.Thread, err
}

// ThreadSetName names a thread; the first name also makes it persistent.
func (c *Client) ThreadSetName(ctx context.Context, threadID, name string) error {
	return c.Call(ctx, MethodThreadSetName, map[string]string{"threadId": threadID, "name": name}, nil)
}

// ThreadArchive archives a thread: it is unloaded and its record is kept.
func (c *Client) ThreadArchive(ctx context.Context, threadID string) error {
	return c.Call(ctx, MethodThreadArchive, map[string]string{"threadId": threadID}, nil)
}

// ThreadUnsubscribe drops this connection's subscription to a thread.
func (c *Client) ThreadUnsubscribe(ctx context.Context, threadID string) error {
	return c.Call(ctx, MethodThreadUnsubscribe, map[string]string{"threadId": threadID}, nil)
}

// Item is one entry of a turn. Only the members the adapter reads are decoded.
type Item struct {
	Type     string  `json:"type"`
	ID       string  `json:"id"`
	ClientID *string `json:"clientId"`
}

// Turn is one run of the agent on a prompt.
type Turn struct {
	ID     string     `json:"id"`
	Status string     `json:"status"`
	Items  []Item     `json:"items"`
	Error  *TurnError `json:"error"`
}

// TurnError is why a turn failed. The message is for the log only.
type TurnError struct {
	Message string `json:"message"`
}

// Running reports whether the turn has not ended.
func (t Turn) Running() bool { return t.Status == TurnInProgress }

// UserMessageIDs returns the client ids of the prompts the turn records.
func (t Turn) UserMessageIDs() []string {
	var ids []string
	for _, it := range t.Items {
		if it.Type == ItemUserMessage && it.ClientID != nil {
			ids = append(ids, *it.ClientID)
		}
	}
	return ids
}

// ThreadTurnsListParams reads the turns of a thread, newest first. ItemsView
// "full" includes the items, which is where the prompts are.
type ThreadTurnsListParams struct {
	ThreadID      string `json:"threadId"`
	Cursor        string `json:"cursor,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	ItemsView     string `json:"itemsView,omitempty"`
	SortDirection string `json:"sortDirection,omitempty"`
}

// ThreadTurnsListResult is one page of turns.
type ThreadTurnsListResult struct {
	Data       []Turn  `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

// ThreadTurnsList reads one page of turns.
func (c *Client) ThreadTurnsList(ctx context.Context, p ThreadTurnsListParams) (ThreadTurnsListResult, error) {
	var r ThreadTurnsListResult
	return r, c.Call(ctx, MethodThreadTurnsList, p, &r)
}

// UserInput is one piece of a prompt.
type UserInput struct {
	Type         string `json:"type"`
	Text         string `json:"text"`
	TextElements []any  `json:"text_elements"`
}

// TextInput is a prompt of plain text.
func TextInput(text string) UserInput {
	return UserInput{Type: "text", Text: text, TextElements: []any{}}
}

// TurnStartParams starts a turn. ClientUserMessageID is the id the server keeps
// on the prompt's record, which is how the prompt is found again.
type TurnStartParams struct {
	ThreadID            string         `json:"threadId"`
	ClientUserMessageID string         `json:"clientUserMessageId,omitempty"`
	Input               []UserInput    `json:"input"`
	CWD                 string         `json:"cwd,omitempty"`
	ApprovalPolicy      string         `json:"approvalPolicy,omitempty"`
	SandboxPolicy       map[string]any `json:"sandboxPolicy,omitempty"`
	Model               string         `json:"model,omitempty"`
	Effort              string         `json:"effort,omitempty"`
}

// TurnStart starts a turn. The answer only says the prompt was accepted: the
// prompt is in the record when its userMessage item completes.
func (c *Client) TurnStart(ctx context.Context, p TurnStartParams) (Turn, error) {
	var r struct {
		Turn Turn `json:"turn"`
	}
	err := c.Call(ctx, MethodTurnStart, p, &r)
	return r.Turn, err
}

// ThreadShellCommand runs a shell command in the thread's environment, outside
// the sandbox and without a model turn.
func (c *Client) ThreadShellCommand(ctx context.Context, threadID, command string, timeoutMs int) error {
	return c.Call(ctx, MethodThreadShell, map[string]any{"threadId": threadID, "command": command, "timeoutMs": timeoutMs}, nil)
}

// CommandExecParams runs one command under a sandbox policy, without a thread.
type CommandExecParams struct {
	Command       []string          `json:"command"`
	CWD           string            `json:"cwd,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	SandboxPolicy map[string]any    `json:"sandboxPolicy,omitempty"`
	TimeoutMs     int               `json:"timeoutMs,omitempty"`
}

// CommandExecResult is how the command ended.
type CommandExecResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// CommandExec runs a command and waits for it.
func (c *Client) CommandExec(ctx context.Context, p CommandExecParams) (CommandExecResult, error) {
	var r CommandExecResult
	return r, c.Call(ctx, MethodCommandExec, p, &r)
}

// ServerVersion extracts the version of the server from the user agent it
// answers initialize with ("<client>/<version> (<platform>) ..."). It returns ""
// when the text does not have that shape.
func ServerVersion(userAgent string) string {
	_, rest, ok := strings.Cut(userAgent, "/")
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, " ")
	return v
}

// ThreadNotification is the payload of the notifications that carry a thread id
// and a turn: turn/started and turn/completed.
type ThreadNotification struct {
	ThreadID string `json:"threadId"`
	Turn     Turn   `json:"turn"`
}

// ItemNotification is the payload of item/completed.
type ItemNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Item     Item   `json:"item"`
}

// StatusNotification is the payload of thread/status/changed.
type StatusNotification struct {
	ThreadID string       `json:"threadId"`
	Status   ThreadStatus `json:"status"`
}

// MCPStatusNotification is the payload of mcpServer/startupStatus/updated.
type MCPStatusNotification struct {
	ThreadID string `json:"threadId"`
	Name     string `json:"name"`
	Status   string `json:"status"`
}

// DecodeParams decodes the payload of a notification.
func DecodeParams[T any](n Notification) (T, error) {
	var v T
	err := json.Unmarshal(n.Params, &v)
	return v, err
}

// IsThreadGone reports whether an error says the thread does not exist in the
// server: it was never written, or it was removed. The server says so in words, not
// in a code, so the words are matched here and nowhere else; any other failure
// leaves the thread's existence unknown. The words are held to the installed
// server by the real-Codex test.
func IsThreadGone(err error) bool {
	var rpc *RPCError
	if !errors.As(err, &rpc) {
		return false
	}
	m := strings.ToLower(rpc.Message)
	return strings.Contains(m, "no rollout found") || strings.Contains(m, "not found") || strings.Contains(m, "does not exist")
}
