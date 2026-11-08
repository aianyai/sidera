package cli

import (
	"reflect"
	"testing"
)

// --model and --effort reach the control request under the members the Agent
// interface takes, only when given, and only on control.
func TestControlModelAndEffortFlagsBecomeRequestMembers(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"a model and an effort", []string{"control", "ep", "session.create", "--preset", "fast", "--model", "claude-haiku-4-5", "--effort", "low"},
			map[string]any{"to": "ep", "signal": "session.create", "preset": "fast", "model": "claude-haiku-4-5", "effort": "low"}},
		{"neither", []string{"control", "ep", "session.create", "--preset", "fast"},
			map[string]any{"to": "ep", "signal": "session.create", "preset": "fast"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &command{arguments: arguments(tc.args)}
			got, err := c.messageRequest(tc.args)
			if err != nil || got.method != "control" || !reflect.DeepEqual(got.params, tc.want) {
				t.Fatalf("method %q, params %v, error %v; want params %v", got.method, got.params, err, tc.want)
			}
		})
	}
	for _, name := range []string{"send", "reply"} {
		args := []string{name, "--model", "m"}
		c := &command{arguments: arguments(args)}
		if _, err := c.messageRequest(args); err == nil {
			t.Errorf("%s accepted --model", name)
		}
	}
}
