package serverreq

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestHandle(t *testing.T) {
	denied := map[string]any{"decision": map[string]any{"denied": map[string]any{"rejection": "the preset does not approve"}}}
	for _, tc := range []struct {
		method   string
		approves bool
		reply    any // nil when an error is expected
		errCode  int
		disp     adapterproto.Disposition
	}{
		{MethodCommandApproval, true, map[string]any{"decision": "accept"}, 0, adapterproto.DispositionPreset},
		{MethodCommandApproval, false, map[string]any{"decision": "decline"}, 0, adapterproto.DispositionRejectedByPreset},
		{MethodFileChangeApproval, true, map[string]any{"decision": "accept"}, 0, adapterproto.DispositionPreset},
		{MethodFileChangeApproval, false, map[string]any{"decision": "decline"}, 0, adapterproto.DispositionRejectedByPreset},
		{MethodApplyPatchLegacy, true, map[string]any{"decision": "approved"}, 0, adapterproto.DispositionPreset},
		{MethodApplyPatchLegacy, false, denied, 0, adapterproto.DispositionRejectedByPreset},
		{MethodExecCommandLegacy, true, map[string]any{"decision": "approved"}, 0, adapterproto.DispositionPreset},
		{MethodExecCommandLegacy, false, denied, 0, adapterproto.DispositionRejectedByPreset},
		// An approving preset grants no more than the sandbox gives.
		{MethodPermissionsRequest, true, map[string]any{"permissions": map[string]any{}}, 0, adapterproto.DispositionRejectedByPreset},
		{MethodElicitation, true, map[string]any{"action": "decline"}, 0, adapterproto.DispositionRejectedByPreset},
		{MethodUserInput, true, nil, CodeRefused, adapterproto.DispositionRejectedByPreset},
		{MethodDynamicToolCall, true, nil, CodeRefused, adapterproto.DispositionRejectedByPreset},
		{MethodAuthTokensRefresh, true, nil, CodeRefused, adapterproto.DispositionRejectedByPreset},
		{MethodAttestation, true, nil, CodeRefused, adapterproto.DispositionRejectedByPreset},
		{"currentTime/read", true, nil, CodeMethodNotFound, adapterproto.DispositionRejectedUnknown},
		{"", false, nil, CodeMethodNotFound, adapterproto.DispositionRejectedUnknown},
	} {
		got := Handle(tc.method, tc.approves)
		if got.Disposition != tc.disp {
			t.Errorf("%s approves=%v: disposition %s, want %s", tc.method, tc.approves, got.Disposition, tc.disp)
		}
		switch {
		case tc.reply != nil && (got.Error != nil || !reflect.DeepEqual(got.Reply, tc.reply)):
			t.Errorf("%s approves=%v: reply %v / error %v, want %v", tc.method, tc.approves, got.Reply, got.Error, tc.reply)
		case tc.reply == nil && (got.Error == nil || got.Error.Code != tc.errCode || got.Reply != nil):
			t.Errorf("%s approves=%v: reply %v / error %v, want error %d", tc.method, tc.approves, got.Reply, got.Error, tc.errCode)
		}
	}
}

func TestEveryKnownMethodIsAnsweredAsKnown(t *testing.T) {
	for _, m := range Known() {
		if Handle(m, true).Disposition == adapterproto.DispositionRejectedUnknown {
			t.Errorf("%s is listed as known and answered as unknown", m)
		}
	}
	if !slices.Contains(Known(), MethodCommandApproval) {
		t.Error("Known lacks the command approval")
	}
}

func TestThreadID(t *testing.T) {
	for in, want := range map[string]string{
		`{"threadId":"t1","turnId":"u"}`: "t1",
		`{"conversationId":"c1"}`:        "c1",
		`{"reason":"unauthorized"}`:      "",
		`not json`:                       "",
	} {
		if got := ThreadID(json.RawMessage(in)); got != want {
			t.Errorf("ThreadID(%s) = %q, want %q", in, got, want)
		}
	}
}
