package console

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestCommandsBecomeTheEndpointCallTheyName(t *testing.T) {
	env := Env{Getenv: func(k string) string {
		if k == "SIDERA_HOME" {
			return "/data/node"
		}
		return ""
	}, Stdin: strings.NewReader("from stdin")}
	for _, tc := range []struct {
		name string
		args []string
		want string // the request, as JSON
	}{
		{"whoami", []string{"whoami"}, `{"op":"whoami"}`},
		{"status", []string{"status", "m-1"}, `{"op":"status","status":{"endpoint":"","message_id":"m-1"}}`},
		{"recv waits as told", []string{"recv", "--wait", "7"}, `{"op":"recv","recv":{"endpoint":"","wait_s":7}}`},
		{"send to two", []string{"send", "--to", "n/a, n/b", "--body", "hi"},
			`{"op":"send","send":{"endpoint":"","to":["n/a","n/b"],"content_type":"text/plain","body":"hi"}}`},
		{"send from stdin, flags first", []string{"send", "--body-file", "-", "--to", "n/a"},
			`{"op":"send","send":{"endpoint":"","to":["n/a"],"content_type":"text/plain","body":"from stdin"}}`},
		{"reply names the message", []string{"reply", "m-1", "--body", "ok"},
			`{"op":"send","send":{"endpoint":"","to":null,"content_type":"text/plain","body":"ok","reply_to":"m-1"}}`},
		{"reply with the sender", []string{"reply", "--to", "n/a", "m-1", "--body", "ok"},
			`{"op":"send","send":{"endpoint":"","to":["n/a"],"content_type":"text/plain","body":"ok","reply_to":"m-1"}}`},
		{"new with an initial message", []string{"new", "n/rt", "--preset", "dev", "--body", "go"},
			`{"op":"send","send":{"endpoint":"","to":["n/rt"],"content_type":"text/plain","body":"go","control":{"signal":"session.create","params":{"preset":"dev"}}}}`},
		{"new without one", []string{"new", "--preset", "dev", "n/rt"},
			`{"op":"send","send":{"endpoint":"","to":["n/rt"],"body":"","control":{"signal":"session.create","params":{"preset":"dev"}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := parseCommand(tc.args, env)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(inv.req)
			if string(got) != tc.want {
				t.Errorf("request\n got %s\nwant %s", got, tc.want)
			}
			if want := "/data/node"; inv.home != want || inv.instance != AdapterID {
				t.Errorf("home %q instance %q, want %q and %q", inv.home, inv.instance, want, AdapterID)
			}
		})
	}
}

func TestCommandsRefuseWhatTheyCannotSend(t *testing.T) {
	env := Env{Getenv: func(string) string { return "" }, Stdin: strings.NewReader("")}
	for name, args := range map[string][]string{
		"unknown command":     {"frobnicate"},
		"send without --to":   {"send", "--body", "x"},
		"send without a body": {"send", "--to", "n/a"},
		"both body sources":   {"send", "--to", "n/a", "--body", "x", "--body-file", "f"},
		"new without preset":  {"new", "n/rt"},
		"new without target":  {"new", "--preset", "dev"},
		"recv waits too long": {"recv", "--wait", "31"},
		"status without id":   {"status"},
		"extra argument":      {"whoami", "x"},
	} {
		if _, err := parseCommand(args, env); err == nil {
			t.Errorf("%s: %v was accepted", name, args)
		}
	}
}

func TestTheClientFindsTheInstanceInTheStateDirectoryTheDaemonMakes(t *testing.T) {
	env := Env{Getenv: func(string) string { return "" }}
	inv, err := parseCommand([]string{"whoami", "--home", "/data/node", "--instance", "work"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stateDir(inv.home, inv.instance), filepath.Join("/data/node", "adapter-state", "work"); got != want {
		t.Errorf("state directory %q, want %q", got, want)
	}
	_ = adapterproto.MaxWaitSeconds
}
