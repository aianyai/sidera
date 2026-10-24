package adapterproto_test

import (
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestSessionCreateParamsRules(t *testing.T) {
	method(t, func() adapterproto.SessionCreateParams {
		return adapterproto.SessionCreateParams{Attempt: attempt1, RuntimeEndpoint: epRT, SessionEndpoint: ep1, Preset: "default",
			FirstPrompt: adapterproto.FirstPrompt{Attempt: attempt2, Text: "You have 1 new message."}, ToolAccess: toolAccess(), Checkpoint: raw(`{"phase":"spawned"}`)}
	}, []row[adapterproto.SessionCreateParams]{
		{"attempt must be a UUID", func(p *adapterproto.SessionCreateParams) { p.Attempt = "not-a-uuid" }, "attempt", false},
		{"attempt must be lowercase", func(p *adapterproto.SessionCreateParams) { p.Attempt = "6F1C2A4E-8B3D-4C5A-9E7F-0A1B2C3D4E5F" }, "attempt", false},
		{"attempt must be version 4", func(p *adapterproto.SessionCreateParams) { p.Attempt = "6f1c2a4e-8b3d-1c5a-9e7f-0a1b2c3d4e5f" }, "attempt", false},
		{"attempt variant 8", func(p *adapterproto.SessionCreateParams) { p.Attempt = "6f1c2a4e-8b3d-4c5a-8e7f-0a1b2c3d4e5f" }, "", false},
		{"attempt variant a", func(p *adapterproto.SessionCreateParams) { p.Attempt = "6f1c2a4e-8b3d-4c5a-ae7f-0a1b2c3d4e5f" }, "", false},
		{"attempt variant b", func(p *adapterproto.SessionCreateParams) { p.Attempt = "6f1c2a4e-8b3d-4c5a-be7f-0a1b2c3d4e5f" }, "", false},
		{"attempt variant c is not RFC 4122", func(p *adapterproto.SessionCreateParams) { p.Attempt = "6f1c2a4e-8b3d-4c5a-ce7f-0a1b2c3d4e5f" }, "attempt", false},
		{"attempt without dashes", func(p *adapterproto.SessionCreateParams) { p.Attempt = "6f1c2a4e8b3d4c5a9e7f0a1b2c3d4e5f" }, "attempt", false},
		{"attempt with trailing newline", func(p *adapterproto.SessionCreateParams) { p.Attempt = attempt1 + "\n" }, "attempt", false},
		{"runtime endpoint required", func(p *adapterproto.SessionCreateParams) { p.RuntimeEndpoint = "" }, "runtime_endpoint", false},
		{"session endpoint required", func(p *adapterproto.SessionCreateParams) { p.SessionEndpoint = "" }, "session_endpoint", false},
		{"session endpoint of 128", func(p *adapterproto.SessionCreateParams) {
			p.SessionEndpoint = adapterproto.EndpointRef(repeat("e", 128))
		}, "", false},
		{"session endpoint of 129", func(p *adapterproto.SessionCreateParams) {
			p.SessionEndpoint = adapterproto.EndpointRef(repeat("e", 129))
		}, "session_endpoint", false},
		{"preset required", func(p *adapterproto.SessionCreateParams) { p.Preset = "" }, "preset", false},
		{"preset starting with a dot", func(p *adapterproto.SessionCreateParams) { p.Preset = ".x" }, "preset", false},
		{"first prompt attempt validated", func(p *adapterproto.SessionCreateParams) { p.FirstPrompt.Attempt = "x" }, "first_prompt.attempt", false},
		{"first prompt text required", func(p *adapterproto.SessionCreateParams) { p.FirstPrompt.Text = "" }, "first_prompt.text", false},
		{"first prompt text at the limit", func(p *adapterproto.SessionCreateParams) { p.FirstPrompt.Text = repeat("t", 8192) }, "", false},
		{"first prompt text over the limit", func(p *adapterproto.SessionCreateParams) { p.FirstPrompt.Text = repeat("t", 8193) }, "first_prompt.text", false},
		{"tool access validated", func(p *adapterproto.SessionCreateParams) { p.ToolAccess.Env = nil }, "tool_access", false},
		{"checkpoint optional", func(p *adapterproto.SessionCreateParams) { p.Checkpoint = nil }, "", false},
		{"checkpoint must be an object", func(p *adapterproto.SessionCreateParams) { p.Checkpoint = raw(`"x"`) }, "checkpoint", false},
	})
}

func TestSessionCreateResultRules(t *testing.T) {
	created := func() adapterproto.SessionCreateResult {
		return adapterproto.SessionCreateResult{Outcome: "created", NativeRef: "sess_1", Process: proc(), Frontends: []adapterproto.Frontend{"cli", "mcp"}, Detail: "ok"}
	}
	failed := func() adapterproto.SessionCreateResult {
		return adapterproto.SessionCreateResult{Outcome: "failed", Reason: "native_error", Detail: "boom"}
	}
	undecided := func() adapterproto.SessionCreateResult {
		return adapterproto.SessionCreateResult{Outcome: "indeterminate", NativeRef: "sess_1", Process: proc(), Grounds: &adapterproto.Grounds{Code: "record_unreadable", Detail: "locked"}}
	}
	t.Run("created", func(t *testing.T) {
		method(t, created, []row[adapterproto.SessionCreateResult]{
			{"native ref required", func(r *adapterproto.SessionCreateResult) { r.NativeRef = "" }, "native_ref", false},
			{"frontends required", func(r *adapterproto.SessionCreateResult) { r.Frontends = nil }, "frontends", false},
			{"frontends repeat", func(r *adapterproto.SessionCreateResult) { r.Frontends = []adapterproto.Frontend{"cli", "cli"} }, "frontends", false},
			{"frontend unknown", func(r *adapterproto.SessionCreateResult) { r.Frontends = []adapterproto.Frontend{"web"} }, "frontends[0]", false},
			{"process optional", func(r *adapterproto.SessionCreateResult) { r.Process = nil }, "", false},
			{"process validated", func(r *adapterproto.SessionCreateResult) { r.Process.PID = 0 }, "process.pid", false},
			{"reason forbidden", func(r *adapterproto.SessionCreateResult) { r.Reason = "native_error" }, "reason", false},
			{"grounds forbidden", func(r *adapterproto.SessionCreateResult) { r.Grounds = &adapterproto.Grounds{Code: "turn_running"} }, "grounds", false},
			{"detail over the limit", func(r *adapterproto.SessionCreateResult) { r.Detail = repeat("d", 1025) }, "detail", false},
		})
	})
	t.Run("failed", func(t *testing.T) {
		method(t, failed, []row[adapterproto.SessionCreateResult]{
			{"reason required", func(r *adapterproto.SessionCreateResult) { r.Reason = "" }, "reason", false},
			{"reason unknown", func(r *adapterproto.SessionCreateResult) { r.Reason = "adapter_offline" }, "reason", false},
			{"reason control_unknown", func(r *adapterproto.SessionCreateResult) { r.Reason = "control_unknown" }, "", false},
			{"native ref forbidden", func(r *adapterproto.SessionCreateResult) { r.NativeRef = "s" }, "no session to describe", false},
			{"process forbidden", func(r *adapterproto.SessionCreateResult) { r.Process = proc() }, "no session to describe", false},
			{"frontends forbidden", func(r *adapterproto.SessionCreateResult) { r.Frontends = []adapterproto.Frontend{"cli"} }, "no session to describe", false},
			{"grounds forbidden", func(r *adapterproto.SessionCreateResult) { r.Grounds = &adapterproto.Grounds{Code: "turn_running"} }, "grounds", false},
		})
	})
	t.Run("indeterminate", func(t *testing.T) {
		method(t, undecided, []row[adapterproto.SessionCreateResult]{
			{"grounds required", func(r *adapterproto.SessionCreateResult) { r.Grounds = nil }, "grounds", false},
			{"grounds turn_running", func(r *adapterproto.SessionCreateResult) { r.Grounds.Code = "turn_running" }, "", false},
			{"grounds native_unreachable", func(r *adapterproto.SessionCreateResult) { r.Grounds.Code = "native_unreachable" }, "", false},
			{"grounds runtime_version_untested", func(r *adapterproto.SessionCreateResult) { r.Grounds.Code = "runtime_version_untested" }, "", false},
			{"grounds of a found attempt", func(r *adapterproto.SessionCreateResult) { r.Grounds.Code = "attempt_in_record" }, "grounds.code", false},
			{"grounds of an absent attempt", func(r *adapterproto.SessionCreateResult) { r.Grounds.Code = "never_dispatched" }, "grounds.code", false},
			{"grounds code unknown", func(r *adapterproto.SessionCreateResult) { r.Grounds.Code = "who_knows" }, "grounds.code", false},
			{"reason forbidden", func(r *adapterproto.SessionCreateResult) { r.Reason = "native_error" }, "reason", false},
			{"reason control_unknown forbidden", func(r *adapterproto.SessionCreateResult) { r.Reason = "control_unknown" }, "reason", false},
			{"frontends forbidden", func(r *adapterproto.SessionCreateResult) { r.Frontends = []adapterproto.Frontend{"cli"} }, "frontends", false},
			{"native ref optional", func(r *adapterproto.SessionCreateResult) { r.NativeRef = ""; r.Process = nil }, "", false},
		})
	})
	t.Run("outcome", func(t *testing.T) {
		method(t, created, []row[adapterproto.SessionCreateResult]{
			{"outcome required", func(r *adapterproto.SessionCreateResult) { r.Outcome = "" }, "outcome", false},
			{"outcome unknown", func(r *adapterproto.SessionCreateResult) { r.Outcome = "pending" }, "outcome", false},
		})
	})
	for _, f := range []adapterproto.CreateFailure{"preset_unknown", "preset_invalid", "runtime_auth", "native_unavailable", "dependency_missing", "native_error", "control_unknown"} {
		if err := (adapterproto.SessionCreateResult{Outcome: "failed", Reason: f}).Validate(); err != nil {
			t.Errorf("failure %s: %v", f, err)
		}
	}
}

func TestCheckpointPutRules(t *testing.T) {
	method(t, func() adapterproto.CheckpointPutParams {
		return adapterproto.CheckpointPutParams{Scope: "attempt", ID: attempt1, State: raw(`{"phase":"spawned"}`)}
	}, []row[adapterproto.CheckpointPutParams]{
		{"session scope with an endpoint", func(p *adapterproto.CheckpointPutParams) { p.Scope = "session"; p.ID = ep1 }, "", false},
		{"session scope with a UUID is fine, it is an opaque reference", func(p *adapterproto.CheckpointPutParams) { p.Scope = "session" }, "", false},
		{"attempt scope needs an attempt id", func(p *adapterproto.CheckpointPutParams) { p.ID = ep1 }, "id", false},
		{"session scope id required", func(p *adapterproto.CheckpointPutParams) { p.Scope = "session"; p.ID = "" }, "id", false},
		{"session scope id over the limit", func(p *adapterproto.CheckpointPutParams) { p.Scope = "session"; p.ID = repeat("e", 129) }, "id", false},
		{"scope required", func(p *adapterproto.CheckpointPutParams) { p.Scope = "" }, "scope", false},
		{"scope unknown", func(p *adapterproto.CheckpointPutParams) { p.Scope = "global" }, "scope", false},
		{"state required", func(p *adapterproto.CheckpointPutParams) { p.State = nil }, "state", false},
		{"state must be an object", func(p *adapterproto.CheckpointPutParams) { p.State = raw(`[1]`) }, "state", false},
		{"state null", func(p *adapterproto.CheckpointPutParams) { p.State = raw(`null`) }, "state", false},
		{"state empty object", func(p *adapterproto.CheckpointPutParams) { p.State = raw(`{}`) }, "", false},
		{"state not JSON", func(p *adapterproto.CheckpointPutParams) { p.State = raw(`{"a":`) }, "state", false},
		{"state of exactly 16384 bytes", func(p *adapterproto.CheckpointPutParams) { p.State = raw(`{"b":"` + repeat("a", 16384-8) + `"}`) }, "", false},
		{"state of 16385 bytes", func(p *adapterproto.CheckpointPutParams) { p.State = raw(`{"b":"` + repeat("a", 16384-7) + `"}`) }, "state", true},
	})
}

func TestInterruptAndCloseRules(t *testing.T) {
	method(t, func() adapterproto.InterruptParams { return adapterproto.InterruptParams{Endpoint: ep1} },
		[]row[adapterproto.InterruptParams]{
			{"endpoint required", func(p *adapterproto.InterruptParams) { p.Endpoint = "" }, "endpoint", false},
		})
	method(t, func() adapterproto.CloseParams { return adapterproto.CloseParams{Endpoint: ep1} },
		[]row[adapterproto.CloseParams]{
			{"endpoint required", func(p *adapterproto.CloseParams) { p.Endpoint = "" }, "endpoint", false},
			{"endpoint over the limit", func(p *adapterproto.CloseParams) { p.Endpoint = adapterproto.EndpointRef(repeat("e", 129)) }, "endpoint", false},
		})
	method(t, func() adapterproto.InterruptResult {
		return adapterproto.InterruptResult{Stage: "refused", Reason: "native_error"}
	},
		[]row[adapterproto.InterruptResult]{
			{"refusal unreachable", func(r *adapterproto.InterruptResult) { r.Reason = "native_unreachable" }, "", false},
			{"refusal needs a reason", func(r *adapterproto.InterruptResult) { r.Reason = "" }, "reason", false},
			{"refusal reason unknown", func(r *adapterproto.InterruptResult) { r.Reason = "busy" }, "reason", false},
			{"requested", func(r *adapterproto.InterruptResult) { r.Stage = "requested"; r.Reason = "" }, "", false},
			{"stopped", func(r *adapterproto.InterruptResult) { r.Stage = "stopped"; r.Reason = "" }, "", false},
			{"not running", func(r *adapterproto.InterruptResult) { r.Stage = "not_running"; r.Reason = "" }, "", false},
			{"requested with a reason", func(r *adapterproto.InterruptResult) { r.Stage = "requested" }, "reason", false},
			{"stage required", func(r *adapterproto.InterruptResult) { r.Stage = "" }, "stage", false},
			{"stage unknown", func(r *adapterproto.InterruptResult) { r.Stage = "done" }, "stage", false},
		})
	method(t, func() adapterproto.CloseResult {
		return adapterproto.CloseResult{Outcome: "close_unconfirmed", Reason: "turn_still_running"}
	},
		[]row[adapterproto.CloseResult]{
			{"unconfirmed because unreachable", func(r *adapterproto.CloseResult) { r.Reason = "native_unreachable" }, "", false},
			{"unconfirmed needs a reason", func(r *adapterproto.CloseResult) { r.Reason = "" }, "reason", false},
			{"unconfirmed reason unknown", func(r *adapterproto.CloseResult) { r.Reason = "adapter_offline" }, "reason", false},
			{"closed", func(r *adapterproto.CloseResult) { r.Outcome = "closed"; r.Reason = "" }, "", false},
			{"closed with a reason", func(r *adapterproto.CloseResult) { r.Outcome = "closed" }, "reason", false},
			{"unsupported is the daemon's own outcome", func(r *adapterproto.CloseResult) { r.Outcome = "unsupported"; r.Reason = "" }, "outcome", false},
			{"outcome required", func(r *adapterproto.CloseResult) { r.Outcome = "" }, "outcome", false},
		})
}

func TestPromptDeliverRules(t *testing.T) {
	method(t, func() adapterproto.PromptDeliverParams {
		return adapterproto.PromptDeliverParams{Endpoint: ep1, Attempt: attempt1, Text: "You have 2 new messages."}
	}, []row[adapterproto.PromptDeliverParams]{
		{"endpoint required", func(p *adapterproto.PromptDeliverParams) { p.Endpoint = "" }, "endpoint", false},
		{"attempt required", func(p *adapterproto.PromptDeliverParams) { p.Attempt = "" }, "attempt", false},
		{"text required", func(p *adapterproto.PromptDeliverParams) { p.Text = "" }, "text", false},
		{"text at the limit", func(p *adapterproto.PromptDeliverParams) { p.Text = repeat("é", 8192) }, "", false},
		{"text over the limit", func(p *adapterproto.PromptDeliverParams) { p.Text = repeat("é", 8193) }, "text", false},
	})
	method(t, func() adapterproto.PromptDeliverResult {
		return adapterproto.PromptDeliverResult{Stage: "refused", Reason: "busy"}
	},
		[]row[adapterproto.PromptDeliverResult]{
			{"refusal runtime_auth", func(r *adapterproto.PromptDeliverResult) { r.Reason = "runtime_auth" }, "", false},
			{"refusal native_unavailable", func(r *adapterproto.PromptDeliverResult) { r.Reason = "native_unavailable" }, "", false},
			{"refusal session_lost", func(r *adapterproto.PromptDeliverResult) { r.Reason = "session_lost" }, "", false},
			{"refusal control_unknown", func(r *adapterproto.PromptDeliverResult) { r.Reason = "control_unknown" }, "", false},
			{"refusal needs a reason", func(r *adapterproto.PromptDeliverResult) { r.Reason = "" }, "reason", false},
			{"refusal reason unknown", func(r *adapterproto.PromptDeliverResult) { r.Reason = "tired" }, "reason", false},
			{"accepted", func(r *adapterproto.PromptDeliverResult) { r.Stage = "accepted"; r.Reason = "" }, "", false},
			{"persisted", func(r *adapterproto.PromptDeliverResult) { r.Stage = "persisted"; r.Reason = "" }, "", false},
			{"accepted with a reason", func(r *adapterproto.PromptDeliverResult) { r.Stage = "accepted" }, "reason", false},
			{"persisted with a reason", func(r *adapterproto.PromptDeliverResult) { r.Stage = "persisted" }, "reason", false},
			{"stage required", func(r *adapterproto.PromptDeliverResult) { r.Stage = "" }, "stage", false},
			{"stage unknown", func(r *adapterproto.PromptDeliverResult) { r.Stage = "sent" }, "stage", false},
		})
	method(t, func() adapterproto.PromptPersistedParams {
		return adapterproto.PromptPersistedParams{Endpoint: ep1, Attempt: attempt1}
	},
		[]row[adapterproto.PromptPersistedParams]{
			{"endpoint required", func(p *adapterproto.PromptPersistedParams) { p.Endpoint = "" }, "endpoint", false},
			{"attempt required", func(p *adapterproto.PromptPersistedParams) { p.Attempt = "" }, "attempt", false},
		})
}

func TestTurnRules(t *testing.T) {
	method(t, func() adapterproto.TurnStartedParams {
		return adapterproto.TurnStartedParams{Endpoint: ep1, Turn: "turn_1", Attempt: attempt1, Process: proc(), AtMS: 1792713600000}
	}, []row[adapterproto.TurnStartedParams]{
		{"endpoint required", func(p *adapterproto.TurnStartedParams) { p.Endpoint = "" }, "endpoint", false},
		{"turn required", func(p *adapterproto.TurnStartedParams) { p.Turn = "" }, "turn", false},
		{"turn at the limit", func(p *adapterproto.TurnStartedParams) { p.Turn = adapterproto.TurnRef(repeat("t", 256)) }, "", false},
		{"turn over the limit", func(p *adapterproto.TurnStartedParams) { p.Turn = adapterproto.TurnRef(repeat("t", 257)) }, "turn", false},
		{"attempt optional, for a turn nobody prompted", func(p *adapterproto.TurnStartedParams) { p.Attempt = "" }, "", false},
		{"attempt validated", func(p *adapterproto.TurnStartedParams) { p.Attempt = "x" }, "attempt", false},
		{"process optional", func(p *adapterproto.TurnStartedParams) { p.Process = nil }, "", false},
		{"process validated", func(p *adapterproto.TurnStartedParams) { p.Process.StartKey = "" }, "process.start_key", false},
		{"time required", func(p *adapterproto.TurnStartedParams) { p.AtMS = 0 }, "at_ms", false},
		{"time negative", func(p *adapterproto.TurnStartedParams) { p.AtMS = -5 }, "at_ms", false},
		{"time at the exact bound", func(p *adapterproto.TurnStartedParams) { p.AtMS = 1<<53 - 1 }, "", false},
		{"time over the exact bound", func(p *adapterproto.TurnStartedParams) { p.AtMS = 1 << 53 }, "at_ms", false},
	})
	method(t, func() adapterproto.TurnEndedParams {
		return adapterproto.TurnEndedParams{Endpoint: ep1, Turn: "turn_1", Final: "completed", AtMS: 1792713600000}
	}, []row[adapterproto.TurnEndedParams]{
		{"final interrupted", func(p *adapterproto.TurnEndedParams) { p.Final = "interrupted" }, "", false},
		{"final failed", func(p *adapterproto.TurnEndedParams) { p.Final = "failed" }, "", false},
		{"final unknown", func(p *adapterproto.TurnEndedParams) { p.Final = "aborted" }, "final", false},
		{"final required", func(p *adapterproto.TurnEndedParams) { p.Final = "" }, "final", false},
		{"endpoint required", func(p *adapterproto.TurnEndedParams) { p.Endpoint = "" }, "endpoint", false},
		{"turn required", func(p *adapterproto.TurnEndedParams) { p.Turn = "" }, "turn", false},
		{"time required", func(p *adapterproto.TurnEndedParams) { p.AtMS = 0 }, "at_ms", false},
	})
}

func TestReconcileRules(t *testing.T) {
	method(t, func() adapterproto.ReconcileParams {
		return adapterproto.ReconcileParams{Endpoint: ep1, Attempt: attempt1, Checkpoint: raw(`{"dispatched":false}`)}
	}, []row[adapterproto.ReconcileParams]{
		{"endpoint required", func(p *adapterproto.ReconcileParams) { p.Endpoint = "" }, "endpoint", false},
		{"attempt required", func(p *adapterproto.ReconcileParams) { p.Attempt = "" }, "attempt", false},
		{"checkpoint optional", func(p *adapterproto.ReconcileParams) { p.Checkpoint = nil }, "", false},
		{"checkpoint must be an object", func(p *adapterproto.ReconcileParams) { p.Checkpoint = raw(`5`) }, "checkpoint", false},
	})
	found := func() adapterproto.ReconcileResult {
		return adapterproto.ReconcileResult{Verdict: "found", Turn: &adapterproto.TurnReport{Ref: "turn_1", State: "running"}, Grounds: adapterproto.Grounds{Code: "attempt_in_record"}}
	}
	t.Run("found", func(t *testing.T) {
		method(t, found, []row[adapterproto.ReconcileResult]{
			{"turn completed", func(r *adapterproto.ReconcileResult) { r.Turn.State = "completed" }, "", false},
			{"turn interrupted", func(r *adapterproto.ReconcileResult) { r.Turn.State = "interrupted" }, "", false},
			{"turn failed", func(r *adapterproto.ReconcileResult) { r.Turn.State = "failed" }, "", false},
			{"turn state unknown", func(r *adapterproto.ReconcileResult) { r.Turn.State = "paused" }, "turn.state", false},
			{"turn ref required", func(r *adapterproto.ReconcileResult) { r.Turn.Ref = "" }, "turn.ref", false},
			{"turn required", func(r *adapterproto.ReconcileResult) { r.Turn = nil }, "turn", false},
			{"grounds must say the attempt is in the record", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "never_dispatched" }, "grounds.code", false},
			{"grounds detail over the limit", func(r *adapterproto.ReconcileResult) { r.Grounds.Detail = repeat("d", 1025) }, "grounds.detail", false},
		})
	})
	t.Run("provably absent", func(t *testing.T) {
		method(t, func() adapterproto.ReconcileResult {
			return adapterproto.ReconcileResult{Verdict: "provably_absent", Grounds: adapterproto.Grounds{Code: "never_dispatched"}}
		}, []row[adapterproto.ReconcileResult]{
			{"record scanned", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "record_complete_without_attempt" }, "", false},
			{"turn forbidden", func(r *adapterproto.ReconcileResult) { r.Turn = &adapterproto.TurnReport{Ref: "t", State: "failed"} }, "turn", false},
			{"grounds of a found attempt", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "attempt_in_record" }, "grounds.code", false},
			{"grounds of an undecided attempt", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "turn_running" }, "grounds.code", false},
		})
	})
	t.Run("indeterminate", func(t *testing.T) {
		method(t, func() adapterproto.ReconcileResult {
			return adapterproto.ReconcileResult{Verdict: "indeterminate", Grounds: adapterproto.Grounds{Code: "turn_running"}}
		}, []row[adapterproto.ReconcileResult]{
			{"record unreadable", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "record_unreadable" }, "", false},
			{"native unreachable", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "native_unreachable" }, "", false},
			{"runtime version untested", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "runtime_version_untested" }, "", false},
			{"turn forbidden", func(r *adapterproto.ReconcileResult) { r.Turn = &adapterproto.TurnReport{Ref: "t", State: "running"} }, "turn", false},
			{"grounds of a found attempt", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "attempt_in_record" }, "grounds.code", false},
			{"grounds of an absent attempt", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "never_dispatched" }, "grounds.code", false},
			{"grounds code required", func(r *adapterproto.ReconcileResult) { r.Grounds.Code = "" }, "grounds.code", false},
			{"verdict required", func(r *adapterproto.ReconcileResult) { r.Verdict = "" }, "verdict", false},
			{"verdict unknown", func(r *adapterproto.ReconcileResult) { r.Verdict = "maybe" }, "verdict", false},
		})
	})
}

func TestNativeRequestHandledRules(t *testing.T) {
	method(t, func() adapterproto.NativeRequestHandledParams {
		return adapterproto.NativeRequestHandledParams{Endpoint: ep1, Method: "item/commandExecution/requestApproval", Disposition: "preset", AtMS: 1792713612000}
	}, []row[adapterproto.NativeRequestHandledParams]{
		{"endpoint required", func(p *adapterproto.NativeRequestHandledParams) { p.Endpoint = "" }, "endpoint", false},
		{"method required", func(p *adapterproto.NativeRequestHandledParams) { p.Method = "" }, "method", false},
		{"method at the limit", func(p *adapterproto.NativeRequestHandledParams) { p.Method = repeat("m", 128) }, "", false},
		{"method over the limit", func(p *adapterproto.NativeRequestHandledParams) { p.Method = repeat("m", 129) }, "method", false},
		{"disposition rejected by preset", func(p *adapterproto.NativeRequestHandledParams) { p.Disposition = "rejected_by_preset" }, "", false},
		{"disposition rejected unknown", func(p *adapterproto.NativeRequestHandledParams) { p.Disposition = "rejected_unknown" }, "", false},
		{"disposition late", func(p *adapterproto.NativeRequestHandledParams) { p.Disposition = "late" }, "", false},
		{"disposition unknown", func(p *adapterproto.NativeRequestHandledParams) { p.Disposition = "ignored" }, "disposition", false},
		{"disposition required", func(p *adapterproto.NativeRequestHandledParams) { p.Disposition = "" }, "disposition", false},
		{"time required", func(p *adapterproto.NativeRequestHandledParams) { p.AtMS = 0 }, "at_ms", false},
	})
}
