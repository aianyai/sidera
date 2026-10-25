package apiwire_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

type want struct {
	category apiwire.Category
	code     string
}

// The words of the wire for each reason the engine refuses for, written out by
// hand from the table of error categories of the management and Agent
// interfaces: the mapping is checked against this list, and the list is checked
// to hold every reason the engine has, so a new reason without a row fails here.
var engineReasons = map[engine.Reason]want{
	engine.ReasonUnauthenticated:    {"unauthenticated", "token_unknown"},
	engine.ReasonEndpointUnknown:    {"not_found", "endpoint_unknown"},
	engine.ReasonInstanceUnknown:    {"not_found", "instance_unknown"},
	engine.ReasonWrongEndpointKind:  {"refused", "endpoint_kind"},
	engine.ReasonEndpointLimit:      {"refused", "endpoint_limit"},
	engine.ReasonInvalidArgument:    {"invalid_argument", "invalid_value"},
	engine.ReasonOperationIDReused:  {"conflict", "operation_id_reused"},
	engine.ReasonNodeQueueFull:      {"backpressure", "node_queue_full"},
	engine.ReasonStorageFull:        {"backpressure", "storage_full"},
	engine.ReasonStorageUnavailable: {"backpressure", "storage_unavailable"},
	engine.ReasonClockRollback:      {"backpressure", "clock_rollback"},
	engine.ReasonLoopBusy:           {"backpressure", "loop_busy"},
	engine.ReasonRecovering:         {"unavailable", "recovering"},
	engine.ReasonShuttingDown:       {"unavailable", "shutting_down"},
	engine.ReasonStorageFatal:       {"internal", "storage_fatal"},
}

func check(t *testing.T, got *apiwire.Error, w want) {
	t.Helper()
	if got == nil || string(got.Category) != string(w.category) || got.Code != w.code {
		t.Errorf("mapped to %+v, want %s/%s", got, w.category, w.code)
	}
	if got != nil && got.Message == "" {
		t.Errorf("%s/%s has no message", got.Category, got.Code)
	}
}

func TestEveryReasonOfTheEngineHasItsWordsOnTheWire(t *testing.T) {
	for _, r := range engine.Reasons() {
		w, ok := engineReasons[r]
		if !ok {
			t.Errorf("reason %s has no row in the expected table", r)
			continue
		}
		t.Run(r.String(), func(t *testing.T) {
			check(t, apiwire.FromError(&engine.Refusal{Reason: r}), w)
		})
	}
	if len(engineReasons) != len(engine.Reasons()) {
		t.Errorf("the table has %d rows for %d reasons", len(engineReasons), len(engine.Reasons()))
	}
}

func TestARefusalCarriesWhatSaysWhichLimitAndWhichValue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		want    want
		details map[string]any
	}{
		{"the limit of the node", &engine.Refusal{Reason: engine.ReasonEndpointLimit, Limit: engine.LimitNode},
			want{"refused", "endpoint_limit"}, map[string]any{"limit": "node"}},
		{"the limit of the instance", &engine.Refusal{Reason: engine.ReasonEndpointLimit, Limit: engine.LimitInstance},
			want{"refused", "endpoint_limit"}, map[string]any{"limit": "instance"}},
		{"an empty patch", &engine.Refusal{Reason: engine.ReasonInvalidArgument, Field: "settings", Detail: engine.DetailEmptyPatch},
			want{"invalid_argument", "empty_patch"}, map[string]any{"field": "settings"}},
		{"a value", &engine.Refusal{Reason: engine.ReasonInvalidArgument, Field: "endpoint"},
			want{"invalid_argument", "invalid_value"}, map[string]any{"field": "endpoint"}},
		{"a reason wrapped by the caller", fmt.Errorf("closing: %w", &engine.Refusal{Reason: engine.ReasonRecovering}),
			want{"unavailable", "recovering"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := apiwire.FromError(tc.err)
			check(t, got, tc.want)
			if fmt.Sprint(got.Details) != fmt.Sprint(tc.details) && !(len(got.Details) == 0 && len(tc.details) == 0) {
				t.Errorf("details %v, want %v", got.Details, tc.details)
			}
		})
	}
}

// A refused change of the configuration, each reason in the words of the wire;
// the table runs over every reason the configuration has.
func TestEveryRefusedChangeOfTheConfigurationHasItsWordsOnTheWire(t *testing.T) {
	rows := map[config.ChangeReason]want{
		config.ChangeInvalid:                 {"invalid_argument", "config_invalid"},
		config.ChangeVersionConflict:         {"conflict", "version_conflict"},
		config.ChangePresetExists:            {"conflict", "preset_exists"},
		config.ChangePresetUnknown:           {"not_found", "preset_unknown"},
		config.ChangeInstanceUnknown:         {"not_found", "instance_unknown"},
		config.ChangeInstanceExists:          {"conflict", "instance_exists"},
		config.ChangeInstanceAdapterMismatch: {"conflict", "instance_adapter_mismatch"},
		config.ChangeEndpointUnknown:         {"not_found", "endpoint_unknown"},
		config.ChangeAdapterNotInstalled:     {"not_found", "adapter_unknown"},
		config.ChangeAdapterInUse:            {"conflict", "adapter_in_use"},
		config.ChangeNothingToChange:         {"invalid_argument", "empty_patch"},
	}
	seen := 0
	for r := config.ChangeReason(1); r.String() != "unknown"; r++ {
		seen++
		w, ok := rows[r]
		if !ok {
			t.Errorf("change reason %s has no row in the expected table", r)
			continue
		}
		t.Run(r.String(), func(t *testing.T) {
			check(t, apiwire.FromError(&config.ChangeError{Reason: r, Field: "x", Err: errors.New("detail")}), w)
		})
	}
	if seen != len(rows) {
		t.Errorf("the configuration has %d reasons, the table %d rows", seen, len(rows))
	}
}

// The conflict names the current version and the invalid value its path, so that
// the client can refresh and decide again, or tell the owner which value is wrong.
func TestAConflictNamesTheCurrentVersionAndAnInvalidValueItsPath(t *testing.T) {
	got := apiwire.FromError(&config.ChangeError{Reason: config.ChangeVersionConflict, Current: 7})
	if v, ok := got.Details["current_version"]; !ok || fmt.Sprint(v) != "7" {
		t.Errorf("details %v, want current_version 7", got.Details)
	}
	got = apiwire.FromError(&config.ChangeError{Reason: config.ChangeInvalid, Field: "node_queue_limit", Err: errors.New("below the minimum")})
	if got.Details["path"] != "node_queue_limit" || got.Details["reason"] != "below the minimum" {
		t.Errorf("details %v, want the path and the reason", got.Details)
	}
}

func TestAMalformedRequestNamesThePositionOfTheFault(t *testing.T) {
	var v struct {
		A int `json:"a"`
	}
	err := strictjson.Decode([]byte(`{"a":"x"}`), &v)
	var se *strictjson.Error
	if !errors.As(err, &se) || se.Path == "" {
		t.Fatalf("setting up: %v", err)
	}
	got := apiwire.FromError(fmt.Errorf("decoding params: %w", err))
	check(t, got, want{"invalid_argument", "malformed_request"})
	if got.Details["path"] != se.Path {
		t.Errorf("details %v, want the path %q", got.Details, se.Path)
	}
	// a fault with no position names none
	err = strictjson.Decode([]byte(`{"a":1} x`), &v)
	got = apiwire.FromError(err)
	check(t, got, want{"invalid_argument", "malformed_request"})
	if _, ok := got.Details["path"]; ok {
		t.Errorf("details %v name a position the decoder did not give", got.Details)
	}
}

// What the client finds out about the way to the daemon, and what it must never
// take for the daemon's own refusal: a daemon that is not running is not a daemon
// that failed a check of the peer.
func TestTheClientSideFailuresOfTheWayToTheDaemon(t *testing.T) {
	notRunning := fmt.Errorf("%w: /x/manage.sock", ipc.ErrDaemonNotRunning)
	check(t, apiwire.FromError(notRunning), want{"unavailable", "daemon_not_running"})
	peer := &ipc.PeerCheckError{Failure: ipc.FailDirectoryMode, Path: "/x/manage", Detail: "mode 0755, want 0700"}
	got := apiwire.FromError(fmt.Errorf("dialling: %w", peer))
	check(t, got, want{"unavailable", "peer_check_failed"})
	if !strings.Contains(got.Message, "mode 0755") {
		t.Errorf("the message %q does not say which check failed", got.Message)
	}
}

func TestTheDenialOfAnAdministrativeOperation(t *testing.T) {
	for _, tc := range []struct {
		reason admin.Reason
		want   want
	}{
		{admin.ReasonMissingOperationID, want{"invalid_argument", "missing_operation_id"}},
		{admin.ReasonMissingExpectedVersion, want{"invalid_argument", "missing_expected_version"}},
		// the local owner is denied only an operation outside the catalogue, which
		// is a defect of the program, not of the request
		{admin.ReasonUnknownOperation, want{"internal", "unknown_operation"}},
		{admin.ReasonLocalOwnerOnly, want{"refused", "not_permitted"}},
		{admin.ReasonListLevelTooLow, want{"refused", "not_permitted"}},
	} {
		t.Run(string(tc.reason), func(t *testing.T) { check(t, apiwire.FromAdmin(tc.reason), tc.want) })
	}
}

// An error that is none of the known ones is an internal error, and what it says
// stays in the log: a message that carried it could carry a path, a name or
// worse to a client that is not the one who wrote the code.
func TestAnUnknownErrorIsInternalAndKeepsItsTextOffTheWire(t *testing.T) {
	got := apiwire.FromError(errors.New("open /home/x/.sidera/keys/identity.key: secret-token-abc"))
	check(t, got, want{"internal", "internal"})
	if strings.Contains(got.Message, "secret-token-abc") || strings.Contains(got.Message, ".sidera") {
		t.Errorf("the message %q carries the text of the error", got.Message)
	}
	check(t, apiwire.FromError(context.DeadlineExceeded), want{"internal", "internal"})
	if apiwire.FromError(nil) != nil {
		t.Error("no error is not an error")
	}
}

// An error that is already an error of the wire stays as it is, however it was
// wrapped on the way.
func TestAnErrorOfTheWireIsReturnedAsItIs(t *testing.T) {
	e := &apiwire.Error{Category: apiwire.CategoryNotFound, Code: "method_unknown", Message: "no such method"}
	got := apiwire.FromError(fmt.Errorf("serving: %w", e))
	if got != e {
		t.Errorf("got %+v, want the error that was wrapped", got)
	}
}

// The body of an error, byte for byte: members in this order, details only when
// there are some.
func TestTheBodyOfAnErrorGolden(t *testing.T) {
	b, err := json.Marshal(apiwire.Body{Error: apiwire.Error{
		Category: apiwire.CategoryConflict, Code: "version_conflict", Message: "the setting changed; read it again",
		Details: map[string]any{"current_version": 7},
	}})
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{"error":{"category":"conflict","code":"version_conflict","message":"the setting changed; read it again","details":{"current_version":7}}}`
	if string(b) != golden {
		t.Errorf("body\n%s\nwant\n%s", b, golden)
	}
	b, _ = json.Marshal(apiwire.Body{Error: apiwire.Error{Category: apiwire.CategoryInternal, Code: "internal", Message: "x"}})
	if string(b) != `{"error":{"category":"internal","code":"internal","message":"x"}}` {
		t.Errorf("a body with no details: %s", b)
	}
}

// The status of the HTTP answer and the exit code of the command line for each
// category, written out by hand.
func TestStatusAndExitCodeOfEachCategory(t *testing.T) {
	for _, tc := range []struct {
		c      apiwire.Category
		status int
		exit   int
	}{
		{apiwire.CategoryInvalidArgument, http.StatusBadRequest, 3},
		{apiwire.CategoryUnauthenticated, http.StatusUnauthorized, 4},
		{apiwire.CategoryNotFound, http.StatusNotFound, 5},
		{apiwire.CategoryConflict, http.StatusConflict, 6},
		{apiwire.CategoryRefused, http.StatusUnprocessableEntity, 7},
		{apiwire.CategoryBackpressure, http.StatusTooManyRequests, 8},
		{apiwire.CategoryUnavailable, http.StatusServiceUnavailable, 9},
		{apiwire.CategoryInternal, http.StatusInternalServerError, 1},
	} {
		t.Run(string(tc.c), func(t *testing.T) {
			if got := tc.c.HTTPStatus(); got != tc.status {
				t.Errorf("status %d, want %d", got, tc.status)
			}
			if got := tc.c.ExitCode(); got != tc.exit {
				t.Errorf("exit code %d, want %d", got, tc.exit)
			}
		})
	}
	if apiwire.ExitUsage != 2 || apiwire.ExitOutcomeUnknown != 10 {
		t.Errorf("exit codes of the client: usage %d, outcome unknown %d; want 2 and 10", apiwire.ExitUsage, apiwire.ExitOutcomeUnknown)
	}
	// a category that is not one is an internal error, never a success
	if apiwire.Category("").ExitCode() != 1 || apiwire.Category("odd").HTTPStatus() != http.StatusInternalServerError {
		t.Error("a category that is none of the known ones was not treated as internal")
	}
}

// Every category has one exit code of its own, so a script tells them apart.
func TestNoTwoCategoriesShareAnExitCode(t *testing.T) {
	seen := map[int]apiwire.Category{}
	for _, c := range apiwire.Categories() {
		if prev, dup := seen[c.ExitCode()]; dup {
			t.Errorf("%s and %s share exit code %d", prev, c, c.ExitCode())
		}
		seen[c.ExitCode()] = c
		if c.ExitCode() == 0 {
			t.Errorf("%s exits with 0", c)
		}
	}
	if len(seen) != 8 {
		t.Errorf("%d categories, want 8", len(seen))
	}
}
