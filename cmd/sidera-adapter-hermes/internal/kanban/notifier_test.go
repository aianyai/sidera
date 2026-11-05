package kanban

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban/kanbantest"
)

// Every value `config get kanban.notify_in_gateway` printed on Hermes 0.22.2, set
// the way the owner would set it, with what the gateway does for it.
func TestParseRecordedNotifierSettings(t *testing.T) {
	for _, tc := range []struct {
		sample  string
		enabled bool
	}{
		{"unset-default", true}, // a profile that sets nothing resolves to Hermes' default
		{"true", true},
		{"one", true},
		{"false", false},
		{"zero", false},
		{"null", false},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "config-get", tc.sample+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseNotifierSetting(string(b))
		if err != nil || got != tc.enabled {
			t.Errorf("%s: enabled = %v, err = %v; want %v", tc.sample, got, err, tc.enabled)
		}
	}
	for _, out := range []string{"", "yes", "True", "{}\n", "maybe\n"} {
		if got, err := parseNotifierSetting(out); err == nil {
			t.Errorf("%q: read as %v, want a refusal", out, got)
		}
	}
}

// The client asks Hermes the way the recording was made, for the profile asked about
// and for the default profile, which starts the gateway that serves every profile, and
// tells which of them have the notifier off. An output it cannot read and a failing
// Hermes are errors, not an answer.
func TestNotifierDisabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		profiles []string
		files    map[string]string // control directory file -> content
		want     []string
		err      error
	}{
		{"recorded default", kanbantest.ModeRecorded, []string{"feishu"}, nil, nil, nil},
		{"both off", kanbantest.ModeBoard, []string{"feishu"}, map[string]string{kanbantest.NotifierFile: "false\n"}, []string{"feishu", "default"}, nil},
		{"both on", kanbantest.ModeBoard, []string{"feishu"}, map[string]string{kanbantest.NotifierFile: "true\r\n"}, nil, nil},
		{"the profile alone is off: it may run its own gateway", kanbantest.ModeBoard, []string{"feishu"}, map[string]string{
			kanbantest.NotifierFile: "true\n", kanbantest.NotifierFileOf("feishu"): "false\n"}, []string{"feishu"}, nil},
		{"the default alone is off: it starts the shared gateway", kanbantest.ModeBoard, []string{"feishu"}, map[string]string{
			kanbantest.NotifierFile: "true\n", kanbantest.NotifierFileOf("default"): "0\n"}, []string{"default"}, nil},
		{"the default asked about is read once", kanbantest.ModeBoard, []string{"default", "feishu"}, map[string]string{
			kanbantest.NotifierFile: "null\n"}, []string{"default", "feishu"}, nil},
		{"unreadable", kanbantest.ModeBoard, []string{"feishu"}, map[string]string{kanbantest.NotifierFile: "maybe\n"}, nil, ErrUnexpectedOutput},
		{"hermes fails", kanbantest.ModeHang, []string{"feishu"}, nil, nil, ErrTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctl := t.TempDir()
			t.Setenv(kanbantest.ControlEnv, ctl)
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(ctl, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			c := fakeClient(t, tc.mode)
			ctx := context.Background()
			if tc.mode == kanbantest.ModeHang {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			got, err := c.NotifierDisabled(ctx, tc.profiles)
			if !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("disabled = %v, want %v", got, tc.want)
			}
			if err != nil && !strings.Contains(err.Error(), "profile feishu") {
				t.Errorf("error %q does not say which profile", err)
			}
		})
	}
}
