package adapterreg_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func TestPromptOutcome(t *testing.T) {
	tests := []struct {
		name  string
		stage adapterproto.PromptStage
		want  handoff.AttemptOutcome
		err   bool
	}{
		{"accepted counts as delivered", "accepted", handoff.AttemptDelivered, false},
		{"persisted counts as delivered", "persisted", handoff.AttemptDelivered, false},
		{"refused is a certain failure", "refused", handoff.AttemptFailed, false},
		{"no stage", "", 0, true},
		{"unknown stage", "sent", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := adapterreg.PromptOutcome(adapterproto.PromptDeliverResult{Stage: tc.stage})
			if tc.err {
				if !errors.Is(err, adapterreg.ErrUnmapped) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("PromptOutcome = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestPromptOutcomeOnError(t *testing.T) {
	codes := peer.Codes()
	for _, c := range adapterproto.ApplicationCodes() {
		codes = append(codes, c.Code)
	}
	for _, code := range codes {
		// Written out: only these codes leave the outcome open.
		want := handoff.AttemptFailed
		if code == -32603 || code == -32800 || code == 1005 {
			want = handoff.AttemptUnknown
		}
		if got := adapterreg.PromptOutcomeOnError(&peer.Error{Code: code}); got != want {
			t.Errorf("PromptOutcomeOnError(code %d) = %v, want %v", code, got, want)
		}
	}
	for name, tc := range map[string]struct {
		err  error
		want handoff.AttemptOutcome
	}{
		"refused before it was written": {peer.ErrOverloaded, handoff.AttemptFailed},
		"not sent":                      {peer.ErrMessageTooLarge, handoff.AttemptFailed},
		"wrapped error response":        {fmt.Errorf("call: %w", &peer.Error{Code: adapterproto.CodeUnknownAttempt}), handoff.AttemptFailed},
		"wrapped attempt conflict":      {fmt.Errorf("call: %w", &peer.Error{Code: adapterproto.CodeAttemptConflict}), handoff.AttemptUnknown},
		"closed connection":             {peer.ErrClosed, handoff.AttemptUnknown},
		"timeout":                       {context.DeadlineExceeded, handoff.AttemptUnknown},
		"malformed response":            {peer.ErrInvalidResponse, handoff.AttemptUnknown},
		"a code nobody defines":         {&peer.Error{Code: 4242}, handoff.AttemptUnknown},
		"an error of no known kind":     {errors.New("boom"), handoff.AttemptUnknown},
		"no error":                      {nil, handoff.AttemptUnknown},
	} {
		if got := adapterreg.PromptOutcomeOnError(tc.err); got != tc.want {
			t.Errorf("%s: PromptOutcomeOnError = %v, want %v", name, got, tc.want)
		}
	}
}

func TestReconcileOutcome(t *testing.T) {
	for verdict, want := range map[adapterproto.ReconcileVerdict]handoff.AttemptOutcome{
		"found": handoff.AttemptDelivered, "provably_absent": handoff.AttemptFailed, "indeterminate": handoff.AttemptUnknown,
	} {
		got, err := adapterreg.ReconcileOutcome(adapterproto.ReconcileResult{Verdict: verdict})
		if err != nil || got != want {
			t.Errorf("ReconcileOutcome(%s) = %v, %v, want %v", verdict, got, err, want)
		}
	}
	for _, bad := range []adapterproto.ReconcileVerdict{"", "maybe"} {
		if _, err := adapterreg.ReconcileOutcome(adapterproto.ReconcileResult{Verdict: bad}); !errors.Is(err, adapterreg.ErrUnmapped) {
			t.Errorf("ReconcileOutcome(%q) = %v", bad, err)
		}
	}
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func TestSignals(t *testing.T) {
	received := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	early := received.Add(-30 * time.Minute)
	late := received.Add(-5 * time.Minute)
	future := received.Add(time.Minute)
	offlineSince := received.Add(-2 * time.Minute)
	const ep, other = adapterproto.EndpointRef("e1"), adapterproto.EndpointRef("e2")
	av := func(c adapterproto.AvailabilityCondition, since time.Time, endpoints ...adapterproto.EndpointRef) adapterproto.Availability {
		a := adapterproto.Availability{Condition: c, Endpoints: endpoints}
		if !since.IsZero() {
			a.SinceMS = ms(since)
		}
		return a
	}
	tests := []struct {
		name       string
		conditions []adapterproto.Availability
		offline    bool
		want       handoff.Signals
	}{
		{"healthy", nil, false, handoff.Signals{}},
		{"runtime_auth is AuthInvalid", []adapterproto.Availability{av("runtime_auth", early)}, false, handoff.Signals{AuthInvalid: true, Since: early}},
		{"native_unavailable is ServiceUnavailable", []adapterproto.Availability{av("native_unavailable", early)}, false, handoff.Signals{ServiceUnavailable: true, Since: early}},
		{"dependency_missing is ServiceUnavailable", []adapterproto.Availability{av("dependency_missing", early)}, false, handoff.Signals{ServiceUnavailable: true, Since: early}},
		{"since is optional and then unknown", []adapterproto.Availability{av("runtime_auth", time.Time{})}, false, handoff.Signals{AuthInvalid: true}},
		{"since in the future is unknown", []adapterproto.Availability{av("runtime_auth", future)}, false, handoff.Signals{AuthInvalid: true}},
		{"since exactly at receipt is kept", []adapterproto.Availability{av("runtime_auth", received)}, false, handoff.Signals{AuthInvalid: true, Since: received}},
		{"a condition for another endpoint does not apply", []adapterproto.Availability{av("runtime_auth", early, other)}, false, handoff.Signals{}},
		{"a condition naming the endpoint applies", []adapterproto.Availability{av("runtime_auth", early, other, ep)}, false, handoff.Signals{AuthInvalid: true, Since: early}},
		{"auth wins over service for Since", []adapterproto.Availability{av("native_unavailable", early), av("runtime_auth", late)}, false,
			handoff.Signals{AuthInvalid: true, ServiceUnavailable: true, Since: late}},
		{"two service conditions began at the earlier", []adapterproto.Availability{av("native_unavailable", late), av("dependency_missing", early)}, false,
			handoff.Signals{ServiceUnavailable: true, Since: early}},
		{"two service conditions with one start unknown", []adapterproto.Availability{av("native_unavailable", late), av("dependency_missing", time.Time{})}, false,
			handoff.Signals{ServiceUnavailable: true}},
		{"two service conditions with one start in the future", []adapterproto.Availability{av("native_unavailable", late), av("dependency_missing", future)}, false,
			handoff.Signals{ServiceUnavailable: true}},
		{"offline wins and carries the daemon's own start", []adapterproto.Availability{av("runtime_auth", early)}, true,
			handoff.Signals{AdapterOffline: true, AuthInvalid: true, Since: offlineSince}},
		{"offline alone", nil, true, handoff.Signals{AdapterOffline: true, Since: offlineSince}},
		{"an auth condition does not leak into the other cause's start", []adapterproto.Availability{av("runtime_auth", time.Time{}), av("native_unavailable", early)}, false,
			handoff.Signals{AuthInvalid: true, ServiceUnavailable: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := adapterreg.Signals(tc.conditions, ep, received, tc.offline, offlineSince)
			if got != tc.want {
				t.Fatalf("Signals = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSignalsFeedAssessBlocked: the mapping's output means what handoff reads
// into it. An adapter that reported runtime_auth long enough is a blocked
// delivery caused by the runtime's authentication.
func TestSignalsFeedAssessBlocked(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	arrival := now.Add(-time.Hour)
	sig := adapterreg.Signals([]adapterproto.Availability{{Condition: "runtime_auth", SinceMS: now.Add(-20 * time.Minute).UnixMilli()}}, "e", now, false, time.Time{})
	v := handoff.AssessBlocked(handoff.BlockedInput{Signals: sig, OldestPendingAt: arrival}, handoff.DefaultBlockedParams(), now)
	if v.Status != handoff.BlockedNew || v.Cause != handoff.CauseAuthInvalid {
		t.Fatalf("verdict = %+v", v)
	}
	if !v.Since.Equal(now.Add(-20 * time.Minute)) {
		t.Fatalf("blocked since %v", v.Since)
	}
}

func TestStatusConditions(t *testing.T) {
	const ep, other = adapterproto.EndpointRef("e1"), adapterproto.EndpointRef("e2")
	av := func(cond adapterproto.AvailabilityCondition, endpoints ...adapterproto.EndpointRef) adapterproto.Availability {
		return adapterproto.Availability{Condition: cond, Endpoints: endpoints}
	}
	tests := []struct {
		name                                       string
		conditions                                 []adapterproto.Availability
		offline, rejected, controlUnk, versionUnte bool
		want                                       []adapterproto.ControlCondition
	}{
		{"nothing wrong", nil, false, false, false, false, nil},
		{"offline", nil, true, false, false, false, []adapterproto.ControlCondition{1}},
		{"runtime_auth", []adapterproto.Availability{av("runtime_auth")}, false, false, false, false, []adapterproto.ControlCondition{2}},
		{"native_unavailable", []adapterproto.Availability{av("native_unavailable")}, false, false, false, false, []adapterproto.ControlCondition{3}},
		{"dependency_missing shows as native_unavailable", []adapterproto.Availability{av("dependency_missing")}, false, false, false, false, []adapterproto.ControlCondition{3}},
		{"both of those once", []adapterproto.Availability{av("dependency_missing"), av("native_unavailable")}, false, false, false, false, []adapterproto.ControlCondition{3}},
		{"server requests rejected", nil, false, true, false, false, []adapterproto.ControlCondition{4}},
		{"control unknown", nil, false, false, true, false, []adapterproto.ControlCondition{5}},
		{"version untested is the daemon's verdict, not the snapshot's", nil, false, false, false, true, []adapterproto.ControlCondition{6}},
		{"a snapshot cannot carry it", []adapterproto.Availability{av("version_untested")}, false, false, false, false, nil},
		{"another endpoint's condition", []adapterproto.Availability{av("runtime_auth", other)}, false, false, false, false, nil},
		{"everything, ascending", []adapterproto.Availability{av("native_unavailable", ep), av("runtime_auth")}, true, true, true, true,
			[]adapterproto.ControlCondition{1, 2, 3, 4, 5, 6}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := adapterreg.StatusConditions(tc.conditions, ep, tc.offline, tc.rejected, tc.controlUnk, tc.versionUnte)
			if len(got) != len(tc.want) {
				t.Fatalf("StatusConditions = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("StatusConditions = %v, want %v", got, tc.want)
				}
			}
			// What it builds is a valid status reply.
			if err := (adapterproto.ControlStatusReply{Activity: adapterproto.ActivityIdle, AsOf: 1, Conditions: got}).Validate(); err != nil {
				t.Fatalf("not a valid reply: %v", err)
			}
		})
	}
}

func TestCreateReply(t *testing.T) {
	const ep = adapterproto.EndpointRef("session-endpoint")
	type c = adapterproto.ControlCreateReply
	tests := []struct {
		name      string
		in        adapterproto.SessionCreateResult
		want      c
		wantFinal bool
		err       bool
	}{
		{"created", adapterproto.SessionCreateResult{Outcome: "created"}, c{Outcome: 1, SessionEndpoint: ep}, true, false},
		{"preset_unknown", adapterproto.SessionCreateResult{Outcome: "failed", Reason: "preset_unknown"}, c{Outcome: 3, Reason: 1}, true, false},
		{"preset_invalid becomes preset_unavailable", adapterproto.SessionCreateResult{Outcome: "failed", Reason: "preset_invalid"}, c{Outcome: 3, Reason: 2}, true, false},
		{"runtime_auth", adapterproto.SessionCreateResult{Outcome: "failed", Reason: "runtime_auth"}, c{Outcome: 3, Reason: 4}, true, false},
		{"native_unavailable", adapterproto.SessionCreateResult{Outcome: "failed", Reason: "native_unavailable"}, c{Outcome: 3, Reason: 5}, true, false},
		{"dependency_missing becomes native_unavailable", adapterproto.SessionCreateResult{Outcome: "failed", Reason: "dependency_missing"}, c{Outcome: 3, Reason: 5}, true, false},
		{"native_error", adapterproto.SessionCreateResult{Outcome: "failed", Reason: "native_error"}, c{Outcome: 3, Reason: 6}, true, false},
		{"control_unknown", adapterproto.SessionCreateResult{Outcome: "failed", Reason: "control_unknown"}, c{Outcome: 3, Reason: 9}, true, false},
		{"indeterminate is not final", adapterproto.SessionCreateResult{Outcome: "indeterminate"}, c{}, false, false},
		{"a failure without a reason", adapterproto.SessionCreateResult{Outcome: "failed"}, c{}, false, true},
		{"an unknown reason", adapterproto.SessionCreateResult{Outcome: "failed", Reason: "adapter_offline"}, c{}, false, true},
		{"an unknown outcome", adapterproto.SessionCreateResult{Outcome: "pending"}, c{}, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, final, err := adapterreg.CreateReply(tc.in, ep)
			if tc.err {
				if !errors.Is(err, adapterreg.ErrUnmapped) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got != tc.want || final != tc.wantFinal {
				t.Fatalf("CreateReply = %+v, %v, %v, want %+v, %v", got, final, err, tc.want, tc.wantFinal)
			}
			if final {
				if err := got.Validate(); err != nil {
					t.Fatalf("not a valid reply: %v", err)
				}
			}
		})
	}
	if got := adapterreg.CreateUnconfirmed(ep, false); got != (c{Outcome: 2, SessionEndpoint: ep, Reason: 7}) || got.Validate() != nil {
		t.Errorf("CreateUnconfirmed = %+v", got)
	}
	if got := adapterreg.CreateUnconfirmed(ep, true); got != (c{Outcome: 2, SessionEndpoint: ep, Reason: 8}) || got.Validate() != nil {
		t.Errorf("CreateUnconfirmed(lost) = %+v", got)
	}
}

func TestInterruptReply(t *testing.T) {
	type c = adapterproto.ControlInterruptReply
	tests := []struct {
		name      string
		in        adapterproto.InterruptResult
		turnEnded bool
		want      c
		err       bool
	}{
		{"stopped", adapterproto.InterruptResult{Stage: "stopped"}, false, c{Outcome: 1}, false},
		{"requested and the turn ended in time", adapterproto.InterruptResult{Stage: "requested"}, true, c{Outcome: 1}, false},
		{"requested and the turn did not end in time", adapterproto.InterruptResult{Stage: "requested"}, false, c{Outcome: 2}, false},
		{"not running", adapterproto.InterruptResult{Stage: "not_running"}, false, c{Outcome: 3}, false},
		{"not running, whatever turn.ended says", adapterproto.InterruptResult{Stage: "not_running"}, true, c{Outcome: 3}, false},
		{"refused: native unreachable", adapterproto.InterruptResult{Stage: "refused", Reason: "native_unreachable"}, false, c{Outcome: 4, Reason: 2}, false},
		{"refused: native error", adapterproto.InterruptResult{Stage: "refused", Reason: "native_error"}, true, c{Outcome: 4, Reason: 3}, false},
		{"refused without a reason", adapterproto.InterruptResult{Stage: "refused"}, false, c{}, true},
		{"unknown stage", adapterproto.InterruptResult{Stage: "done"}, false, c{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := adapterreg.InterruptReply(tc.in, tc.turnEnded)
			if tc.err {
				if !errors.Is(err, adapterreg.ErrUnmapped) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got != tc.want || got.Validate() != nil {
				t.Fatalf("InterruptReply = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
}

func TestCloseReply(t *testing.T) {
	type c = adapterproto.ControlCloseReply
	for _, tc := range []struct {
		name string
		in   adapterproto.CloseResult
		want c
		err  bool
	}{
		{"closed", adapterproto.CloseResult{Outcome: "closed"}, c{Outcome: 1}, false},
		{"unconfirmed, turn still running", adapterproto.CloseResult{Outcome: "close_unconfirmed", Reason: "turn_still_running"}, c{Outcome: 2, Reason: 1}, false},
		{"unconfirmed, native unreachable", adapterproto.CloseResult{Outcome: "close_unconfirmed", Reason: "native_unreachable"}, c{Outcome: 2, Reason: 3}, false},
		{"unconfirmed without a reason", adapterproto.CloseResult{Outcome: "close_unconfirmed"}, c{}, true},
		{"unconfirmed with the daemon's own reason", adapterproto.CloseResult{Outcome: "close_unconfirmed", Reason: "adapter_offline"}, c{}, true},
		{"unknown outcome", adapterproto.CloseResult{Outcome: "gone"}, c{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := adapterreg.CloseReply(tc.in)
			if tc.err {
				if !errors.Is(err, adapterreg.ErrUnmapped) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got != tc.want || got.Validate() != nil {
				t.Fatalf("CloseReply = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
}

func TestItemAckAndAckReason(t *testing.T) {
	for v, want := range map[delivery.AckVerdict]adapterproto.ItemAck{
		delivery.AckRecorded: "recorded", delivery.AckCompleted: "completed", delivery.AckDuplicate: "duplicate",
		delivery.AckRefusedNoHandout: "refused_no_handout", delivery.AckRefusedLease: "refused_lease", delivery.AckTooLate: "too_late",
	} {
		if got, err := adapterreg.ItemAck(v); err != nil || got != want {
			t.Errorf("ItemAck(%d) = %q, %v, want %q", v, got, err, want)
		}
	}
	for _, v := range []delivery.AckVerdict{0, delivery.AckRefusedToken, 99} {
		if _, err := adapterreg.ItemAck(v); !errors.Is(err, adapterreg.ErrUnmapped) {
			t.Errorf("ItemAck(%d) = %v", v, err)
		}
	}
	for r, want := range map[handoff.AckReason]adapterproto.AckReason{
		handoff.AckSeenInFull: "seen_in_full", handoff.AckReplaced: "replaced", handoff.AckTruncated: "truncated", handoff.AckContentMismatch: "content_mismatch",
	} {
		if got, err := adapterreg.AckReason(r); err != nil || got != want {
			t.Errorf("AckReason(%d) = %q, %v, want %q", r, got, err, want)
		}
	}
	for _, r := range []handoff.AckReason{0, 99} {
		if _, err := adapterreg.AckReason(r); !errors.Is(err, adapterreg.ErrUnmapped) {
			t.Errorf("AckReason(%d) = %v", r, err)
		}
	}
}

func TestActionOf(t *testing.T) {
	for _, s := range []adapterproto.ControlSignal{"session.create", "session.close", "session.interrupt", "endpoint.status"} {
		a, err := adapterreg.ActionOf(s)
		if err != nil || a.String() != string(s) {
			t.Errorf("ActionOf(%s) = %v, %v", s, a, err)
		}
	}
	for _, s := range []adapterproto.ControlSignal{"", "message.send", "see", "nope"} {
		if _, err := adapterreg.ActionOf(s); !errors.Is(err, adapterreg.ErrUnmapped) {
			t.Errorf("ActionOf(%q) = %v", s, err)
		}
	}
	// Every action an endpoint can accept as a signal other than message.send
	// is a signal the protocol can carry.
	for _, a := range []authz.Action{authz.ActionSessionCreate, authz.ActionSessionClose, authz.ActionSessionInterrupt, authz.ActionEndpointStatus} {
		found := false
		for _, s := range []adapterproto.ControlSignal{"session.create", "session.close", "session.interrupt", "endpoint.status"} {
			if got, _ := adapterreg.ActionOf(s); got == a {
				found = true
			}
		}
		if !found {
			t.Errorf("no signal maps to %v", a)
		}
	}
}

func TestCheckHandshake(t *testing.T) {
	m := man()
	m.Protocol = adapterproto.Range{Min: 1, Max: 2}
	sent := adapterproto.Range{Min: 2, Max: 3}
	good := adapterproto.InitializeResult{ProtocolVersion: 2, Adapter: adapterproto.AdapterInfo{ID: "example", Version: "1.0.0"}}
	if err := adapterreg.CheckHandshake(sent, good, m); err != nil {
		t.Fatalf("CheckHandshake = %v", err)
	}
	for name, tc := range map[string]struct {
		mutate func(*adapterproto.InitializeResult)
		want   adapterreg.HandshakeReason
	}{
		"adapter id":                  {func(r *adapterproto.InitializeResult) { r.Adapter.ID = "other" }, adapterreg.HandshakeAdapterMismatch},
		"adapter version":             {func(r *adapterproto.InitializeResult) { r.Adapter.Version = "1.0.1" }, adapterreg.HandshakeVersionMismatch},
		"protocol below the request":  {func(r *adapterproto.InitializeResult) { r.ProtocolVersion = 1 }, adapterreg.HandshakeProtocolOutsideRequest},
		"protocol above the request":  {func(r *adapterproto.InitializeResult) { r.ProtocolVersion = 4 }, adapterreg.HandshakeProtocolOutsideRequest},
		"protocol above the manifest": {func(r *adapterproto.InitializeResult) { r.ProtocolVersion = 3 }, adapterreg.HandshakeProtocolOutsideManifest},
	} {
		t.Run(name, func(t *testing.T) {
			r := good
			tc.mutate(&r)
			var he *adapterreg.HandshakeError
			err := adapterreg.CheckHandshake(sent, r, m)
			if !errors.As(err, &he) || he.Reason != tc.want {
				t.Fatalf("CheckHandshake = %v, want %v", err, tc.want)
			}
		})
	}
	for r, want := range map[adapterreg.HandshakeReason]string{
		adapterreg.HandshakeAdapterMismatch: "adapter_mismatch", adapterreg.HandshakeVersionMismatch: "version_mismatch",
		adapterreg.HandshakeProtocolOutsideRequest: "protocol_outside_request", adapterreg.HandshakeProtocolOutsideManifest: "protocol_outside_manifest", 0: "invalid(0)",
	} {
		if r.String() != want {
			t.Errorf("%d: %q", r, r.String())
		}
	}
}

// TestRuntimeVersionUntested: the daemon alone judges a probed runtime version
// against the manifest's closed SemVer ranges. The expectations are written out
// by hand from the ranges, not computed with the comparison under test.
func TestRuntimeVersionUntested(t *testing.T) {
	one := manifest.Manifest{Tested: []manifest.TestedRange{{Runtime: "example-runtime", Min: "2.1.306", Max: "2.1.308"}}}
	two := manifest.Manifest{Tested: []manifest.TestedRange{
		{Runtime: "example-runtime", Min: "2.1.306", Max: "2.1.308"},
		{Runtime: "example-runtime", Min: "3.0.0", Max: "3.0.0"},
		{Runtime: "other-runtime", Min: "1.0.0", Max: "9.9.9"},
	}}
	rt := func(name, version string) *adapterproto.RuntimeInfo {
		return &adapterproto.RuntimeInfo{Name: name, Version: version}
	}
	tests := []struct {
		name     string
		m        manifest.Manifest
		runtime  *adapterproto.RuntimeInfo
		untested bool
	}{
		{"the lower bound is inside", one, rt("example-runtime", "2.1.306"), false},
		{"between the bounds is inside", one, rt("example-runtime", "2.1.307"), false},
		{"the upper bound is inside", one, rt("example-runtime", "2.1.308"), false},
		{"one below the range", one, rt("example-runtime", "2.1.305"), true},
		{"one above the range", one, rt("example-runtime", "2.1.309"), true},
		{"a later minor version", one, rt("example-runtime", "2.2.0"), true},
		{"numbers compare as numbers, not as text", one, rt("example-runtime", "2.1.1000"), true},
		{"a short patch number is below, not above", one, rt("example-runtime", "2.1.31"), true},
		{"a pre-release between the bounds is inside", one, rt("example-runtime", "2.1.307-rc.1"), false},
		{"a pre-release of the upper bound sorts below it", one, rt("example-runtime", "2.1.308-rc.1"), false},
		{"a pre-release of the lower bound sorts below it", one, rt("example-runtime", "2.1.306-rc.1"), true},
		{"build metadata is ignored", one, rt("example-runtime", "2.1.307+build.5"), false},
		{"a version that is not SemVer is never inside", one, rt("example-runtime", "latest"), true},
		{"another runtime's name does not match", one, rt("other-runtime", "2.1.307"), true},
		{"a second range of the same runtime", two, rt("example-runtime", "3.0.0"), false},
		{"between two ranges", two, rt("example-runtime", "2.5.0"), true},
		{"a range of another runtime is not this one's", two, rt("example-runtime", "5.0.0"), true},
		{"the other runtime's own range", two, rt("other-runtime", "5.0.0"), false},
		{"no tested range at all contains no version", manifest.Manifest{}, rt("example-runtime", "2.1.307"), true},
		{"no runtime probed makes no claim", one, nil, false},
		{"no runtime probed and no range makes no claim", manifest.Manifest{}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := adapterreg.RuntimeVersionUntested(tc.m, tc.runtime); got != tc.untested {
				t.Fatalf("RuntimeVersionUntested = %v, want %v", got, tc.untested)
			}
		})
	}
}

// An attempt_conflict answer to prompt.deliver says the attempt identifier is
// already known with other parameters: an earlier request of the attempt reached
// the adapter and may have delivered, so the attempt is unknown and reconciled,
// not failed and retried.
func TestPromptOutcomeOnAttemptConflictIsUnknown(t *testing.T) {
	err := &peer.Error{Code: adapterproto.CodeAttemptConflict, Message: "attempt conflict"}
	if !peer.NoEffect(err) {
		t.Fatal("the premise changed: the effect rule no longer counts attempt_conflict as no effect")
	}
	if got := adapterreg.PromptOutcomeOnError(err); got != handoff.AttemptUnknown {
		t.Errorf("PromptOutcomeOnError(attempt_conflict) = %v, want unknown", got)
	}
	// The neighbours in the same range stay failed: the request did nothing and
	// names no earlier attempt.
	for _, code := range []int{adapterproto.CodeUnknownAttempt, adapterproto.CodeUnknownEndpoint, adapterproto.CodeSendRefused} {
		if got := adapterreg.PromptOutcomeOnError(&peer.Error{Code: code}); got != handoff.AttemptFailed {
			t.Errorf("PromptOutcomeOnError(%d) = %v, want failed", code, got)
		}
	}
}

// Every failure reason a session.create result can carry has a reply reason, and
// the reply is valid. The expected numbers are written out here, not derived.
func TestCreateReplyMapsEveryCreateFailure(t *testing.T) {
	want := map[string]adapterproto.ControlCreateReason{
		"preset_unknown": 1, "preset_invalid": 2, "runtime_auth": 4, "native_unavailable": 5,
		"dependency_missing": 5, "native_error": 6, "control_unknown": 9,
	}
	listed := adapterproto.EnumTables()["CreateFailure"]
	if len(listed) != len(want) {
		t.Fatalf("the protocol has %d creation failures %v, the table %d", len(listed), listed, len(want))
	}
	for _, f := range listed {
		reason, ok := want[f]
		if !ok {
			t.Errorf("creation failure %q has no row", f)
			continue
		}
		got, final, err := adapterreg.CreateReply(adapterproto.SessionCreateResult{Outcome: "failed", Reason: adapterproto.CreateFailure(f)}, "e")
		if err != nil || !final || got.Outcome != adapterproto.CreateReplyFailed || got.Reason != reason || got.SessionEndpoint != "" || got.Validate() != nil {
			t.Errorf("%s: CreateReply = %+v, %v, %v, want reason %d", f, got, final, err, reason)
		}
	}
}

// A creation the daemon refuses itself, without calling the adapter, is a failed
// creation with no session endpoint: the daemon allocated none, and nothing was
// created. The wire values are written out by hand, from the protocol's reply
// table (outcome 3 failed; reasons 1 preset_unknown, 2 preset_unavailable,
// 3 adapter_offline), and a preset closed to local sources reuses
// preset_unavailable, as an adapter's preset_invalid does: the preset exists and
// cannot be used for this creation.
func TestDaemonCreateFailure(t *testing.T) {
	tests := []struct {
		name     string
		in       adapterreg.DaemonCreateReason
		outcome  uint8
		reason   uint8
		wireJSON string
	}{
		{"an unknown preset is preset_unknown", adapterreg.DaemonCreatePresetUnknown, 3, 1,
			`{"outcome":"failed","reason":"preset_unknown"}`},
		{"a preset closed to local sources is preset_unavailable", adapterreg.DaemonCreatePresetClosedToLocalSources, 3, 2,
			`{"outcome":"failed","reason":"preset_unavailable"}`},
		{"an adapter that is offline is adapter_offline", adapterreg.DaemonCreateAdapterOffline, 3, 3,
			`{"outcome":"failed","reason":"adapter_offline"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := adapterreg.DaemonCreateFailure(tc.in)
			if want := (adapterproto.ControlCreateReply{Outcome: adapterproto.ControlCreateOutcome(tc.outcome), Reason: adapterproto.ControlCreateReason(tc.reason)}); got != want {
				t.Fatalf("DaemonCreateFailure(%d) = %+v, want %+v", tc.in, got, want)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("not a valid reply: %v", err)
			}
			b, err := json.Marshal(got)
			if err != nil || string(b) != tc.wireJSON {
				t.Errorf("wire form %s (%v), want %s", b, err, tc.wireJSON)
			}
		})
	}
	t.Run("the reasons are distinct and none is the zero value", func(t *testing.T) {
		seen := map[adapterreg.DaemonCreateReason]bool{}
		for _, r := range []adapterreg.DaemonCreateReason{adapterreg.DaemonCreatePresetUnknown, adapterreg.DaemonCreatePresetClosedToLocalSources, adapterreg.DaemonCreateAdapterOffline} {
			if r == 0 || seen[r] {
				t.Errorf("reason %d is zero or repeated", r)
			}
			seen[r] = true
		}
	})
	t.Run("a value that is no reason gives a reply that is not valid, so it cannot be sent", func(t *testing.T) {
		for _, in := range []adapterreg.DaemonCreateReason{0, 4, 255} {
			if got := adapterreg.DaemonCreateFailure(in); got != (adapterproto.ControlCreateReply{}) || got.Validate() == nil {
				t.Errorf("DaemonCreateFailure(%d) = %+v, want the zero reply, which does not validate", in, got)
			}
		}
	})
}

func TestBlockedCause(t *testing.T) {
	for c, want := range map[handoff.Cause]delivery.Cause{
		handoff.CauseAdapterOffline: delivery.CauseAdapterOffline, handoff.CauseAuthInvalid: delivery.CauseRuntimeAuth,
		handoff.CauseServiceUnavailable: delivery.CauseNativeUnavailable,
	} {
		if got, err := adapterreg.BlockedCause(c); err != nil || got != want {
			t.Errorf("BlockedCause(%d) = %v, %v, want %v", c, got, err, want)
		}
		if back, err := adapterreg.HandoffBlockedCause(want); err != nil || back != c {
			t.Errorf("HandoffBlockedCause(%v) = %v, %v, want %v", want, back, err, c)
		}
	}
	for _, c := range []delivery.Cause{0, delivery.CauseNetwork} {
		if _, err := adapterreg.HandoffBlockedCause(c); !errors.Is(err, adapterreg.ErrUnmapped) {
			t.Errorf("HandoffBlockedCause(%v) = %v", c, err)
		}
	}
	for _, c := range []handoff.Cause{0, 99} {
		if _, err := adapterreg.BlockedCause(c); !errors.Is(err, adapterreg.ErrUnmapped) {
			t.Errorf("BlockedCause(%d) = %v", c, err)
		}
	}
}
