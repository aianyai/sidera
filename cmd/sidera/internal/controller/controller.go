// Package controller attaches a session a person opened to the node, so that the
// session can use the Agent interface like a session Sidera created.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The adapter identifiers of the runtimes that can host a controller.
const (
	Claude = "claude"
	Codex  = "codex"
)

// What a controller's environment shows.
const (
	envToken         = "SIDERA_TOKEN"
	envProbeToken    = "SIDERA_PROBE_TOKEN"
	envClaudeCode    = "CLAUDECODE"
	envClaudeSession = "CLAUDE_CODE_SESSION_ID"
	envCodexThread   = "CODEX_THREAD_ID"
)

// claudeWakeKeys are the variables of a Claude Code session that say how to reach
// its inbox: the socket, its token and the process whose registration names the
// session now. They are copied to the node as they are, when present, and are
// secrets. The process is absent from the environment of Claude Code's MCP
// servers; the adapter then finds it by the session id.
var claudeWakeKeys = []string{"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_PID"}

// ErrNotController says the process is not in a session a person opened: a plain
// terminal, or a process that already has a credential of its own.
var ErrNotController = errors.New("the process is not in a controller session")

// Wanted reports whether a process with this environment may become a controller:
// it holds no endpoint token and no probe token.
func Wanted(getenv func(string) string) bool {
	return getenv(envToken) == "" && getenv(envProbeToken) == ""
}

// Request is what `sessions.attach` is asked for. Wake holds secrets.
type Request struct {
	Adapter     string            `json:"adapter"`
	NativeRef   string            `json:"native_ref"`
	DisplayName string            `json:"display_name,omitempty"`
	Wake        map[string]string `json:"wake,omitempty"`
	// Create false only finds the endpoint the session has. Absent, the node makes
	// one when there is none.
	Create *bool `json:"create,omitempty"`
}

// RequestFromEnv builds the request of the session whose environment this is.
// thread is the Codex thread id a Codex MCP server learns from a tool call, empty
// for a process that has no such source. It returns ErrNotController for a
// process that is not in a controller session, and a wire error of category
// unauthenticated when the environment shows a session of a runtime but not
// which session. Claude wins when both runtimes show a session: the environment
// cannot say which started the process, and Claude's variable is the more specific.
func RequestFromEnv(getenv func(string) string, getwd func() (string, error), thread string) (Request, error) {
	if !Wanted(getenv) {
		return Request{}, ErrNotController
	}
	var adapter, ref string
	switch {
	case getenv(envClaudeSession) != "":
		adapter, ref = Claude, getenv(envClaudeSession)
	case getenv(envCodexThread) != "":
		adapter, ref = Codex, getenv(envCodexThread)
	case getenv(envClaudeCode) != "":
		adapter = Claude
	case thread != "":
		adapter, ref = Codex, thread
	default:
		return Request{}, ErrNotController
	}
	if ref == "" {
		return Request{}, SessionUnknown()
	}
	req := Request{Adapter: adapter, NativeRef: ref, DisplayName: displayName(adapter, getwd)}
	if adapter == Claude {
		for _, key := range claudeWakeKeys {
			if value := getenv(key); value != "" {
				if req.Wake == nil {
					req.Wake = map[string]string{}
				}
				req.Wake[key] = value
			}
		}
	}
	return req, nil
}

// SessionUnknown is the error of a process that has no token and cannot tell which
// session it belongs to. Its code is the one a plain terminal gets from the node.
func SessionUnknown() *apiwire.Error {
	return apiwire.New(apiwire.CategoryUnauthenticated, "token_missing",
		"no Sidera token is set and the session id is unknown, so this session cannot attach itself as a controller: "+
			"a Claude Code session is recognised by CLAUDE_CODE_SESSION_ID and a Codex session by its thread id; "+
			"outside them, set SIDERA_TOKEN")
}

func displayName(adapter string, getwd func() (string, error)) string {
	name := map[string]string{Claude: "Claude", Codex: "Codex"}[adapter] + " controller"
	dir, err := getwd()
	if err != nil {
		return name
	}
	base := filepath.Base(dir)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return name
	}
	base = strings.ToValidUTF8(base, "?")
	room := adapterproto.MaxDisplayName - utf8.RuneCountInString(name) - len(" ()")
	for utf8.RuneCountInString(base) > room {
		_, size := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-size]
	}
	// A directory name may hold a character a display name cannot, such as a line
	// break; the name then leaves the directory out instead of changing it.
	full := name + " (" + base + ")"
	if adapterproto.ValidateDisplayName(full) != nil {
		return name
	}
	return full
}

// Wake is what the adapter answered about delivering prompts to the session.
type Wake struct {
	State  string `json:"state"` // ready, unavailable or unknown
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Attachment is the answer of `sessions.attach`. Token is a secret.
type Attachment struct {
	Endpoint string             `json:"endpoint"`
	Instance string             `json:"instance"`
	Token    adapterproto.Token `json:"token"`
	Wake     Wake               `json:"wake"`
}

// Manager is the management client as far as this package uses it.
type Manager interface {
	Call(ctx context.Context, method string, in manageapi.Request) (json.RawMessage, error)
}

// CodeSessionNotAttached is the code of the refusal that answers a request which only
// finds an endpoint (Create false) when the session has none.
const CodeSessionNotAttached = "session_not_attached"

// Attach asks the node to attach the session, or to find the endpoint it already
// has. It carries no operation identifier: the method is safe to repeat.
func Attach(ctx context.Context, m Manager, req Request) (Attachment, error) {
	raw, err := m.Call(ctx, "sessions.attach", manageapi.Request{Params: req})
	if err != nil {
		return Attachment{}, explain(withoutWake(err, req.Wake), req.Adapter)
	}
	var out struct {
		Result Attachment `json:"result"`
	}
	// The node answered success. A member this program does not know is ignored, as
	// a long-running session must keep working under a node updated after it
	// started; an answer it cannot read is said so, and the attachment, which a
	// repeat finds again, is not asked for twice.
	if err := strictjson.DecodeTolerant(raw, &out); err != nil || out.Result.Token.Validate() != nil {
		return Attachment{}, apiwire.AnswerUnreadable(true, "")
	}
	return out.Result, nil
}

// withoutWake returns err with every wake value of the request taken out of its
// words. The node's own errors name a wake key and never its value, and so does
// every error this process makes; this is the one place that keeps a value out of what
// a person, a model or a log is shown should an error on the way carry the request.
func withoutWake(err error, wake map[string]string) error {
	if len(wake) == 0 {
		return err
	}
	clean := func(text string) string {
		for _, value := range wake {
			text = strings.ReplaceAll(text, value, "[wake value]")
		}
		return text
	}
	var wire *apiwire.Error
	if errors.As(err, &wire) {
		if message := clean(wire.Message); message != wire.Message {
			out := *wire
			out.Message, out.Details = message, nil
			return &out
		}
		return err
	}
	if text := clean(err.Error()); text != err.Error() {
		return errors.New(text)
	}
	return err
}

// explain words the refusals of an attachment for the person or model who must
// act on them; the category and code stay as the node gave them.
func explain(err error, adapter string) error {
	var wire *apiwire.Error
	if !errors.As(err, &wire) {
		return err
	}
	var message string
	switch wire.Code {
	case "instance_unknown":
		message = "this node has no enabled " + adapter + " adapter instance to attach the session to; enable one with `sidera adapter enable " + adapter + "`"
	case "instance_required":
		message = "this node has several " + adapter + " adapter instances and the session cannot choose one; leave one of them enabled"
	case "attach_unsupported":
		message = "the " + adapter + " adapter of this node does not take sessions a person opened; update the adapter"
	case "native_session_created":
		message = "this session was created by Sidera, which gives it its own SIDERA_TOKEN; it cannot attach itself as a controller"
	default:
		return err
	}
	out := *wire
	out.Message = message
	return &out
}

// Arrival is what a model is told about an attachment its session made: where it is
// attached, whether the node can wake it and, when it had an endpoint that was closed
// before, which one.
type Arrival struct {
	// Endpoint is the address of the endpoint the session is attached as.
	Endpoint string
	// Replaced is the address of the endpoint the session held until it was closed;
	// empty when it had none.
	Replaced string
	Wake     Wake
}

const (
	wakesByItself  = "A reply to a message you send wakes this session by itself."
	collectReplies = "Replies will not interrupt you: call recv with a wait (wait_s, or --wait on the command line) inside a turn, or run a background watcher, to collect them."
)

func (w Wake) cause() string {
	why := w.Reason
	if why == "" {
		why = w.State
	}
	if w.Detail != "" {
		why += ": " + w.Detail
	}
	return why
}

// Notice is the text a controller's model reads on its first use, saying whether
// the node can wake the session when a reply arrives.
func Notice(w Wake) string {
	if w.State == "ready" {
		return "Sidera: this session is attached as a controller. " + wakesByItself
	}
	return "Sidera: this session is attached as a controller, but the node cannot wake it (" + w.cause() + "). " + collectReplies
}

// Notice is the text the model reads with the result of the call that made the
// attachment. A session that lost its endpoint is told the old address is closed and
// the new one is where its peers must write.
func (a Arrival) Notice() string {
	if a.Replaced == "" || a.Replaced == a.Endpoint {
		return Notice(a.Wake)
	}
	text := "Sidera: the endpoint of this session, " + a.Replaced + ", was closed, so messages sent to that address are no longer delivered. " +
		"This session is attached again as " + a.Endpoint + "; tell the peers that wrote to the old address to use the new one. "
	if a.Wake.State == "ready" {
		return text + wakesByItself
	}
	return text + "The node cannot wake it (" + a.Wake.cause() + "). " + collectReplies
}
