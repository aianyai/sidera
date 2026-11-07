package cli

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/termout"
)

func TestGlobalFlagsUseTheRegisteredLocalArity(t *testing.T) {
	home, err := filepath.Abs("chosen-home")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"--json", "--home", "--", "-h"} {
		t.Run(value, func(t *testing.T) {
			local := flags("test")
			payload := local.String("new-value-flag", "", "a newly registered local flag")
			enabled := local.Bool("new-bool-flag", false, "a newly registered boolean")
			c := command{Config: Config{Getenv: func(string) string { return "" }}}
			args := []string{"--home", home, "operand", "--new-value-flag", value, "--new-bool-flag", "--json", "--", "--home"}
			before := append([]string(nil), args...)
			remaining, err := c.globals(args, local)
			if err != nil {
				t.Fatal(err)
			}
			positionals, err := parse(local, remaining)
			if err != nil {
				t.Fatal(err)
			}
			if *payload != value || !*enabled || !c.json || c.home != home || !reflect.DeepEqual(positionals, []string{"operand", "--home"}) || !reflect.DeepEqual(args, before) {
				t.Fatalf("payload %q, enabled %t, json %t, home %q, positionals %q, argv %q", *payload, *enabled, c.json, c.home, positionals, args)
			}
		})
	}
}

// The commands under `network address` and `network listen` have three words:
// discovery names them whole (so their flags are registered), and their plans send
// the method with the node and the addresses as params, reading the version from
// network.show.
func TestNetworkCommandsHaveThreeWordsAndPlanTheirMethods(t *testing.T) {
	const node = "node1abc"
	for _, tc := range []struct {
		args    []string
		path    []string
		method  string
		params  map[string]any
		usage   bool
		changes bool
	}{
		{args: []string{"network", "show"}, path: []string{"network", "show"}, method: "network.show"},
		{args: []string{"network", "address", "set", node, "/ip4/10.0.0.1/tcp/7744", "/ip4/10.0.0.2/tcp/7744"}, path: []string{"network", "address", "set"},
			method: "network.address.set", changes: true,
			params: map[string]any{"node": node, "addresses": []string{"/ip4/10.0.0.1/tcp/7744", "/ip4/10.0.0.2/tcp/7744"}}},
		{args: []string{"--home", "h", "network", "address", "remove", node, "--expected-version", "3"}, path: []string{"network", "address", "remove"},
			method: "network.address.remove", changes: true, params: map[string]any{"node": node}},
		{args: []string{"network", "listen", "set", "/ip4/0.0.0.0/tcp/7745"}, path: []string{"network", "listen", "set"},
			method: "network.listen.set", changes: true, params: map[string]any{"addresses": []string{"/ip4/0.0.0.0/tcp/7745"}}},
		{args: []string{"network", "address", "set", node}, path: []string{"network", "address", "set"}, usage: true},
		{args: []string{"network", "address", "remove"}, path: []string{"network", "address", "remove"}, usage: true},
		{args: []string{"network", "address"}, path: []string{"network", "address"}, usage: true},
		{args: []string{"network", "listen", "set"}, path: []string{"network", "listen", "set"}, usage: true},
		{args: []string{"network", "show", "extra"}, path: []string{"network", "show"}, usage: true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			if got := commandPath(tc.args); !reflect.DeepEqual(got, tc.path) {
				t.Fatalf("command path %q, want %q", got, tc.path)
			}
			c := command{Config: Config{Getenv: func(string) string { return "" }}}
			c.arguments = arguments(tc.args)
			rest, err := c.globals(tc.args, c.arguments.flags)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := c.managementPlan(rest)
			var u *usageError
			if tc.usage {
				if !errors.As(err, &u) {
					t.Fatalf("error %v, want a usage error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.method != tc.method || plan.change != tc.changes || !reflect.DeepEqual(plan.params, orEmpty(tc.params)) {
				t.Errorf("plan %+v, want %s with %v", plan, tc.method, tc.params)
			}
			if tc.changes && plan.versionMethod != "network.show" {
				t.Errorf("the version is read from %q, want network.show", plan.versionMethod)
			}
		})
	}
}

// Only the flag turns the opt-in on: without it the request carries no allow_attached,
// so the daemon refuses an attached session.
func TestEndpointCloseSendsTheOptInOnlyWithItsFlag(t *testing.T) {
	const address = "node1abc/ep1"
	for _, tc := range []struct {
		args   []string
		params map[string]any
	}{
		{[]string{"endpoint", "close", address}, map[string]any{"endpoint": address}},
		{[]string{"endpoint", "close", address, "--allow-attached"}, map[string]any{"endpoint": address, "allow_attached": true}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			c := command{Config: Config{Getenv: func(string) string { return "" }}}
			c.arguments = arguments(tc.args)
			rest, err := c.globals(tc.args, c.arguments.flags)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := c.managementPlan(rest)
			if err != nil {
				t.Fatal(err)
			}
			if plan.method != "endpoints.close" || !reflect.DeepEqual(plan.params, tc.params) {
				t.Errorf("plan %+v, want endpoints.close with %v", plan, tc.params)
			}
		})
	}
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func TestColorFlagIsGlobalAndTakesBothSpellings(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		want    termout.ColorMode
		rest    []string
		wantErr string
	}{
		{args: []string{"me"}, want: termout.ColorAuto, rest: []string{"me"}},
		{args: []string{"--color", "always", "me"}, want: termout.ColorAlways, rest: []string{"me"}},
		{args: []string{"me", "--color=never"}, want: termout.ColorNever, rest: []string{"me"}},
		{args: []string{"--color=auto", "send", "--body", "--color"}, want: termout.ColorAuto, rest: []string{"send", "--body", "--color"}},
		{args: []string{"me", "--color"}, wantErr: "--color needs auto, always or never"},
		{args: []string{"me", "--color", "sometimes"}, wantErr: "--color needs auto, always or never"},
		{args: []string{"me", "--color="}, wantErr: "--color needs auto, always or never"},
		{args: []string{"--color", "always", "me", "--json=maybe"}, want: termout.ColorAlways, wantErr: "--json needs a boolean"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			c := command{Config: Config{Getenv: func(string) string { return "" }}}
			rest, err := c.readGlobals(tc.args)
			// The error is reported through the streams, so they exist whether or not the
			// flags could be read.
			if c.out == nil || c.errOut == nil {
				t.Fatalf("streams not built: out %v, errOut %v", c.out, c.errOut)
			}
			var u *usageError
			if tc.wantErr != "" {
				if !errors.As(err, &u) || u.message != tc.wantErr || c.color != tc.want {
					t.Fatalf("err = %v, color %v; want usage error %q, color %v", err, c.color, tc.wantErr, tc.want)
				}
				return
			}
			if err != nil || c.color != tc.want || !reflect.DeepEqual(rest, tc.rest) {
				t.Fatalf("color %v, rest %q, err %v; want %v, %q", c.color, rest, err, tc.want, tc.rest)
			}
		})
	}
}
