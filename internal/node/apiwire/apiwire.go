// Package apiwire is the one place where what the engine and the packages below
// it say (a typed reason, a refused change, a failed check of the way to the
// daemon) becomes what the Agent and management interfaces say on the wire: the
// category and code of an error, the body that carries them, the status of the
// HTTP answer and the exit code of the command line. The engine never speaks the
// words of the wire; every interface and every client maps through here, so one
// reason has one pair of words everywhere. See README.md.
package apiwire

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// Category is the class of an error, the first thing a client decides on.
type Category string

// The categories.
const (
	CategoryInvalidArgument Category = "invalid_argument"
	CategoryUnauthenticated Category = "unauthenticated"
	CategoryNotFound        Category = "not_found"
	CategoryConflict        Category = "conflict"
	CategoryRefused         Category = "refused"
	CategoryBackpressure    Category = "backpressure"
	CategoryUnavailable     Category = "unavailable"
	CategoryInternal        Category = "internal"
	// CategoryOutcomeUnknown: whether the request took effect is not known. A
	// client judges it itself when it wrote a request and did not read the answer
	// whole (OutcomeUnknown); the daemon says it for a failure of the disk after
	// which memory and disk may disagree (ReasonStorageFatal). Either way the way
	// out is the same: send the request again with the same operation identifier.
	CategoryOutcomeUnknown Category = "outcome_unknown"
)

// Categories lists every category.
func Categories() []Category {
	return []Category{
		CategoryInvalidArgument, CategoryUnauthenticated, CategoryNotFound, CategoryConflict,
		CategoryRefused, CategoryBackpressure, CategoryUnavailable, CategoryInternal,
		CategoryOutcomeUnknown,
	}
}

// The exit codes of the command line that no daemon-side category gives: a command
// that was used wrongly, and a request whose outcome is not known.
const (
	ExitUsage          = 2
	ExitOutcomeUnknown = 10
)

// HTTPStatus is the status of the answer. Clients decide on the category and the
// code; the status is a hint for people and for the remote management that will
// reuse the same handlers.
func (c Category) HTTPStatus() int {
	switch c {
	case CategoryInvalidArgument:
		return http.StatusBadRequest
	case CategoryUnauthenticated:
		return http.StatusUnauthorized
	case CategoryNotFound:
		return http.StatusNotFound
	case CategoryConflict:
		return http.StatusConflict
	case CategoryRefused:
		return http.StatusUnprocessableEntity
	case CategoryBackpressure:
		return http.StatusTooManyRequests
	case CategoryUnavailable:
		return http.StatusServiceUnavailable
	}
	// internal, outcome_unknown (the daemon is failing when it says it) and any
	// category that is none of the known ones.
	return http.StatusInternalServerError
}

// ExitCode is the exit code of the command line for an error of the category.
// Success is 0 and no category gives it; an unknown category is an internal
// error.
func (c Category) ExitCode() int {
	switch c {
	case CategoryInvalidArgument:
		return 3
	case CategoryUnauthenticated:
		return 4
	case CategoryNotFound:
		return 5
	case CategoryConflict:
		return 6
	case CategoryRefused:
		return 7
	case CategoryBackpressure:
		return 8
	case CategoryUnavailable:
		return 9
	case CategoryOutcomeUnknown:
		return ExitOutcomeUnknown
	}
	return 1
}

// Error is an error as the wire carries it.
type Error struct {
	Category Category `json:"category"`
	// Code is a stable lower-case word; clients branch on it.
	Code string `json:"code"`
	// Message is English for a person. It holds no message body, no token and no
	// key.
	Message string `json:"message"`
	// Details is present only when there is something to say beyond the code.
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s/%s: %s", e.Category, e.Code, e.Message) }

// Body is the body of an answer that is an error.
type Body struct {
	Error Error `json:"error"`
}

// New makes an error of the wire.
func New(c Category, code, message string) *Error {
	return &Error{Category: c, Code: code, Message: message}
}

func (e *Error) with(k string, v any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[k] = v
	return e
}

// Codes of the wire that more than one place makes. A code that only the mapping
// below makes is written there.
const (
	CodeMalformedRequest  = "malformed_request"
	CodeRequestTooLarge   = "request_too_large"
	CodeMethodNotAllowed  = "method_not_allowed"
	CodeMethodUnknown     = "method_unknown"
	CodeUnexpectedVersion = "unexpected_version"
	CodeInternal          = "internal"
	CodeEndpointUnknown   = "endpoint_unknown"
	CodeAlertUnknown      = "alert_unknown"
	CodeLeaseUnknown      = "lease_unknown"
	CodeOperationUnknown  = "operation_unknown"
	CodeOutcomeUnknown    = "outcome_unknown"
)

// EndpointUnknown is the answer for an address that names no endpoint of this
// node: one that does not exist, and one of another node, which this node cannot
// know. The engine's refusal for an unknown endpoint is worded the same way.
func EndpointUnknown() *Error {
	return New(CategoryNotFound, CodeEndpointUnknown, "there is no such endpoint on this node")
}

// OperationUnknown is the answer for an operation identifier that has no stored
// result: it was never used, or its result has been dropped.
func OperationUnknown() *Error {
	return New(CategoryNotFound, CodeOperationUnknown, "there is no stored result for this operation identifier")
}

// OutcomeUnknown is what a client says when it wrote a request and did not read the
// answer whole, so it cannot tell whether the request took effect. The operation
// identifier, when the request had one, is in the details: it is the one to send the
// request again with, which returns the stored result if the request did run and
// runs it if it did not. This is the one definition the command line, the MCP front
// end and the test fixture share.
func OutcomeUnknown(operationID string) *Error {
	e := New(CategoryOutcomeUnknown, CodeOutcomeUnknown,
		"the request was sent and no complete answer was read; whether it took effect is not known: send it again with the same operation identifier")
	if operationID != "" {
		e.with("operation_id", operationID)
	}
	return e
}

// Malformed is a request that is not what the method takes. path names the value
// at fault, when the decoder could place it.
func Malformed(message, path string) *Error {
	e := New(CategoryInvalidArgument, CodeMalformedRequest, message)
	if path != "" {
		e.with("path", path)
	}
	return e
}

// FromError maps an error to its words on the wire: the error of the wire itself
// as it is, a refusal of the engine, a refused change of the configuration, a
// fault in a request, a failure of the way to the daemon, and everything else as
// an internal error whose text stays out of the answer. nil is nil.
func FromError(err error) *Error {
	if err == nil {
		return nil
	}
	var we *Error
	var ref *engine.Refusal
	var ce *config.ChangeError
	var se *strictjson.Error
	var pe *ipc.PeerCheckError
	switch {
	case errors.As(err, &we):
		return we
	case errors.As(err, &ref):
		return fromRefusal(ref)
	case errors.As(err, &ce):
		return fromChange(ce)
	case errors.As(err, &se):
		return Malformed("the request is not what the method takes: "+se.Error(), se.Path)
	case errors.As(err, &pe):
		return New(CategoryUnavailable, "peer_check_failed", pe.Error())
	case errors.Is(err, ipc.ErrDaemonNotRunning):
		return New(CategoryUnavailable, "daemon_not_running", "the daemon is not running; start it with `sidera daemon`")
	}
	return New(CategoryInternal, CodeInternal, "internal error")
}

func fromRefusal(r *engine.Refusal) *Error {
	switch r.Reason {
	case engine.ReasonUnauthenticated:
		return New(CategoryUnauthenticated, "token_unknown", "the token is not one of an open endpoint")
	case engine.ReasonEndpointUnknown:
		return EndpointUnknown()
	case engine.ReasonInstanceUnknown:
		return New(CategoryNotFound, "instance_unknown", "there is no such adapter instance")
	case engine.ReasonWrongEndpointKind:
		return New(CategoryRefused, "endpoint_kind", "this cannot be done to an endpoint of that kind")
	case engine.ReasonEndpointLimit:
		e := New(CategoryRefused, "endpoint_limit", "no more endpoints can be opened")
		if r.Limit != 0 {
			e.with("limit", r.Limit.String())
		}
		return e
	case engine.ReasonInvalidArgument:
		// A refusal that says only that a value is not acceptable says the request is
		// not well formed; the codes of the category are the listed ones.
		code := CodeMalformedRequest
		if r.Detail != 0 {
			code = r.Detail.String()
		}
		e := New(CategoryInvalidArgument, code, "a value of the request is not acceptable")
		if r.Field != "" {
			e.with("field", r.Field)
		}
		if r.Path != "" {
			e.with("path", r.Path)
		}
		return e
	case engine.ReasonOperationIDReused:
		return New(CategoryConflict, "operation_id_reused", "the operation identifier was used for another request")
	case engine.ReasonIdempotencyConflict:
		return New(CategoryConflict, "idempotency_conflict", "the idempotency key was used for another request: other content or other recipients")
	case engine.ReasonMessageConflict:
		return New(CategoryConflict, "message_conflict", "a message with this identifier from this sender was taken in before with other content")
	case engine.ReasonMessageUnknown:
		return New(CategoryNotFound, "message_unknown", "the endpoint has no such message")
	case engine.ReasonLeaseUnknown:
		return New(CategoryNotFound, CodeLeaseUnknown, "the endpoint was handed no page under this lease")
	case engine.ReasonTooLarge:
		e := New(CategoryRefused, "too_large", "the body is longer than the protocol allows")
		if r.Field != "" {
			e.with("field", r.Field)
		}
		return e
	case engine.ReasonNodeQueueFull:
		return New(CategoryBackpressure, "node_queue_full", "the node holds as many unfinished deliveries as it may; try again later")
	case engine.ReasonStorageFull:
		return New(CategoryBackpressure, "storage_full", "the disk is full or nearly; nothing was recorded")
	case engine.ReasonStorageUnavailable:
		return New(CategoryBackpressure, "storage_unavailable", "the disk could not be written; nothing was recorded, try again")
	case engine.ReasonClockRollback:
		return New(CategoryBackpressure, "clock_rollback", "the clock was set back; the owner has to clear the pause")
	case engine.ReasonLoopBusy:
		return New(CategoryBackpressure, "loop_busy", "the node is busy; try again")
	case engine.ReasonRecovering:
		return New(CategoryUnavailable, "recovering", "the node is starting; it does not take this yet")
	case engine.ReasonShuttingDown:
		return New(CategoryUnavailable, "shutting_down", "the node is stopping")
	case engine.ReasonAlertUnknown:
		return New(CategoryNotFound, CodeAlertUnknown, "the node holds no alert with this identifier")
	case engine.ReasonIdentityRetired:
		return New(CategoryUnavailable, "identity_retired", "the identity of this node was retired; nothing is signed in its name and the node is stopping")
	case engine.ReasonInstallInProgress:
		return New(CategoryConflict, "install_in_progress", "another adapter package is being installed; try again when it is done")
	case engine.ReasonInstallRepeated:
		return New(CategoryConflict, "install_in_progress", "this installation is still in progress; ask for its result with the same operation identifier when it is done")
	case engine.ReasonPlatformUnsupported:
		return New(CategoryRefused, "platform_unsupported", "the package does not run on this platform")
	case engine.ReasonProbeTokenUsed:
		return New(CategoryUnauthenticated, "probe_token_used", "the probe token was presented before and is good once")
	case engine.ReasonProbeTokenExpired:
		return New(CategoryUnauthenticated, "probe_token_expired", "the probe token expired; it is good for a few minutes")
	case engine.ReasonStorageFatal:
		// Whether the command took effect is what the engine says it does not know,
		// which is the outcome a client cannot tell either, with the same way out.
		return New(CategoryOutcomeUnknown, "storage_fatal", "a disk failure stopped the node; whether this took effect is not known: send it again with the same operation identifier")
	}
	return New(CategoryInternal, CodeInternal, "internal error")
}

func fromChange(c *config.ChangeError) *Error {
	switch c.Reason {
	case config.ChangeInvalid:
		e := New(CategoryInvalidArgument, "config_invalid", "a value is not acceptable")
		if c.Field != "" {
			e.with("path", c.Field)
		}
		if c.Err != nil {
			e.with("reason", c.Err.Error())
		}
		return e
	case config.ChangeVersionConflict:
		return New(CategoryConflict, "version_conflict", "the setting changed since it was read; read it again").
			with("current_version", uint64(c.Current))
	case config.ChangePresetExists:
		return New(CategoryConflict, "preset_exists", "a preset of this name exists").named(c)
	case config.ChangePresetUnknown:
		return New(CategoryNotFound, "preset_unknown", "there is no preset of this name").named(c)
	case config.ChangeInstanceUnknown:
		return New(CategoryNotFound, "instance_unknown", "there is no adapter instance of this name").named(c)
	case config.ChangeInstanceExists:
		return New(CategoryConflict, "instance_exists", "an instance of this name exists; its configuration is not replaced").named(c)
	case config.ChangeInstanceAdapterMismatch:
		return New(CategoryConflict, "instance_adapter_mismatch", "the instance belongs to another adapter").named(c)
	case config.ChangeEndpointUnknown:
		return New(CategoryNotFound, "endpoint_unknown", "there is no such endpoint of the instance").named(c)
	case config.ChangeAdapterNotInstalled:
		return New(CategoryNotFound, "adapter_unknown", "the adapter is not installed").named(c)
	case config.ChangeAdapterInUse:
		return New(CategoryConflict, "adapter_in_use", "an instance of the adapter is enabled; disable it first").named(c)
	case config.ChangeNothingToChange:
		return New(CategoryInvalidArgument, "empty_patch", "the patch names no setting")
	}
	return New(CategoryInternal, CodeInternal, "internal error")
}

// named adds the name a refused change is about, when it has one.
func (e *Error) named(c *config.ChangeError) *Error {
	if c.Field != "" {
		e.with("name", c.Field)
	}
	return e
}

// FromAdmin maps the denial or the refusal of a request by the administrative
// rules. The local owner is denied only an operation that is not in the catalogue,
// which is a defect of the program; the reasons that a remote person can meet are
// for the remote management to word when it exists, and until then they are
// refused as not permitted.
func FromAdmin(r admin.Reason) *Error {
	switch r {
	case admin.ReasonMissingOperationID:
		return New(CategoryInvalidArgument, "missing_operation_id", "a request that changes something carries an operation identifier")
	case admin.ReasonMissingExpectedVersion:
		return New(CategoryInvalidArgument, "missing_expected_version", "a change of a setting carries the version of the setting it is based on")
	case admin.ReasonUnknownOperation:
		return New(CategoryInternal, "unknown_operation", "the operation is not in the catalogue of administrative operations")
	}
	return New(CategoryRefused, "not_permitted", "this operation is not permitted").with("reason", string(r))
}
