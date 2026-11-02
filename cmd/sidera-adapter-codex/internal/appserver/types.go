package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	MethodThreadRead        = "thread/read"
	MethodThreadQueueAdd    = "thread/queue/add"
	MethodThreadQueueList   = "thread/queue/list"
	MethodThreadQueueDelete = "thread/queue/delete"
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
	// ExperimentalAPI opts the connection into the methods the server marks
	// experimental, which a request to one of them is refused without. The queue of
	// a thread's pending prompts (thread/queue/*) is one of them.
	ExperimentalAPI bool `json:"experimentalApi,omitempty"`
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
	// Ephemeral is set for a thread that is never written to disk.
	Ephemeral bool `json:"ephemeral"`
	// ParentThreadID is set for a thread a sub-agent runs in.
	ParentThreadID *string `json:"parentThreadId"`
	// Source says where the thread came from: a word, or an object for a
	// sub-agent's.
	Source json.RawMessage `json:"source"`
}

// IsSubAgent reports whether the thread belongs to a sub-agent, which takes no
// input from outside.
func (t Thread) IsSubAgent() bool {
	if t.ParentThreadID != nil && *t.ParentThreadID != "" {
		return true
	}
	var src struct {
		SubAgent json.RawMessage `json:"subAgent"`
	}
	return json.Unmarshal(t.Source, &src) == nil && len(src.SubAgent) > 0
}

// ThreadStartParams starts a thread. Config and DeveloperInstructions are the
// thread-level configuration: the server does not keep them, so every resume
// sends them again.
type ThreadStartParams struct {
	CWD            string         `json:"cwd,omitempty"`
	ApprovalPolicy string         `json:"approvalPolicy,omitempty"`
	Sandbox        string         `json:"sandbox,omitempty"`
	Model          string         `json:"model,omitempty"`
	Ephemeral      bool           `json:"ephemeral"`
	Config         map[string]any `json:"config,omitempty"`

	DeveloperInstructions string `json:"developerInstructions,omitempty"`
}

// ActivePermissionProfile names the permission profile a thread runs under: the
// id of a `[permissions.<id>]` table, or of a built-in profile such as ":workspace".
type ActivePermissionProfile struct {
	ID      string  `json:"id"`
	Extends *string `json:"extends"`
}

// ThreadResult is the answer of thread/start and thread/resume.
// ActivePermissionProfile is nil when the thread runs under no named profile (a
// preset without a sandbox, or a thread whose profile Codex dropped). The server
// sends it without the experimental API being asked for, though its published
// schema leaves it out.
type ThreadResult struct {
	Thread                  Thread                   `json:"thread"`
	ActivePermissionProfile *ActivePermissionProfile `json:"activePermissionProfile"`
}

// ThreadStart starts a thread and subscribes this connection to it.
func (c *Client) ThreadStart(ctx context.Context, p ThreadStartParams) (ThreadResult, error) {
	var r ThreadResult
	err := c.Call(ctx, MethodThreadStart, p, &r)
	return r, err
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

	DeveloperInstructions string `json:"developerInstructions,omitempty"`
}

// ThreadResume loads a thread and subscribes this connection to it.
func (c *Client) ThreadResume(ctx context.Context, p ThreadResumeParams) (ThreadResult, error) {
	var r ThreadResult
	err := c.Call(ctx, MethodThreadResume, p, &r)
	return r, err
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

// Turn is one run of the agent on a prompt. StartedAt and CompletedAt are Unix
// seconds; CompletedAt is null until the turn has ended.
type Turn struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Items       []Item     `json:"items"`
	Error       *TurnError `json:"error"`
	StartedAt   *int64     `json:"startedAt"`
	CompletedAt *int64     `json:"completedAt"`
}

// TurnError is why a turn failed. The message is for the log only.
type TurnError struct {
	Message string `json:"message"`
}

// Running reports whether the turn has not ended.
func (t Turn) Running() bool { return t.Status == TurnInProgress }

// Unfinished reports whether a turn that another process runs has not ended, as it
// reads from outside that process: the record has no completion time. The status
// of such a turn is "interrupted", not "inProgress" (the record holds no end for it
// and the reader does not run it), so the completion time is what tells it from a
// turn the person interrupted, which has one.
func (t Turn) Unfinished() bool {
	return t.Running() || (t.Status == TurnInterrupted && t.CompletedAt == nil)
}

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

// ErrUnrecognizedRecord is returned when a page of turns asked for with their
// full items is not in the form the adapter reads a record in. The adapter finds a
// prompt by the client id on a userMessage item, and decoding ignores what it does
// not know, so a record written in another form would read as holding no prompts:
// the answer is an error instead, which callers treat like a record that could not
// be read.
var ErrUnrecognizedRecord = errors.New("appserver: the record is not in a form this adapter reads")

// ThreadTurnsList reads one page of turns. With ItemsView "full" the page must
// also be in the recognized form (see ErrUnrecognizedRecord).
func (c *Client) ThreadTurnsList(ctx context.Context, p ThreadTurnsListParams) (ThreadTurnsListResult, error) {
	var raw json.RawMessage
	if err := c.Call(ctx, MethodThreadTurnsList, p, &raw); err != nil {
		return ThreadTurnsListResult{}, err
	}
	var r ThreadTurnsListResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return ThreadTurnsListResult{}, fmt.Errorf("appserver: %s result: %w", MethodThreadTurnsList, err)
	}
	if p.ItemsView == "full" {
		if err := checkFullRecord(raw); err != nil {
			return ThreadTurnsListResult{}, err
		}
	}
	return r, nil
}

// checkFullRecord verifies the members a prompt is found by: every turn lists its
// items and says they are all there, and every userMessage item has a clientId
// (null when the prompt was not given one). The server writes all three always, so
// an absent one means the form changed, and a missing item list or a renamed id
// must not be read as "no prompt here".
func checkFullRecord(page json.RawMessage) error {
	var turns struct {
		Data []struct {
			Items     *[]json.RawMessage `json:"items"`
			ItemsView *string            `json:"itemsView"`
		} `json:"data"`
	}
	if err := json.Unmarshal(page, &turns); err != nil {
		return fmt.Errorf("%w: %v", ErrUnrecognizedRecord, err)
	}
	for _, t := range turns.Data {
		if t.Items == nil || t.ItemsView == nil || *t.ItemsView != "full" {
			return fmt.Errorf("%w: a turn without its full list of items", ErrUnrecognizedRecord)
		}
		for _, raw := range *t.Items {
			var it struct {
				Type     string          `json:"type"`
				ClientID json.RawMessage `json:"clientId"`
			}
			if err := json.Unmarshal(raw, &it); err != nil {
				return fmt.Errorf("%w: %v", ErrUnrecognizedRecord, err)
			}
			if it.Type == ItemUserMessage && it.ClientID == nil {
				return fmt.Errorf("%w: a %s item without clientId", ErrUnrecognizedRecord, ItemUserMessage)
			}
		}
	}
	return nil
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
// server by the real-Codex test. ("thread not loaded" is what thread/read and
// thread/turns/list say of an id nothing stored carries.)
func IsThreadGone(err error) bool {
	var rpc *RPCError
	if !errors.As(err, &rpc) {
		return false
	}
	m := strings.ToLower(rpc.Message)
	return strings.Contains(m, "no rollout found") || strings.Contains(m, "not found") || strings.Contains(m, "does not exist") ||
		strings.Contains(m, "thread not loaded")
}

// CodeInvalidRequest is the code of an answer that refuses a request for what it
// asks: the server did nothing.
const CodeInvalidRequest = -32600

// IsInvalidRequest reports whether the server refused a request as invalid, which
// it does before it acts on it.
func IsInvalidRequest(err error) bool {
	var rpc *RPCError
	return errors.As(err, &rpc) && rpc.Code == CodeInvalidRequest
}

// ThreadRead reads a thread's metadata, without its turns. It works for a thread
// another process has loaded.
func (c *Client) ThreadRead(ctx context.Context, threadID string) (Thread, error) {
	var r struct {
		Thread Thread `json:"thread"`
	}
	err := c.Call(ctx, MethodThreadRead, map[string]any{"threadId": threadID, "includeTurns": false}, &r)
	return r.Thread, err
}

// QueuedSubmission is a prompt waiting in a thread's queue. ClientUserMessageID is
// the id the prompt's record keeps once its turn starts.
type QueuedSubmission struct {
	ID                  string      `json:"id"`
	ClientUserMessageID string      `json:"clientUserMessageId"`
	Input               []UserInput `json:"input"`
}

// ThreadQueueAdd puts a prompt into a thread's queue. The queue is shared by every
// process that uses the same Codex home: the one that has the thread loaded takes
// the prompt when the thread is idle, which for a process other than this server
// can take several seconds, and none takes it while no process has the thread
// loaded. The server refuses a thread that is archived, ephemeral or a
// sub-agent's, or that it does not know. The method is experimental (see
// InitializeCapabilities.ExperimentalAPI).
func (c *Client) ThreadQueueAdd(ctx context.Context, threadID, clientUserMessageID string, input []UserInput) (QueuedSubmission, error) {
	var r struct {
		QueuedSubmission QueuedSubmission `json:"queuedSubmission"`
	}
	err := c.Call(ctx, MethodThreadQueueAdd, map[string]any{
		"threadId": threadID, "clientUserMessageId": clientUserMessageID, "input": input,
	}, &r)
	return r.QueuedSubmission, err
}

// ThreadQueueList reads one page of the prompts waiting in a thread's queue; more
// says there are further pages.
func (c *Client) ThreadQueueList(ctx context.Context, threadID string, limit int) (queued []QueuedSubmission, more bool, err error) {
	var r struct {
		Data       []QueuedSubmission `json:"data"`
		NextCursor *string            `json:"nextCursor"`
	}
	if err := c.Call(ctx, MethodThreadQueueList, map[string]any{"threadId": threadID, "limit": limit}, &r); err != nil {
		return nil, false, err
	}
	return r.Data, r.NextCursor != nil && *r.NextCursor != "", nil
}

// ThreadQueueDelete takes a prompt out of a thread's queue by the id the queue gave
// it, and says whether it was still there: a prompt the thread's owner took in the
// meantime is not, and that is no error.
func (c *Client) ThreadQueueDelete(ctx context.Context, threadID, queuedSubmissionID string) (deleted bool, err error) {
	var r struct {
		Deleted bool `json:"deleted"`
	}
	err = c.Call(ctx, MethodThreadQueueDelete, map[string]any{"threadId": threadID, "queuedSubmissionId": queuedSubmissionID}, &r)
	return r.Deleted, err
}
